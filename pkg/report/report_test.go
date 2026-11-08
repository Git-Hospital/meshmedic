package report

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Git-Hospital/meshmedic/pkg/catalog"
	"github.com/Git-Hospital/meshmedic/pkg/detect"
	"github.com/Git-Hospital/meshmedic/pkg/kube"
	"github.com/Git-Hospital/meshmedic/pkg/prom"
)

func TestMarkdownContainsTheStoryAndThePatch(t *testing.T) {
	inc := detect.Incident{
		Scenario: catalog.Scenario{
			ID:          "canary-latency-rollback",
			Title:       "Canary subset latency regression",
			Severity:    "critical",
			Description: "The canary is slow.",
			Signal:      catalog.Signal{Comparison: ">", Threshold: 1000, For: "90s"},
			Remediation: catalog.Remediation{Target: catalog.Target{Kind: "VirtualService"}},
			Guardrails:  catalog.Guardrails{RequiresApproval: true},
			Rollback:    "Restore previous weights.",
		},
		Params: map[string]string{"service": "payments", "namespace": "prod"},
		Value:  2412.7,
		Since:  time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		Evidence: []detect.EvidenceResult{
			{Name: "p99-stable-for-comparison", Samples: []prom.Sample{{Value: 84.2}}},
			{Name: "errors-by-workload", Samples: []prom.Sample{
				{Labels: map[string]string{"destination_workload": "payments-v1"}, Value: 0.002},
				{Labels: map[string]string{"destination_workload": "payments-v2"}, Value: 0.19},
			}},
			{Name: "canary-request-share", Err: errors.New("scrape failed")},
		},
	}
	out := Markdown(inc, "kind: VirtualService\n")

	for _, want := range []string{
		"Canary subset latency regression",
		"`namespace=prod` `service=payments`",
		"2413",
		"2026-07-17T12:00:00Z",
		"p99-stable-for-comparison | 84.2",
		`errors-by-workload{destination_workload="payments-v2"} | 0.19`,
		"canary-request-share | unavailable",
		"```yaml\nkind: VirtualService\n```",
		"Restore previous weights.",
		"requires human approval",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, out)
		}
	}

	// Triage sections render matched log lines and the rollout diff.
	inc.LogEvidence = []detect.LogEvidenceResult{{
		Name: "client-failure-log-sweep",
		Matches: map[string][]string{
			"loadgen": {"curl: (6) Could not resolve host: payments-svc.demo"},
		},
	}}
	inc.RolloutEvidence = []detect.RolloutEvidenceResult{{
		Name:     "recent-rollouts",
		Rollouts: []kube.Rollout{{Deployment: "loadgen", AgeSeconds: 120, Diff: "- old-target\n+ new-target"}},
	}}
	out = Markdown(inc, "kind: VirtualService\n")
	for _, want := range []string{
		"### Log evidence",
		"Could not resolve host: payments-svc.demo",
		"### Recent rollouts",
		"`loadgen` rolled 120s ago",
		"+ new-target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("triage markdown missing %q\n---\n%s", want, out)
		}
	}

	// The breakdown must lead with the offender: v2's row above v1's.
	v2 := strings.Index(out, `{destination_workload="payments-v2"}`)
	v1 := strings.Index(out, `{destination_workload="payments-v1"}`)
	if v2 == -1 || v1 == -1 || v2 > v1 {
		t.Errorf("evidence rows not ranked biggest-first (v2 at %d, v1 at %d)\n---\n%s", v2, v1, out)
	}
}

func TestResolvedReportsTheClosingStory(t *testing.T) {
	res := detect.Resolution{
		Scenario: catalog.Scenario{
			ID:     "canary-latency-rollback",
			Title:  "Canary subset latency regression",
			Signal: catalog.Signal{Comparison: ">", Threshold: 1000},
		},
		Params:     map[string]string{"service": "payments", "namespace": "prod"},
		Value:      142.5,
		Threshold:  1000,
		Since:      time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		ResolvedAt: time.Date(2026, 7, 17, 12, 4, 30, 0, time.UTC),
	}
	out := Resolved(res)
	for _, want := range []string{
		"## Resolved: Canary subset latency regression",
		"`canary-latency-rollback`",
		"`namespace=prod` `service=payments`",
		"has recovered",
		"back to **142.5**",
		"2026-07-17T12:00:00Z",
		"2026-07-17T12:04:30Z",
		"duration of 4m30s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("resolved report missing %q\n---\n%s", want, out)
		}
	}
}

// Text from the cluster must not close a code block, add a table column or end a list item
// early, and an empty signal.for must not leave "for  since" in the headline (#19).
func TestMarkdownKeepsClusterTextInsideItsBlock(t *testing.T) {
	inc := detect.Incident{
		Scenario: catalog.Scenario{
			ID:          "error-surge-outlier-ejection",
			Title:       "Error surge",
			Signal:      catalog.Signal{Comparison: ">", Threshold: 0.05},
			Remediation: catalog.Remediation{Target: catalog.Target{Kind: "DestinationRule"}},
		},
		Value: 0.2,
		Since: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC),
		Evidence: []detect.EvidenceResult{
			{Name: "errors-by-workload", Samples: []prom.Sample{
				{Labels: map[string]string{"destination_workload": "a|b"}, Value: 0.2},
			}},
			{Name: "canary-request-share", Err: errors.New("scrape failed\n| injected | row |")},
		},
		ObjectEvidence: []detect.ObjectEvidenceResult{{
			Name: "env", Ref: "Deployment/payments",
			Fields: map[string]string{"spec.template.spec.containers[*].env": "MODE=`fast`\n# heading"},
		}},
		LogEvidence: []detect.LogEvidenceResult{{
			Name: "client-failure-log-sweep",
			Matches: map[string][]string{
				"loadgen": {"before", "```", "[click](https://example.com)", "after"},
			},
		}},
		RolloutEvidence: []detect.RolloutEvidenceResult{{
			Name:     "recent-rollouts",
			Rollouts: []kube.Rollout{{Deployment: "loadgen", AgeSeconds: 5, Diff: "- a\n+ ````b"}},
		}},
	}
	out := Markdown(inc, "kind: DestinationRule\n")

	if strings.Contains(out, "for  since") || !strings.Contains(out, "(threshold > 0.05) since") {
		t.Errorf("headline with an empty signal.for:\n%s", out)
	}

	// Every evidence row has exactly the two columns of the header: three unescaped pipes.
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| query") || strings.HasPrefix(line, "| ---") {
			continue
		}
		rows++
		if n := strings.Count(strings.ReplaceAll(line, `\|`, ""), "|"); n != 3 {
			t.Errorf("table row has %d column separators, want 3: %q", n, line)
		}
	}
	if rows != 2 {
		t.Errorf("got %d evidence rows, want 2 (an error text must not add rows)\n%s", rows, out)
	}

	// The log block opens with a fence longer than the ``` inside it, and the same fence closes
	// it, so the link line stays inside the block.
	if !strings.Contains(out, "````\nbefore\n```\n[click](https://example.com)\nafter\n````\n") {
		t.Errorf("log lines are not inside a longer fence:\n%s", out)
	}
	if !strings.Contains(out, "`````diff\n- a\n+ ````b\n`````\n") {
		t.Errorf("rollout diff is not inside a longer fence:\n%s", out)
	}

	// A field value with backticks and a line break stays one code span on one list line.
	if !strings.Contains(out, "``MODE=`fast` # heading``") {
		t.Errorf("field value is not one inline code span:\n%s", out)
	}
	if strings.Contains(out, "\n# heading") {
		t.Errorf("a line break in a field value started a new markdown line:\n%s", out)
	}
}

func TestCodeSpanDelimiters(t *testing.T) {
	for in, want := range map[string]string{
		"plain":    "`plain`",
		"a`b":      "``a`b``",
		"`edge`":   "`` `edge` ``",
		"x``y":     "```x``y```",
		"one\ntwo": "`one two`",
	} {
		if got := code(in); got != want {
			t.Errorf("code(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cell("a|b\nc"); got != `a\|b c` {
		t.Errorf("cell = %q", got)
	}
	if got := fence("no ticks"); got != "```" {
		t.Errorf("fence without backticks = %q", got)
	}
}
