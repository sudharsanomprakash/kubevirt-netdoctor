package host

import (
	"bufio"
	"bytes"
	"strings"
)

// ProcBond is the subset of /proc/net/bonding/<bond> netdoctor uses: the
// LACP partner each member negotiated with, which iproute2 does not expose.
type ProcBond struct {
	Mode             string
	ActiveAggregator string
	Members          map[string]ProcBondMember
}

// ProcBondMember is one "Slave Interface" block.
type ProcBondMember struct {
	MIIStatus    string
	AggregatorID string
	PartnerMAC   string
}

// ParseProcBonding parses the text of /proc/net/bonding/<bond>.
func ParseProcBonding(data []byte) ProcBond {
	b := ProcBond{Members: map[string]ProcBondMember{}}
	var member string
	var inActiveAgg, inPartnerPDU bool
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		raw := sc.Text()
		key, value, ok := strings.Cut(strings.TrimSpace(raw), ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch {
		case key == "Bonding Mode":
			b.Mode = value
		case key == "Active Aggregator Info":
			inActiveAgg = true
		case key == "Slave Interface":
			member, inActiveAgg, inPartnerPDU = value, false, false
			b.Members[member] = ProcBondMember{}
		case key == "details partner lacp pdu":
			inPartnerPDU = true
		case key == "details actor lacp pdu":
			inPartnerPDU = false
		case inActiveAgg && key == "Aggregator ID" && member == "":
			b.ActiveAggregator = value
			inActiveAgg = false
		case member != "":
			m := b.Members[member]
			switch {
			case key == "MII Status":
				m.MIIStatus = value
			case key == "Aggregator ID":
				m.AggregatorID = value
			case inPartnerPDU && key == "system mac address":
				m.PartnerMAC = value
			}
			b.Members[member] = m
		}
	}
	return b
}
