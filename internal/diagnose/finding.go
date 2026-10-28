// Package diagnose evaluates diagnostic rules against an observed topology
// and produces structured findings.
//
// Every finding states how certain it is (Category). Rules only make claims
// that the Kubernetes API state supports; they never report connectivity as
// working or broken, because v0.1 performs no data-plane tests.
package diagnose

import (
	"sort"

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
	Resource   string   `json:"resource"`
	Summary    string   `json:"summary"`
	Evidence   []string `json:"evidence,omitempty"`
	NextAction string   `json:"nextAction,omitempty"`
}

// Rule inspects the topology and returns zero or more findings.
type Rule struct {
	// ID is the rule identifier; every finding the rule emits uses an ID
	// from its documented set.
	ID          string
	Description string
	Check       func(*topology.Topology) []Finding
}

// Evaluate runs the rules against the topology and returns the findings
// ordered by severity, then ID, then resource, for stable output.
func Evaluate(t *topology.Topology, rules []Rule) []Finding {
	var out []Finding
	for _, r := range rules {
		out = append(out, r.Check(t)...)
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
