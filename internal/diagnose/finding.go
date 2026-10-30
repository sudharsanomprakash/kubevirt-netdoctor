// Package diagnose evaluates diagnostic rules against an observed topology
// and produces structured findings.
//
// Every finding states how certain it is (Category). Rules only make claims
// that the Kubernetes API state supports; they never report connectivity as
// working or broken, because v0.1 performs no data-plane tests.
package diagnose

import (
	"sort"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/expect"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/host"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/hostpath"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/route"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/topology"
)

// Severity ranks the impact of a finding.
type Severity string

const (
	SeverityError   Severity = "Error"
	SeverityWarning Severity = "Warning"
	SeverityInfo    Severity = "Info"
)

func (s Severity) rank() int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

// Category states how certain a finding is.
type Category string

const (
	// Confirmed findings are directly established by API state, for example
	// a referenced object that does not exist.
	Confirmed Category = "Confirmed"
	// Suspected findings are consistent with a problem but have plausible
	// benign explanations (timing, version differences, missing telemetry).
	Suspected Category = "Suspected"
	// Unavailable findings record information netdoctor could not observe,
	// due to permissions, missing APIs or missing telemetry.
	Unavailable Category = "Unavailable"
)

// Finding is one diagnostic result.
type Finding struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Category Category `json:"category"`
	// Resource identifies the object the finding is about, as Kind ns/name.
	Resource string   `json:"resource"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence,omitempty"`
	// Expected is the state the check compared against, when known, and
	// where that expectation came from (NAD configuration, VMI spec or the
	// user's expectations file).
	Expected   string `json:"expected,omitempty"`
	NextAction string `json:"nextAction,omitempty"`
}

// Input is everything rules may inspect. Only Topology is always set; the
// other fields are nil when the corresponding data was not requested or
// not available, and rules depending on them then stay silent.
type Input struct {
	Topology *topology.Topology
	// Snapshot and Host are set when a host snapshot was supplied. Host is
	// the correlation of the snapshot with the topology.
	Snapshot *host.Snapshot
	Host     *hostpath.Result
	Expect   *expect.Spec
	Route    *route.Analysis
}

// Rule inspects the topology and returns zero or more findings.
type Rule struct {
	// ID is the rule identifier; every finding the rule emits uses an ID
	// from its documented set.
	ID          string
	Description string
	Check       func(*Input) []Finding
}

// onTopology adapts a rule that only needs the Kubernetes topology.
func onTopology(check func(*topology.Topology) []Finding) func(*Input) []Finding {
	return func(in *Input) []Finding { return check(in.Topology) }
}

// Evaluate runs the rules against the topology and returns the findings
// ordered by severity, then ID, then resource, for stable output.
func Evaluate(in *Input, rules []Rule) []Finding {
	var out []Finding
	for _, r := range rules {
		out = append(out, r.Check(in)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Severity.rank() != b.Severity.rank() {
			return a.Severity.rank() < b.Severity.rank()
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Resource < b.Resource
	})
	if out == nil {
		out = []Finding{}
	}
	return out
}
