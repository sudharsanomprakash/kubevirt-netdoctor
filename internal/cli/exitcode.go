package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/diagnose"
)

// Exit codes. They are part of the CLI contract (docs/cli.md).
const (
	ExitOK       = 0 // command succeeded; no finding reached --fail-on
	ExitError    = 1 // the command itself failed (bad input, API error, ...)
	ExitFindings = 2 // the report was written and a finding reached --fail-on
)

// Fail-on thresholds.
const (
	FailOnNone    = "none"
	FailOnError   = "error"
	FailOnWarning = "warning"
	FailOnInfo    = "info"
)

// FindingsError reports that findings reached the --fail-on threshold. The
// report has already been written when it is returned.
type FindingsError struct {
	Threshold string
	Count     int
}

func (e *FindingsError) Error() string {
	return fmt.Sprintf("%d finding(s) at or above --fail-on=%s", e.Count, e.Threshold)
}

// ExitCode maps a command error to the process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var fe *FindingsError
	if errors.As(err, &fe) {
		return ExitFindings
	}
	return ExitError
}

func addFailOnFlag(cmd *cobra.Command, v *string) {
	cmd.Flags().StringVar(v, "fail-on", FailOnNone,
		"Exit with code 2 when a finding has at least this severity: none, info, warning or error")
}

func validateFailOn(v string) error {
	switch v {
	case FailOnNone, FailOnError, FailOnWarning, FailOnInfo:
		return nil
	}
	return fmt.Errorf("invalid --fail-on %q (use none, info, warning or error)", v)
}

// checkFailOn returns a FindingsError when findings reach the threshold.
func checkFailOn(threshold string, findings []diagnose.Finding) error {
	rank := map[diagnose.Severity]int{diagnose.SeverityError: 3, diagnose.SeverityWarning: 2, diagnose.SeverityInfo: 1}
	min := map[string]int{FailOnError: 3, FailOnWarning: 2, FailOnInfo: 1}[threshold]
	if min == 0 {
		return nil
	}
	n := 0
	for _, f := range findings {
		if rank[f.Severity] >= min {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return &FindingsError{Threshold: threshold, Count: n}
}
