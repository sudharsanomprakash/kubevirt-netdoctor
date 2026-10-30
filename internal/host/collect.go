package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
)

// Runner executes the collector's fixed commands. It exists so tests can
// replace process execution.
type Runner interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs commands with os/exec, never through a shell.
type ExecRunner struct{}

// LookPath implements Runner.
func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

// Run implements Runner. Stderr is included in the returned error.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- name and args come from the fixed allowlist below
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, msg)
	}
	return stdout.Bytes(), nil
}

// CollectOptions configures Collect.
type CollectOptions struct {
	// NodeName is recorded in the snapshot; it should be the Kubernetes node
	// name, which may differ from the hostname.
	NodeName string
	Hostname string
	// SkipOVS disables all OVS collection.
	SkipOVS bool
	// SkipFlows disables OpenFlow table summaries.
	SkipFlows bool
	// CommandTimeout bounds each command. Zero means 10 seconds.
	CommandTimeout time.Duration
	Now            func() time.Time
}

// The complete set of commands the collector may run. Every invocation is
// read-only. No argument is ever taken from user input; the only dynamic
// argument is an OVS bridge name read from OVSDB, validated by
// validBridgeName before use.
var (
	cmdLinks       = []string{"ip", "-j", "-d", "link", "show"}
	cmdAddrs       = []string{"ip", "-j", "addr", "show"}
	cmdRoutes4     = []string{"ip", "-j", "-4", "route", "show", "table", "all"}
	cmdRoutes6     = []string{"ip", "-j", "-6", "route", "show", "table", "all"}
	cmdBridgeVLANs = []string{"bridge", "-j", "vlan", "show"}
	cmdNetns       = []string{"ip", "-j", "netns", "list"}
	cmdOVSBridges  = []string{"ovs-vsctl", "--format=json", "--data=json", "--columns=_uuid,name,ports,fail_mode,datapath_type", "list", "Bridge"}
	cmdOVSPorts    = []string{"ovs-vsctl", "--format=json", "--data=json", "--columns=_uuid,name,interfaces,tag,trunks,vlan_mode,bond_mode,lacp,external_ids", "list", "Port"}
	cmdOVSIfaces   = []string{"ovs-vsctl", "--format=json", "--data=json", "--columns=_uuid,name,type,ofport,admin_state,link_state,mtu,mac_in_use,error,external_ids", "list", "Interface"}
	cmdOVSBondShow = []string{"ovs-appctl", "bond/show"}
	cmdOVSLACPShow = []string{"ovs-appctl", "lacp/show"}
)

var bridgeNameRE = regexp.MustCompile(`^[A-Za-z0-9_.:][A-Za-z0-9_.:-]{0,63}$`)

func validBridgeName(name string) bool { return bridgeNameRE.MatchString(name) }

type collector struct {
	ctx     context.Context
	runner  Runner
	timeout time.Duration
	snap    *Snapshot
}

// Collect gathers a host snapshot. It never fails as a whole: each source
// records its own status, so a snapshot taken without privileges still
// carries everything that could be read. Only context cancellation aborts.
//
// procFS must be rooted at the host's "/" (os.DirFS("/") in production).
func Collect(ctx context.Context, runner Runner, procFS fs.FS, opts CollectOptions) (*Snapshot, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	c := &collector{
		ctx:     ctx,
		runner:  runner,
		timeout: opts.CommandTimeout,
		snap: &Snapshot{
			SchemaVersion: SchemaVersion,
			NodeName:      opts.NodeName,
			Hostname:      opts.Hostname,
			CollectedAt:   now().UTC().Format(time.RFC3339),
		},
	}
	if c.timeout == 0 {
		c.timeout = 10 * time.Second
	}

	c.collectLinux(procFS)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.SkipOVS {
		c.record(SourceOVSDB, StatusSkipped, nil, "disabled by --skip-ovs")
	} else {
		c.collectOVS(!opts.SkipFlows)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.snap, nil
}

func (c *collector) record(name, status string, cmd []string, msg string) {
	c.snap.Sources = append(c.snap.Sources, SourceStatus{Name: name, Status: status, Command: cmd, Message: msg})
}

// run executes a command and records failures. ok is false when the source
// must be treated as unavailable; empty output with ok=true is valid.
func (c *collector) run(source string, argv []string) (out []byte, ok bool) {
	if _, err := c.runner.LookPath(argv[0]); err != nil {
		c.record(source, StatusNotInstalled, argv, argv[0]+" is not installed")
		return nil, false
	}
	ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
	defer cancel()
	out, err := c.runner.Run(ctx, argv[0], argv[1:]...)
	if err != nil {
		msg := err.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg = fmt.Sprintf("timed out after %s", c.timeout)
		} else if isPermissionError(msg) {
			msg += " (this source requires elevated privileges; see the host collector section of the README)"
		}
		c.record(source, StatusFailed, argv, msg)
		return nil, false
	}
	return out, true
}

func isPermissionError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "permission denied") || strings.Contains(m, "operation not permitted")
}

func (c *collector) collectLinux(procFS fs.FS) {
	if out, ok := c.run(SourceLinks, cmdLinks); ok {
		links, err := ParseLinks(out)
		if err != nil {
			c.record(SourceLinks, StatusFailed, cmdLinks, err.Error())
		} else {
			c.snap.Interfaces = links
			c.record(SourceLinks, StatusOK, cmdLinks, "")
		}
	}
	if out, ok := c.run(SourceAddresses, cmdAddrs); ok {
		addrs, err := ParseAddresses(out)
		if err != nil {
			c.record(SourceAddresses, StatusFailed, cmdAddrs, err.Error())
		} else {
			for i := range c.snap.Interfaces {
				c.snap.Interfaces[i].Addresses = addrs[c.snap.Interfaces[i].Name]
			}
			c.record(SourceAddresses, StatusOK, cmdAddrs, "")
		}
	}

	routesOK := true
	for _, v := range []struct {
		argv []string
		ipv6 bool
	}{{cmdRoutes4, false}, {cmdRoutes6, true}} {
		out, ok := c.run(SourceRoutes, v.argv)
		if !ok {
			routesOK = false
			continue
		}
		routes, err := ParseRoutes(out, v.ipv6)
		if err != nil {
			c.record(SourceRoutes, StatusFailed, v.argv, err.Error())
			routesOK = false
			continue
		}
		c.snap.Routes = append(c.snap.Routes, routes...)
	}
	if routesOK {
		c.record(SourceRoutes, StatusOK, nil, "")
	}

	if out, ok := c.run(SourceBridgeVLANs, cmdBridgeVLANs); ok {
		vlans, err := ParseBridgeVLANs(out)
		if err != nil {
			c.record(SourceBridgeVLANs, StatusFailed, cmdBridgeVLANs, err.Error())
		} else {
			c.snap.BridgeVLANs = vlans
			c.record(SourceBridgeVLANs, StatusOK, cmdBridgeVLANs, "")
		}
	}
	if out, ok := c.run(SourceNamespaces, cmdNetns); ok {
		ns, err := ParseNamespaces(out)
		if err != nil {
			c.record(SourceNamespaces, StatusFailed, cmdNetns, err.Error())
		} else {
			c.snap.Namespaces = ns
			c.record(SourceNamespaces, StatusOK, cmdNetns, "")
		}
	}
	c.collectBonding(procFS)
}

// collectBonding enriches bond members with LACP partner MACs from
// /proc/net/bonding, which iproute2 does not report.
func (c *collector) collectBonding(procFS fs.FS) {
	hasBond := false
	for _, i := range c.snap.Interfaces {
		if i.Bond != nil {
			hasBond = true
		}
	}
	if !hasBond {
		c.record(SourceBonding, StatusSkipped, nil, "no Linux bonds present")
		return
	}
	var failures []string
	for i := range c.snap.Interfaces {
		bond := c.snap.Interfaces[i]
		if bond.Bond == nil {
			continue
		}
		data, err := fs.ReadFile(procFS, path.Join("proc/net/bonding", bond.Name))
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", bond.Name, err))
			continue
		}
		pb := ParseProcBonding(data)
		for name, m := range pb.Members {
			if iface := c.snap.FindInterface(name); iface != nil && iface.BondMember != nil {
				iface.BondMember.PartnerMAC = m.PartnerMAC
			}
		}
	}
	if len(failures) > 0 {
		c.record(SourceBonding, StatusFailed, nil, strings.Join(failures, "; "))
		return
	}
	c.record(SourceBonding, StatusOK, nil, "")
}

func (c *collector) collectOVS(flows bool) {
	bridges, ok := c.run(SourceOVSDB, cmdOVSBridges)
	if !ok {
		return
	}
	ports, okPorts := c.run(SourceOVSDB, cmdOVSPorts)
	ifaces, okIfaces := c.run(SourceOVSDB, cmdOVSIfaces)
	if !okPorts || !okIfaces {
		return
	}
	ovs, err := ParseOVSDB(bridges, ports, ifaces)
	if err != nil {
		c.record(SourceOVSDB, StatusFailed, nil, err.Error())
		return
	}
	c.snap.OVS = ovs
	c.record(SourceOVSDB, StatusOK, nil, "")

	if out, ok := c.run(SourceOVSBonds, cmdOVSBondShow); ok {
		ovs.Bonds = ParseOVSBondShow(out)
		c.record(SourceOVSBonds, StatusOK, cmdOVSBondShow, "")
		if lacp, ok := c.run(SourceOVSLACP, cmdOVSLACPShow); ok {
			ovs.Bonds = MergeOVSLACPShow(ovs.Bonds, lacp)
			c.record(SourceOVSLACP, StatusOK, cmdOVSLACPShow, "")
		}
	}

	if !flows {
		c.record(SourceOVSFlows, StatusSkipped, nil, "disabled by --skip-flows")
		return
	}
	if _, err := c.runner.LookPath("ovs-ofctl"); err != nil {
		c.record(SourceOVSFlows, StatusNotInstalled, []string{"ovs-ofctl"}, "ovs-ofctl is not installed")
		return
	}
	var failures []string
	for i := range ovs.Bridges {
		name := ovs.Bridges[i].Name
		if !validBridgeName(name) {
			failures = append(failures, fmt.Sprintf("%q: name rejected by validation", name))
			continue
		}
		argv := []string{"ovs-ofctl", "dump-flows", name}
		ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
		out, err := c.runner.Run(ctx, argv[0], argv[1:]...)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		summary := ParseOVSFlows(out)
		ovs.Bridges[i].Flows = &summary
	}
	if len(failures) > 0 {
		c.record(SourceOVSFlows, StatusFailed, []string{"ovs-ofctl", "dump-flows", "<bridge>"}, strings.Join(failures, "; "))
		return
	}
	c.record(SourceOVSFlows, StatusOK, []string{"ovs-ofctl", "dump-flows", "<bridge>"}, "")
}
