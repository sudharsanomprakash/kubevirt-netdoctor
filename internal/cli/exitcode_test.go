package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/diagnose"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/testutil/fakecluster"
)

func TestCheckFailOn(t *testing.T) {
	fs := []diagnose.Finding{{Severity: diagnose.SeverityWarning}, {Severity: diagnose.SeverityInfo}}
	cases := map[string]int{FailOnNone: 0, FailOnError: 0, FailOnWarning: 1, FailOnInfo: 2}
	for th, want := range cases {
		err := checkFailOn(th, fs)
		var fe *FindingsError
		switch {
		case want == 0 && err != nil:
			t.Errorf("%s: unexpected %v", th, err)
		case want > 0 && (!errors.As(err, &fe) || fe.Count != want):
			t.Errorf("%s: err = %v, want %d findings", th, err, want)
		}
	}
	if ExitCode(nil) != ExitOK || ExitCode(errors.New("x")) != ExitError || ExitCode(&FindingsError{}) != ExitFindings {
		t.Error("exit code mapping")
	}
}

func TestFailOnCLI(t *testing.T) {
	broken := fakecluster.New(t, []string{fixture("stopped-invalid-nad.yaml")})
	out, err := run(t, broken, "diagnose", "vm/batch-04", "-n", "demo", "--fail-on", "error", "-o", "json")
	if ExitCode(err) != ExitFindings {
		t.Fatalf("broken VM with --fail-on error: err = %v", err)
	}
	if !strings.Contains(out, `"findings"`) {
		t.Error("the report must still be written before failing")
	}
	if _, err := run(t, broken, "inspect", "vm/batch-04", "-n", "demo", "--fail-on", "error"); ExitCode(err) != ExitFindings {
		t.Errorf("inspect --fail-on error: %v", err)
	}
	if _, err := run(t, broken, "diagnose", "vm/batch-04", "-n", "demo"); err != nil {
		t.Errorf("default --fail-on none must not fail: %v", err)
	}
	healthy := fakecluster.New(t, []string{fixture("bridge-vlan-running.yaml")})
	if _, err := run(t, healthy, "inspect", "vm/web-01", "-n", "demo", "--fail-on", "warning"); err != nil {
		t.Errorf("healthy VM: %v", err)
	}
	if _, err := run(t, healthy, "inspect", "vm/web-01", "-n", "demo", "--fail-on", "sometimes"); ExitCode(err) != ExitError {
		t.Errorf("invalid threshold: %v", err)
	}
}
