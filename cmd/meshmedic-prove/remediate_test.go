package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/Git-Hospital/meshmedic/pkg/catalog"
	"github.com/Git-Hospital/meshmedic/pkg/proof"
)

func patchingEntry() catalog.Scenario {
	return catalog.Scenario{
		ID: "error-surge",
		Remediation: catalog.Remediation{
			Action:        "enable-outlier-detection",
			PatchTemplate: "kind: DestinationRule\nmetadata:\n  namespace: {{.namespace}}\n",
		},
	}
}

func remediationSpec() proof.Spec {
	return proof.Spec{
		Entry:  "error-surge",
		Target: map[string]string{"namespace": "demo"},
		Inject: []proof.Command{{Run: []string{"kubectl", "--context=kind-test", "set", "env", "x"}}},
		Reset:  []proof.Command{{Run: []string{"kubectl", "--context=kind-test", "delete", "reset-fault"}}},
		Remediation: &proof.RemediationCheck{
			Revert: []proof.Command{{Run: []string{"kubectl", "--context=kind-test", "delete", "the-patch"}}},
		},
	}
}

// Hypothesis 8. The acknowledgement covered the fault and not the patch
// written on top of it. An operator agreeing to a remediation run has to see
// the bytes that will be applied, the command that applies them, and the
// revert that removes them.
func TestConfirmationShowsThePatchBeforeApplyingIt(t *testing.T) {
	specs := []proof.Spec{remediationSpec()}
	plans := []proof.RemediationPlan{proof.PlanRemediation([]catalog.Scenario{patchingEntry()}, specs[0])}

	var buf bytes.Buffer
	if !confirmed(&buf, specs, plans, true) {
		t.Fatal("an acknowledged run was refused")
	}
	out := buf.String()
	for _, want := range []string{
		"kubectl --context=kind-test apply -f -",
		"| kind: DestinationRule",
		"|   namespace: demo",
		"revert  kubectl --context=kind-test delete the-patch",
		"reset   kubectl --context=kind-test delete reset-fault",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("confirmation is missing %q:\n%s", want, out)
		}
	}
}

// An entry that will not be run says so instead of listing a fault that will
// never be injected.
func TestConfirmationSaysWhichEntriesWillNotRun(t *testing.T) {
	spec := remediationSpec()
	spec.Remediation = nil
	specs := []proof.Spec{spec}
	plans := []proof.RemediationPlan{proof.PlanRemediation([]catalog.Scenario{patchingEntry()}, spec)}

	var buf bytes.Buffer
	confirmed(&buf, specs, plans, true)
	out := buf.String()
	if strings.Contains(out, "inject  ") {
		t.Errorf("a fault that will not be injected was listed:\n%s", out)
	}
	if !strings.Contains(out, "no remediation check") {
		t.Errorf("the reason is missing:\n%s", out)
	}
}

// The ordinary proof's confirmation is unchanged.
func TestConfirmationWithoutPlansListsInjectAndReset(t *testing.T) {
	var buf bytes.Buffer
	confirmed(&buf, []proof.Spec{remediationSpec()}, nil, false)
	out := buf.String()
	if strings.Contains(out, "apply") || strings.Contains(out, "revert") {
		t.Errorf("an ordinary proof run mentions a patch:\n%s", out)
	}
	if !strings.Contains(out, "inject  kubectl") || !strings.Contains(out, "Refusing") {
		t.Errorf("confirmation = \n%s", out)
	}
}

// Hypothesis 9. A run that proved nothing exited 0, so a script read "every
// entry skipped" as "the remedies work".
func TestRemediationSummaryFailsWhenNothingWasProven(t *testing.T) {
	cases := []struct {
		name    string
		results []proof.RemediationResult
		want    int
	}{
		{"nothing selected", nil, 1},
		{"all report-only", []proof.RemediationResult{{Entry: "a", Skipped: "report-only"}}, 1},
		{"all unchecked", []proof.RemediationResult{{Entry: "a", Unchecked: "no check"}}, 1},
		{"one proven among skips", []proof.RemediationResult{
			{Entry: "a", Skipped: "report-only"},
			{Entry: "b", Cleared: true},
		}, 0},
		{"a blind run", []proof.RemediationResult{{Entry: "b", Cleared: true}, {Entry: "c", Blind: true}}, 1},
		{"a failed run", []proof.RemediationResult{{Entry: "b", Cleared: true}, {Entry: "c", Applied: true}}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summarizeRemediation(io.Discard, c.results); got != c.want {
				t.Errorf("exit = %d, want %d", got, c.want)
			}
		})
	}
}

// Review finding 3, the summary half. A setup error, a cleanup failure after a
// held clear, and a patch that did not work are three different facts, and
// the summary told all three as "proposed a change that did not stop it".
func TestRemediationSummaryNamesWhatFailed(t *testing.T) {
	cases := []struct {
		name    string
		results []proof.RemediationResult
		want    int
		says    []string
		never   []string
	}{
		{
			name:    "a setup error",
			results: []proof.RemediationResult{{Entry: "a", Errors: []string{"inject failed"}}},
			want:    1,
			says:    []string{"ERROR", "could not be set up"},
			never:   []string{"did not stop it", "did not end"},
		},
		{
			name: "a pass whose cleanup failed",
			results: []proof.RemediationResult{{Entry: "a", Fired: true, Applied: true, Cleared: true,
				CleanupErrors: []string{"RESET FAILED (x): boom"}}},
			want:  1,
			says:  []string{"PASS", "FAILED", "the patch ended the incident", "may still carry"},
			never: []string{"did not stop it", "did not end"},
		},
		{
			name:    "a patch that did not work",
			results: []proof.RemediationResult{{Entry: "a", Fired: true, Applied: true, Reverted: true, Reset: true, Failures: []string{"still in breach"}}},
			want:    1,
			says:    []string{"FAIL", "did not end a held breach"},
		},
		{
			name:    "a clean pass",
			results: []proof.RemediationResult{{Entry: "a", Fired: true, Applied: true, Cleared: true, Reverted: true, Reset: true}},
			want:    0,
			says:    []string{"PASS", "1 proved"},
			never:   []string{"FAILED"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			if got := summarizeRemediation(&buf, c.results); got != c.want {
				t.Errorf("exit = %d, want %d", got, c.want)
			}
			out := buf.String()
			for _, s := range c.says {
				if !strings.Contains(out, s) {
					t.Errorf("summary does not say %q:\n%s", s, out)
				}
			}
			for _, s := range c.never {
				if strings.Contains(out, s) {
					t.Errorf("summary says %q:\n%s", s, out)
				}
			}
		})
	}
}
