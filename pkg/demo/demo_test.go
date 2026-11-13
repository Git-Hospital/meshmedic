package demo

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	meshmedic "github.com/Git-Hospital/meshmedic"
	"github.com/Git-Hospital/meshmedic/pkg/catalog"
	"github.com/Git-Hospital/meshmedic/pkg/detect"
	"github.com/Git-Hospital/meshmedic/pkg/remediate"
	"github.com/Git-Hospital/meshmedic/pkg/report"
)

func realCatalog(t *testing.T) []catalog.Scenario {
	t.Helper()
	scenarios, err := catalog.LoadFS(meshmedic.CatalogFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	return scenarios
}

func run(t *testing.T, name string, scenarios []catalog.Scenario, unlocked map[string]string) Result {
	t.Helper()
	fx, err := Load(name)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), fx, scenarios, unlocked)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func screen(res Result, verbose bool) string {
	var b strings.Builder
	Render(&b, res, verbose, nil)
	return b.String()
}

// The demo's dossier is the structure watch hands to report.Markdown, built by
// the detector from the catalog entry and the Prometheus answers, not text
// that lives in the fixture.
func TestFiringGoesThroughTheWatchPath(t *testing.T) {
	res := run(t, "canary-latency-rollback", realCatalog(t), nil)

	var incs []detect.Incident
	for _, sr := range res.Steps {
		incs = append(incs, sr.Incidents...)
	}
	if len(incs) != 1 {
		t.Fatalf("want exactly one incident across the replay, got %d", len(incs))
	}
	inc := incs[0]
	if inc.Scenario.ID != "canary-latency-rollback" || inc.Value != 2485 || inc.Threshold != 1000 {
		t.Fatalf("incident = %s value %v threshold %v", inc.Scenario.ID, inc.Value, inc.Threshold)
	}
	if want := time.Date(2026, 8, 19, 12, 48, 49, 0, time.UTC); !inc.Since.Equal(want) {
		t.Fatalf("Since = %v, want %v", inc.Since, want)
	}
	if len(inc.Evidence) != len(inc.Scenario.Evidence) {
		t.Fatalf("evidence results = %d, catalog declares %d", len(inc.Evidence), len(inc.Scenario.Evidence))
	}
	for _, e := range inc.Evidence {
		if e.Err != nil || len(e.Samples) != 1 {
			t.Fatalf("evidence %s: %v %v", e.Name, e.Samples, e.Err)
		}
		if !strings.Contains(e.PromQL, `destination_service_name="payments"`) {
			t.Fatalf("evidence %s PromQL was not rendered from the catalog template: %s", e.Name, e.PromQL)
		}
	}
	if len(inc.ObjectEvidence) != 1 || len(inc.ObjectEvidence[0].Fields) != 2 || inc.ObjectEvidence[0].Err != nil {
		t.Fatalf("object evidence = %+v", inc.ObjectEvidence)
	}

	// What the demo printed is exactly what the shared report code renders for
	// that incident and the shared patch renderer's output.
	patch, err := remediate.Render(inc.Scenario, inc.Params)
	if err != nil {
		t.Fatal(err)
	}
	want := report.Markdown(inc, patch)
	var got string
	for _, sr := range res.Steps {
		for _, d := range sr.Dossiers {
			got = d
		}
	}
	if got != want {
		t.Fatalf("dossier differs from report.Markdown(incident, remediate.Render)")
	}
	for _, need := range []string{"### Proposed patch (VirtualService)", "weight: 0", "### Rollback", "nothing was applied to the cluster"} {
		if !strings.Contains(want, need) {
			t.Errorf("dossier lacks %q", need)
		}
	}
	if !strings.Contains(screen(res, false), want) {
		t.Fatal("the printed screen does not contain the dossier")
	}
}

func TestFiringThenResolving(t *testing.T) {
	res := run(t, "canary-latency-rollback", realCatalog(t), nil)
	var fired, resolved []int
	for i, sr := range res.Steps {
		if len(sr.Incidents) > 0 {
			fired = append(fired, i)
		}
		if len(sr.Resolutions) > 0 {
			resolved = append(resolved, i)
		}
	}
	// The 90s hold is not met at 0s or 45s; it is met at 90s; the fifth cycle clears.
	if len(fired) != 1 || fired[0] != 2 || len(resolved) != 1 || resolved[0] != 4 {
		t.Fatalf("fired at %v, resolved at %v", fired, resolved)
	}
	if res.Steps[0].Cycle.Waiting != 1 || res.Steps[1].Cycle.Waiting != 1 {
		t.Fatalf("cycles 1 and 2 should be awaiting the hold: %q %q", res.Steps[0].Cycle.Line(), res.Steps[1].Cycle.Line())
	}
	if res.Steps[3].Cycle.Reported != 1 || len(res.Steps[3].Incidents) != 0 {
		t.Fatalf("cycle 4 must not report a second time: %q", res.Steps[3].Cycle.Line())
	}
	r := res.Steps[4].Resolutions[0]
	if r.Duration() != 3*time.Minute || r.Value != 312 {
		t.Fatalf("resolution = %v after %v", r.Value, r.Duration())
	}
	if res.Steps[4].Resolved[0] != report.Resolved(r) {
		t.Fatal("resolution text is not report.Resolved")
	}
}

func TestErrorSurgeScenario(t *testing.T) {
	res := run(t, "error-surge-outlier-ejection", realCatalog(t), nil)
	if len(res.Steps[2].Incidents) != 1 || len(res.Steps[4].Resolutions) != 1 {
		t.Fatal("error-surge should fire on cycle 3 and resolve on cycle 5")
	}
	d := res.Steps[2].Dossiers[0]
	for _, need := range []string{"payments-v2", "0.6667", "kind: DestinationRule", "ERROR_RATE=0.9"} {
		if !strings.Contains(d, need) {
			t.Errorf("dossier lacks %q", need)
		}
	}
}

// Blind is the detector's own state. The first cycle must not be a clear, and
// the second, with telemetry back, is.
func TestBlindIsNeverClear(t *testing.T) {
	res := run(t, "blind-no-telemetry", realCatalog(t), nil)
	c1, c2 := res.Steps[0].Cycle, res.Steps[1].Cycle
	if c1.Unobserved != 1 || c1.Blind != 1 || c1.Clear != 0 || c1.Healthy() {
		t.Fatalf("cycle 1 must be blind and unhealthy: %s", c1.Line())
	}
	if ev := c1.Evaluations[0]; ev.Outcome != detect.OutcomeBlind || !strings.Contains(ev.Reason, "no mesh telemetry") {
		t.Fatalf("cycle 1 evaluation = %+v", ev)
	}
	if c2.Clear != 1 || c2.Blind != 0 || !c2.Healthy() {
		t.Fatalf("cycle 2 must be a real clear: %s", c2.Line())
	}
	for _, sr := range res.Steps {
		if len(sr.Incidents) != 0 {
			t.Fatal("a blind or clear replay must not report an incident")
		}
	}
	out := screen(res, false)
	if !strings.Contains(out, "BLIND, not clear") || !strings.Contains(out, "`meshmedic check` would exit 1") {
		t.Fatalf("screen does not show the refusal:\n%s", out)
	}
}

// The demo follows the code. Change the catalog entry and the demo changes
// with it; nothing printed is stored text.
func TestDemoFollowsTheCatalog(t *testing.T) {
	base := run(t, "canary-latency-rollback", realCatalog(t), nil)

	t.Run("threshold", func(t *testing.T) {
		scenarios := realCatalog(t)
		for i := range scenarios {
			if scenarios[i].ID == "canary-latency-rollback" {
				scenarios[i].Signal.Threshold = 3000
			}
		}
		res := run(t, "canary-latency-rollback", scenarios, nil)
		for _, sr := range res.Steps {
			if len(sr.Incidents) != 0 {
				t.Fatal("a threshold above the replayed value must not fire")
			}
		}
		if screen(res, false) == screen(base, false) {
			t.Fatal("output did not change")
		}
	})
	t.Run("text and patch", func(t *testing.T) {
		scenarios := realCatalog(t)
		for i := range scenarios {
			if scenarios[i].ID == "canary-latency-rollback" {
				scenarios[i].Description = "EDITED DIAGNOSIS"
				scenarios[i].Remediation.PatchTemplate = strings.Replace(scenarios[i].Remediation.PatchTemplate, "weight: 0", "weight: 5", 1)
			}
		}
		out := screen(run(t, "canary-latency-rollback", scenarios, nil), false)
		if !strings.Contains(out, "EDITED DIAGNOSIS") || !strings.Contains(out, "weight: 5") {
			t.Fatal("an edited catalog entry did not reach the demo output")
		}
	})
	t.Run("signal query", func(t *testing.T) {
		scenarios := realCatalog(t)
		for i := range scenarios {
			if scenarios[i].ID == "canary-latency-rollback" {
				scenarios[i].Signal.PromQL = "vector(1) # edited {{.service}}"
			}
		}
		res := run(t, "canary-latency-rollback", scenarios, nil)
		var asked bool
		for _, sr := range res.Steps {
			for _, rq := range sr.Requests {
				asked = asked || strings.Contains(rq.PromQL, "vector(1) # edited payments")
			}
		}
		if !asked {
			t.Fatal("the detector did not send the edited query")
		}
	})
}

func TestUnlockedEntryDoesNotRun(t *testing.T) {
	res := run(t, "canary-latency-rollback", realCatalog(t), map[string]string{"canary-latency-rollback": "edited after approval"})
	for _, sr := range res.Steps {
		if sr.Cycle.Unlocked != 1 || len(sr.Incidents) != 0 {
			t.Fatalf("an unlocked entry must not run: %s", sr.Cycle.Line())
		}
	}
	if !strings.Contains(screen(res, false), "UNLOCKED, does not run") {
		t.Fatal("unlocked state not shown")
	}
}

func TestVerboseShowsQueriesAndResponses(t *testing.T) {
	out := screen(run(t, "canary-latency-rollback", realCatalog(t), nil), true)
	for _, need := range []string{"query (coverage probe)", "query (signal canary-latency-rollback)", "query (evidence canary-request-share)", `"resultType":"vector"`, "cycle summary: 1 targets observed"} {
		if !strings.Contains(out, need) {
			t.Errorf("verbose output lacks %q", need)
		}
	}
	if strings.Contains(screen(run(t, "canary-latency-rollback", realCatalog(t), nil), false), "query (") {
		t.Error("queries shown without --verbose")
	}
}

func TestDeterministic(t *testing.T) {
	a := screen(run(t, "canary-latency-rollback", realCatalog(t), nil), true)
	b := screen(run(t, "canary-latency-rollback", realCatalog(t), nil), true)
	if a != b {
		t.Fatal("two runs differ")
	}
}

// The replayed Prometheus is on loopback and refuses what its fixture does not
// explain, rather than guessing an answer.
func TestReplayServerIsLoopbackAndStrict(t *testing.T) {
	fx, _ := Load("canary-latency-rollback")
	rp, err := newReplay(fx, realCatalog(t)[:0])
	if err != nil {
		t.Fatal(err)
	}
	base, stop, err := rp.serve()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("not loopback: %s", base)
	}
	resp, err := http.Get(base + "/api/v1/query?query=" + url.QueryEscape("up"))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown query got %d, want 400", resp.StatusCode)
	}
	if log := rp.log(); len(log) != 1 || log[0].Role != "unexpected" {
		t.Fatalf("unknown query not logged as unexpected: %+v", log)
	}
}

// Every fixture runs against the real catalog with no query the fixture cannot
// answer, and names a source file that exists in this repository.
func TestFixtures(t *testing.T) {
	scenarios := realCatalog(t)
	if Names()[0] != DefaultName {
		t.Fatalf("default is not first: %v", Names())
	}
	for _, n := range Names() {
		fx, err := Load(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Run(context.Background(), fx, scenarios, nil); err != nil {
			t.Errorf("%s: %v", n, err)
		}
		if len(fx.Constructed) == 0 {
			t.Errorf("%s: says nothing about what is constructed", n)
		}
		if f := strings.Fields(fx.Source)[0]; strings.HasSuffix(f, ".md") || strings.HasSuffix(f, ".txt") {
			if _, err := os.Stat("../../" + f); err != nil {
				t.Errorf("%s: source %s: %v", n, f, err)
			}
		}
	}
}
