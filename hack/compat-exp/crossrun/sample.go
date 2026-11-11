package main

import (
	"fmt"
	"regexp"
	"strings"
)

// Replacement is one entry of the replacements data file: a chart of the
// frozen sample whose W0 failed later, replaced by the next reserve in both
// runs (docs/compat-experiment.md section 4).
type Replacement struct {
	Chart   string `json:"chart"`
	Reserve string `json:"reserve"`
	Reason  string `json:"reason"`
	// Run is the workflow run id whose report is the evidence that the chart's
	// W0 was not 3 of 3.
	Run string `json:"run"`
}

var (
	chartIDRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,51}[a-z0-9])?$`)
	runIDRe   = regexp.MustCompile(`^[0-9]{1,20}$`)
)

// LoadReplacements reads the replacements file, shape
// {"schema": 1, "replacements": [{"chart", "reserve", "reason", "run"}]},
// unknown fields rejected. The checks that need the frame and the reports are
// made in Analyze.
func LoadReplacements(path string) ([]Replacement, error) {
	var doc struct {
		Schema       int           `json:"schema"`
		Replacements []Replacement `json:"replacements"`
	}
	if err := readJSON(path, &doc, true); err != nil {
		return nil, err
	}
	if doc.Schema != 1 {
		return nil, fmt.Errorf("%s: schema %d, want 1", path, doc.Schema)
	}
	seenChart, seenReserve := map[string]bool{}, map[string]bool{}
	for i, r := range doc.Replacements {
		where := fmt.Sprintf("%s: replacements[%d]", path, i)
		switch {
		case !chartIDRe.MatchString(r.Chart):
			return nil, fmt.Errorf("%s: chart %q is not a chart id", where, r.Chart)
		case !chartIDRe.MatchString(r.Reserve):
			return nil, fmt.Errorf("%s: reserve %q is not a chart id", where, r.Reserve)
		case strings.TrimSpace(r.Reason) == "":
			return nil, fmt.Errorf("%s: reason is empty", where)
		case !runIDRe.MatchString(r.Run):
			return nil, fmt.Errorf("%s: run %q is not a workflow run id (digits only)", where, r.Run)
		case seenChart[r.Chart]:
			return nil, fmt.Errorf("%s: chart %q replaced twice", where, r.Chart)
		case seenReserve[r.Reserve]:
			return nil, fmt.Errorf("%s: reserve %q used twice", where, r.Reserve)
		}
		seenChart[r.Chart], seenReserve[r.Reserve] = true, true
	}
	return doc.Replacements, nil
}

// requiredWorlds are the worlds a chart of the sample must be present with in
// every run, besides W0, the oracle.
var requiredWorlds = []string{"W1", "W3"}

// Sample is the chart set the analysis runs over: the frame's primary charts
// with the replacements applied.
type Sample struct {
	Active   []string
	Replaced map[string]bool
}

func (s *Sample) isActive(id string) bool {
	for _, a := range s.Active {
		if a == id {
			return true
		}
	}
	return false
}

// buildSample applies the replacements to the frame's primary charts and
// checks each one: the replaced chart is in the sample at that point, the
// reserve is the next unused reserve in list order, and the named run is a run
// of the manifest whose report shows the replaced chart's W0 not 3 of 3.
func buildSample(in *Inputs) (*Sample, error) {
	isReserve := map[string]bool{}
	for _, r := range in.Reserves {
		isReserve[r] = true
	}
	s := &Sample{Replaced: map[string]bool{}}
	for _, id := range in.FrameOrder {
		if !isReserve[id] {
			s.Active = append(s.Active, id)
		}
	}
	next := 0
	for i, r := range in.Replacements {
		where := fmt.Sprintf("replacement %d (%s -> %s)", i+1, r.Chart, r.Reserve)
		if !s.isActive(r.Chart) {
			return nil, fmt.Errorf("%s: %q is not a chart of the sample at that point (a primary chart, or a reserve taken earlier)", where, r.Chart)
		}
		if next >= len(in.Reserves) {
			return nil, fmt.Errorf("%s: the frame has no reserve left", where)
		}
		if want := in.Reserves[next]; r.Reserve != want {
			return nil, fmt.Errorf("%s: reserves are taken in list order, the next reserve is %q", where, want)
		}
		next++
		var ev *RunInput
		for j := range in.Runs {
			if in.Runs[j].ID == r.Run {
				ev = &in.Runs[j]
			}
		}
		if ev == nil {
			return nil, fmt.Errorf("%s: evidence run %s is not a run of the manifest", where, r.Run)
		}
		ch := findChart(ev.Report, r.Chart)
		if ch == nil || ch.FixOf != "" {
			return nil, fmt.Errorf("%s: run %s has no report for %q, so its W0 failure cannot be verified", where, r.Run, r.Chart)
		}
		failed := false
		for _, w := range ch.Worlds {
			if len(w.OracleRunsUsed) < ev.Report.RunsRequired {
				failed = true
			}
		}
		if !failed {
			return nil, fmt.Errorf("%s: in run %s the W0 of %q was %d of %d in every world: it did not fail, so it is not replaced",
				where, r.Run, r.Chart, ev.Report.RunsRequired, ev.Report.RunsRequired)
		}
		for k, a := range s.Active {
			if a == r.Chart {
				s.Active[k] = r.Reserve
			}
		}
		s.Replaced[r.Chart] = true
	}
	return s, nil
}

// checkComplete is the completeness rule: every chart of the sample is in
// every run's report with W0 records and with W1 and W3, and the report holds
// no chart outside the sample (replaced charts are ignored).
func checkComplete(s *Sample, runs []RunInput) (warnings []string, err error) {
	var problems []string
	for _, run := range runs {
		have := map[string]*ChartReport{}
		for i := range run.Report.Charts {
			ch := &run.Report.Charts[i]
			if ch.FixOf != "" {
				continue
			}
			have[ch.Chart] = ch
			if !s.isActive(ch.Chart) && !s.Replaced[ch.Chart] {
				problems = append(problems, fmt.Sprintf("run %s: chart %q is in the report but not in the sample (not a primary chart, and not a reserve taken by a replacement)", run.ID, ch.Chart))
			}
		}
		for _, id := range s.Active {
			ch := have[id]
			if ch == nil {
				problems = append(problems, fmt.Sprintf("run %s: chart %q is missing (it needs W0, W1 and W3; a chart whose W0 failed is replaced by the next reserve in both runs, in the replacements file)", run.ID, id))
				continue
			}
			for _, w := range requiredWorlds {
				var wr *WorldReport
				for k := range ch.Worlds {
					if ch.Worlds[k].World == w {
						wr = &ch.Worlds[k]
					}
				}
				switch {
				case wr == nil:
					problems = append(problems, fmt.Sprintf("run %s: chart %q has no world %s", run.ID, id, w))
				case len(wr.OracleRunsUsed)+len(wr.OracleRunsLeftOut) == 0:
					problems = append(problems, fmt.Sprintf("run %s: chart %q world %s has no W0 runs", run.ID, id, w))
				case len(wr.OracleRunsUsed) < run.Report.RunsRequired:
					warnings = append(warnings, fmt.Sprintf("run %s: chart %q world %s: W0 was not %d of %d and the chart is not replaced", run.ID, id, w, run.Report.RunsRequired, run.Report.RunsRequired))
				}
			}
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("the reports do not cover the frozen sample:\n  %s", strings.Join(problems, "\n  "))
	}
	return warnings, nil
}
