package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/Git-Hospital/meshmedic/pkg/catalog"
	"github.com/Git-Hospital/meshmedic/pkg/detect"
	"github.com/Git-Hospital/meshmedic/pkg/prom"
	"github.com/Git-Hospital/meshmedic/pkg/remediate"
	"github.com/Git-Hospital/meshmedic/pkg/report"
)

// StepResult is what one cycle produced. Cycle, Incidents and Resolutions are
// the structures `watch` works with, unchanged.
type StepResult struct {
	At          time.Time
	Cycle       detect.Cycle
	Incidents   []detect.Incident
	Dossiers    []string // report.Markdown of each incident, as watch prints it
	Patches     []string // remediate.Render of each incident
	Resolutions []detect.Resolution
	Resolved    []string // report.Resolved of each resolution
	Requests    []Request
}

// Result is a whole replay.
type Result struct {
	Fixture   Fixture
	Scenarios []catalog.Scenario // the catalog entries that ran

	// Set by the caller, for the screen: the catalog directory when it is not
	// the one built into the binary, and whether --out was given.
	Catalog      string
	OutRequested bool
	Steps        []StepResult
}

// Run replays fx through the detector. scenarios is the catalog (all of it;
// the fixture names which entries to watch) and unlocked is the lock standing
// as `watch` computes it. The detector is built and ticked exactly as `watch`
// builds and ticks it; only the Prometheus URL and the cluster reader differ.
func Run(ctx context.Context, fx Fixture, scenarios []catalog.Scenario, unlocked map[string]string) (Result, error) {
	res := Result{Fixture: fx}
	byID := map[string]catalog.Scenario{}
	for _, s := range scenarios {
		byID[s.ID] = s
	}
	for _, id := range fx.Scenarios {
		s, ok := byID[id]
		if !ok {
			return res, fmt.Errorf("demo %s needs catalog entry %q, which is not in the catalog", fx.Name, id)
		}
		res.Scenarios = append(res.Scenarios, s)
	}

	rp, err := newReplay(fx, res.Scenarios)
	if err != nil {
		return res, err
	}
	url, stop, err := rp.serve()
	if err != nil {
		return res, fmt.Errorf("starting the replayed Prometheus on loopback: %w", err)
	}
	defer stop()

	var cur *StepResult
	handler := func(_ context.Context, inc detect.Incident) error {
		// The same two calls watch makes.
		patch, err := remediate.Render(inc.Scenario, inc.Params)
		if err != nil {
			cur.Dossiers = append(cur.Dossiers, report.Markdown(inc, "# patch rendering failed, see logs\n"))
			cur.Incidents = append(cur.Incidents, inc)
			cur.Patches = append(cur.Patches, "")
			return nil
		}
		cur.Incidents = append(cur.Incidents, inc)
		cur.Patches = append(cur.Patches, patch)
		cur.Dossiers = append(cur.Dossiers, report.Markdown(inc, patch))
		return nil
	}

	d := detect.New(scenarios, []detect.Target{{Params: fx.Target, Scenarios: fx.Scenarios}},
		prom.NewClient(url), handler)
	d.Unlocked = unlocked
	d.Objects = objectReader{fx.Objects}
	d.OnResolve = func(_ context.Context, r detect.Resolution) error {
		cur.Resolutions = append(cur.Resolutions, r)
		cur.Resolved = append(cur.Resolved, report.Resolved(r))
		return nil
	}

	for i, step := range fx.Steps {
		rp.setStep(i)
		at := step.At
		d.Now = func() time.Time { return at }
		before := len(rp.log())
		res.Steps = append(res.Steps, StepResult{At: at})
		cur = &res.Steps[len(res.Steps)-1]
		cur.Cycle = d.Tick(ctx, at)
		cur.Requests = rp.log()[before:]
	}
	for _, sr := range res.Steps {
		for _, r := range sr.Requests {
			if r.Role == "unexpected" {
				return res, fmt.Errorf("demo %s: the detector asked a query the fixture does not cover: %s", fx.Name, r.PromQL)
			}
		}
	}
	return res, nil
}

// objectReader answers configuration reads from the fixture. It stands where
// kube.Reader (kubectl) stands in `watch`; the detector's own field extraction
// and redaction run on what it returns.
type objectReader struct{ objects map[string]map[string]any }

func (o objectReader) Get(_ context.Context, _, kind, namespace, name string) (map[string]any, error) {
	if obj, ok := o.objects[kind+" "+namespace+"/"+name]; ok {
		return obj, nil
	}
	return nil, fmt.Errorf("%s %s/%s is not in the replay fixture", kind, namespace, name)
}
