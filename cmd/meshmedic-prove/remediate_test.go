package main

import (
	"bytes"
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
		Reset:  []proof.Command{{Run: []string{"kubectl", "--context=kind-test", "reset-fault"}}},
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
		"reset   kubectl --context=kind-test reset-fault",
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
			if got := summarizeRemediation(c.results); got != c.want {
				t.Errorf("exit = %d, want %d", got, c.want)
			}
		})
	}
}
