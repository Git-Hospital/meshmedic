package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRealCatalog(t *testing.T) {
	scenarios, err := LoadDir("../../catalog")
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately no count assertion. A magic number here has to be edited
	// on every legitimate catalog change, which trains people to bump it
	// without reading the diff, and an accidental deletion is caught properly
	// by the lock instead: an approval left behind by a removed entry fails
	// TestLockHasNoStaleApprovals and the CI drift gate.
	if len(scenarios) == 0 {
		t.Fatal("catalog is empty")
	}
	for i := 1; i < len(scenarios); i++ {
		if scenarios[i-1].ID >= scenarios[i].ID {
			t.Fatalf("scenarios not sorted by id: %q before %q", scenarios[i-1].ID, scenarios[i].ID)
		}
	}
	for _, s := range scenarios {
		if s.Rollback == "" {
			t.Errorf("%s: every scenario must document its rollback", s.ID)
		}
		if s.Signal.For == "" {
			t.Errorf("%s: signal.for is empty, transient blips would fire remediation", s.ID)
		}
	}
}

// writeScenarios lays out a temp catalog where each entry may suppress others.
func writeScenarios(t *testing.T, suppresses map[string][]string) string {
	t.Helper()
	dir := t.TempDir()
	for id, sup := range suppresses {
		doc := fmt.Sprintf(`id: %s
title: %s
severity: warning
description: a test entry
rollback: revert the pull request
signal:
  promql: up
  comparison: ">"
  threshold: 1
remediation:
  target:
    kind: VirtualService
  patchTemplate: "kind: VirtualService"
`, id, id)
		for _, s := range sup {
			doc += fmt.Sprintf("suppresses:\n  - %s\n", s)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadRejectsUnknownSuppressReference(t *testing.T) {
	dir := writeScenarios(t, map[string][]string{"a": {"ghost"}})
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("got %v, want unknown-reference error naming ghost", err)
	}
}

func TestLoadRejectsSelfSuppression(t *testing.T) {
	dir := writeScenarios(t, map[string][]string{"a": {"a"}})
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("got %v, want self-suppression error", err)
	}
}

func TestLoadRejectsMutualSuppression(t *testing.T) {
	dir := writeScenarios(t, map[string][]string{"a": {"b"}, "b": {"a"}})
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "each other") {
		t.Fatalf("got %v, want mutual-suppression error", err)
	}
}

// validScenario is the smallest entry Validate accepts. Each rejection test
// breaks one field of it, so it cannot pass because of some other missing field.
func validScenario() Scenario {
	return Scenario{
		ID:          "x",
		Title:       "x",
		Severity:    "warning",
		Description: "what is wrong and why",
		Signal:      Signal{PromQL: "up", Comparison: ">", Threshold: 1},
		Remediation: Remediation{
			Target:        Target{Kind: "VirtualService"},
			PatchTemplate: "kind: VirtualService",
		},
		Guardrails: Guardrails{MaxAppliesPerHour: 1},
		Rollback:   "revert the pull request",
	}
}

func TestValidateAcceptsTheSmallestEntry(t *testing.T) {
	if err := validScenario().Validate(); err != nil {
		t.Fatalf("validScenario does not validate: %v", err)
	}
}

func TestValidateRejectsBadComparison(t *testing.T) {
	s := validScenario()
	s.Signal.Comparison = "~"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "signal.comparison") {
		t.Fatalf("got %v, want an error naming signal.comparison", err)
	}
}

func TestValidateRejectsBrokenTemplate(t *testing.T) {
	s := validScenario()
	s.Remediation.PatchTemplate = "kind: {{.unclosed"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "patchTemplate") {
		t.Fatalf("got %v, want an error naming patchTemplate", err)
	}
}

// CONTRIBUTING.md asks every entry for a description, a rollback note and
// guardrails. An entry without a rollback note used to load and open a pull
// request whose Rollback section was empty.
func TestValidateRejectsMissingDocumentedFields(t *testing.T) {
	for name, tc := range map[string]struct {
		break_ func(*Scenario)
		want   string
	}{
		"empty description":      {func(s *Scenario) { s.Description = "" }, "description"},
		"blank description":      {func(s *Scenario) { s.Description = " \n" }, "description"},
		"empty rollback":         {func(s *Scenario) { s.Rollback = "" }, "rollback"},
		"blank rollback":         {func(s *Scenario) { s.Rollback = "\t\n" }, "rollback"},
		"empty severity":         {func(s *Scenario) { s.Severity = "" }, "severity"},
		"unknown severity":       {func(s *Scenario) { s.Severity = "info" }, "severity"},
		"severity in other case": {func(s *Scenario) { s.Severity = "Critical" }, "severity"},
		"negative applies":       {func(s *Scenario) { s.Guardrails.MaxAppliesPerHour = -1 }, "guardrails.maxAppliesPerHour"},
	} {
		t.Run(name, func(t *testing.T) {
			s := validScenario()
			tc.break_(&s)
			if err := s.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error naming %s", err, tc.want)
			}
		})
	}
}

// Deleting a catalog entry used to pass CI in silence. The lock knew, because
// the approval it left behind no longer had an entry, but nothing failed on
// it, so a detection could disappear and only the count in a test would
// notice. Retirement should be a deliberate act with a record, not a diff
// nobody had to acknowledge.
func TestLockHasNoStaleApprovals(t *testing.T) {
	scenarios, err := LoadDir("../../catalog")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := LoadLock("../../catalog.lock")
	if err != nil {
		t.Fatal(err)
	}
	if stale := lock.Stale(scenarios); len(stale) > 0 {
		t.Errorf("catalog.lock approves %v, which no longer exist in the catalog.\n"+
			"An entry was removed without the approval being retired. Run `meshmedic approve --prune`\n"+
			"and record why it went in catalog/RETIRED.md.", stale)
	}
}
