package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Replacement is one entry of the replacements data file: a chart of the
// sample whose W0 was not 3 of 3 in a run and again in the rerun of its W0
// job, replaced by the next reserve in both runs (docs/compat-experiment.md
// section 4, INTERPRETATIONS.md 22). crossrun derives the replacements itself
// from the reports; the file must say exactly what the rule says.
type Replacement struct {
	Chart   string `json:"chart"`
	Reserve string `json:"reserve"`
	Reason  string `json:"reason"`
	// Run is the sample run (run 1 or run 2) in which the chart's W0 was not
	// 3 of 3; Rerun is the separately dispatched observe run, restricted to
	// that chart, in which it was not 3 of 3 again.
	Run   string `json:"run"`
	Rerun string `json:"rerun"`
}

// RerunRef names the rerun of one chart's W0 job for one sample run: a chart
// whose W0 was not 3 of 3 in Run, rerun once in the observe run Rerun.
type RerunRef struct {
	Chart string `json:"chart"`
	Run   string `json:"run"`
	Rerun string `json:"rerun"`
}

var (
	chartIDRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,51}[a-z0-9])?$`)
	runIDRe   = regexp.MustCompile(`^[0-9]{1,20}$`)
)

// LoadReplacements reads the replacements file, shape
// {"schema": 1, "reruns": [{"chart", "run", "rerun"}],
// "replacements": [{"chart", "reserve", "reason", "run", "rerun"}]}, unknown
// fields rejected. `reruns` is optional and lists the reruns that were not
// followed by a replacement. The checks that need the frame and the reports
// are made in buildSample.
func LoadReplacements(path string) ([]Replacement, []RerunRef, error) {
	var doc struct {
		Schema       int           `json:"schema"`
		Reruns       []RerunRef    `json:"reruns"`
		Replacements []Replacement `json:"replacements"`
	}
	if err := readJSON(path, &doc, true); err != nil {
		return nil, nil, err
	}
	if doc.Schema != 1 {
		return nil, nil, fmt.Errorf("%s: schema %d, want 1", path, doc.Schema)
	}
	seenChart, seenReserve := map[string]bool{}, map[string]bool{}
	seenRerun := map[string]bool{}
	for i, r := range doc.Reruns {
		where := fmt.Sprintf("%s: reruns[%d]", path, i)
		switch {
		case !chartIDRe.MatchString(r.Chart):
			return nil, nil, fmt.Errorf("%s: chart %q is not a chart id", where, r.Chart)
		case !runIDRe.MatchString(r.Run) || !runIDRe.MatchString(r.Rerun):
			return nil, nil, fmt.Errorf("%s: run %q and rerun %q must be workflow run ids (digits only)", where, r.Run, r.Rerun)
		case r.Run == r.Rerun:
			return nil, nil, fmt.Errorf("%s: the rerun is the run itself", where)
		case seenRerun[r.Chart+"|"+r.Run]:
			return nil, nil, fmt.Errorf("%s: chart %q has two reruns for run %s (one rerun only)", where, r.Chart, r.Run)
		}
		seenRerun[r.Chart+"|"+r.Run] = true
	}
	for i, r := range doc.Replacements {
		where := fmt.Sprintf("%s: replacements[%d]", path, i)
		switch {
		case !chartIDRe.MatchString(r.Chart):
			return nil, nil, fmt.Errorf("%s: chart %q is not a chart id", where, r.Chart)
		case !chartIDRe.MatchString(r.Reserve):
			return nil, nil, fmt.Errorf("%s: reserve %q is not a chart id", where, r.Reserve)
		case strings.TrimSpace(r.Reason) == "":
			return nil, nil, fmt.Errorf("%s: reason is empty", where)
		case !runIDRe.MatchString(r.Run):
			return nil, nil, fmt.Errorf("%s: run %q is not a workflow run id (digits only)", where, r.Run)
		case !runIDRe.MatchString(r.Rerun):
			return nil, nil, fmt.Errorf("%s: rerun %q is not a workflow run id (digits only): a chart is replaced only after its W0 job was rerun and failed again, in a separately dispatched observe run restricted to that chart", where, r.Rerun)
		case r.Run == r.Rerun:
			return nil, nil, fmt.Errorf("%s: the rerun is the run itself", where)
		case seenChart[r.Chart]:
			return nil, nil, fmt.Errorf("%s: chart %q replaced twice", where, r.Chart)
		case seenReserve[r.Reserve]:
			return nil, nil, fmt.Errorf("%s: reserve %q used twice", where, r.Reserve)
		case seenRerun[r.Chart+"|"+r.Run]:
			return nil, nil, fmt.Errorf("%s: chart %q has two reruns for run %s (one rerun only)", where, r.Chart, r.Run)
		}
		seenChart[r.Chart], seenReserve[r.Reserve] = true, true
		seenRerun[r.Chart+"|"+r.Run] = true
	}
	return doc.Replacements, doc.Reruns, nil
}

// requiredWorlds are the worlds a chart of the sample must be present with in
// every run, besides W0, the oracle.
var requiredWorlds = []string{"W1", "W3"}

// Sample is the chart set the analysis runs over: the frame's primary charts
// with the replacements applied.
type Sample struct {
	Active   []string
	Replaced map[string]bool
	// Reruns lists the reruns read: those whose records stand in for the run's
	// chart wholesale, and those after which the chart is replaced.
	Reruns []RerunUse
	// Pending lists the reruns a chart still needs in a run-1-only analysis:
	// the condition holds in the run and no rerun is listed yet. In a final
	// analysis a missing rerun is an error, so this stays empty there.
	Pending []PendingRerun
	// Orig are the runs as given, before any rerun was applied: the undecided
	// share "before the reruns" is read from them.
	Orig []RunInput
}

// PendingRerun is a rerun a chart needs and does not have yet.
type PendingRerun struct {
	Chart string `json:"chart"`
	Run   string `json:"run"`
	Why   string `json:"why"`
}

// RerunUse is one rerun the analysis read: Failed is true when the rerun's W0
// was not 3 of 3 either (the chart is then replaced), false when its records
// replace the run's for that chart.
type RerunUse struct {
	Chart  string `json:"chart"`
	Run    string `json:"run"`
	Rerun  string `json:"rerun"`
	Failed bool   `json:"failedAgain"`
	// Why is why the chart was rerun in the run (W0 not 3 of 3, undecided
	// chart-worlds, or both); After says what the rerun showed.
	Why   string `json:"why"`
	After string `json:"after,omitempty"`
}

func (s *Sample) isActive(id string) bool {
	for _, a := range s.Active {
		if a == id {
			return true
		}
	}
	return false
}

// w0Met is the replacement rule's test, "W0 is 3 of 3": in every world entry
// of the chart's report the oracle had the required number of usable runs (not
// left out as pull failures or capacity runs), and difftable's oracleInstall
// says every one of them installed (helm:install ok), which is what
// qualification demanded. The string says why not.
func w0Met(ch *ChartReport, required int) (bool, string) {
	if len(ch.Worlds) == 0 {
		return false, "the report holds no world"
	}
	for _, w := range ch.Worlds {
		if len(w.OracleRunsUsed) < required {
			why := fmt.Sprintf("%s: %d of %d W0 runs usable", w.World, len(w.OracleRunsUsed), required)
			var lo []string
			for _, l := range w.OracleRunsLeftOut {
				lo = append(lo, fmt.Sprintf("run %d %s", l.Run, l.Reason))
			}
			if len(lo) > 0 {
				why += " (left out: " + strings.Join(lo, ", ") + ")"
			}
			return false, why
		}
	}
	if oi := ch.OracleInstall; oi.Status != "ok" || oi.Ok < required {
		return false, fmt.Sprintf("W0 installs %d of %d ok (oracleInstall %q)", oi.Ok, oi.Runs, oi.Status)
	}
	return true, ""
}

// undecidedRequired lists the required mesh worlds (W1, W3) of the chart that
// the report leaves undecided.
func undecidedRequired(ch *ChartReport) []string {
	var out []string
	for _, w := range requiredWorlds {
		for k := range ch.Worlds {
			if ch.Worlds[k].World == w && ch.Worlds[k].Verdict == "undecided" {
				out = append(out, w)
			}
		}
	}
	return out
}

// rerunCondition is the one condition on which a chart is rerun in a run: the
// run leaves at least one of the chart's chart-worlds (W1, W3) undecided, or
// its W0 is not 3 of 3. why says which; it is empty when the chart is not
// rerun.
func rerunCondition(ch *ChartReport, required int) (w0 bool, w0why string, und []string, why string) {
	w0, w0why = w0Met(ch, required)
	und = undecidedRequired(ch)
	var parts []string
	if !w0 {
		parts = append(parts, "W0 not 3 of 3 ("+w0why+")")
	}
	if len(und) > 0 {
		parts = append(parts, strings.Join(und, " and ")+" undecided")
	}
	return w0, w0why, und, strings.Join(parts, "; ")
}

// buildSample applies the rerun rule and the replacement rule, which have no
// choice in them (INTERPRETATIONS.md 22 and 27).
//
// For every chart of the sample (the frame's primary charts, and a reserve once
// it is taken) and each sample run, the chart is rerun in that run if and only
// if the run leaves at least one of its chart-worlds undecided or its W0 not
// 3 of 3. The rerun is a separately dispatched observe run restricted to the
// chart (all three worlds), named in the replacements file. When both runs are
// given, crossrun errors if a chart that meets the condition has no rerun
// listed and if a rerun is listed for a chart that does not; with run 1 only a
// missing rerun is pending. The rerun's report and records replace the chart's
// in the run wholesale (never a world at a time), so every comparison stays
// within one run, and everything downstream (cells, the undecided share) reads
// the chart after its rerun.
//
// The chart is replaced if and only if its W0 is not 3 of 3 in a run and again
// in the rerun of that run; reserves are taken strictly in list order, the
// k-th replaced chart (frame order) getting the k-th reserve. The returned runs
// are the effective ones: a copy of in.Runs with the rerun records standing in.
func buildSample(in *Inputs) (*Sample, []RunInput, error) {
	isReserve := map[string]bool{}
	for _, r := range in.Reserves {
		isReserve[r] = true
	}
	final := len(in.Runs) == 2
	s := &Sample{Replaced: map[string]bool{}}
	active := map[string]bool{}
	for _, id := range in.FrameOrder {
		if !isReserve[id] {
			s.Active = append(s.Active, id)
			active[id] = true
		}
	}
	s.Orig = append([]RunInput{}, in.Runs...)
	runs := make([]RunInput, len(in.Runs))
	copy(runs, in.Runs)
	for i := range runs {
		rep := *runs[i].Report
		rep.Charts = append([]ChartReport{}, rep.Charts...)
		runs[i].Report = &rep
	}
	rerunRun := map[string]*RunInput{}
	for i := range in.Reruns {
		rerunRun[in.Reruns[i].ID] = &in.Reruns[i]
	}
	refs := map[string]string{} // chart|run -> rerun id
	for _, r := range in.RerunRefs {
		k := r.Chart + "|" + r.Run
		if prev, dup := refs[k]; dup {
			return nil, nil, fmt.Errorf("chart %q has two reruns for run %s, %s and %s: one rerun only", r.Chart, r.Run, prev, r.Rerun)
		}
		refs[k] = r.Rerun
	}
	byChart := map[string]*Replacement{}
	for i := range in.Replacements {
		r := &in.Replacements[i]
		byChart[r.Chart] = r
		if prev, dup := refs[r.Chart+"|"+r.Run]; dup {
			return nil, nil, fmt.Errorf("replacement %s: run %s has two reruns, %s and %s: one rerun only", r.Chart, r.Run, prev, r.Rerun)
		}
		refs[r.Chart+"|"+r.Run] = r.Rerun
	}
	usedRef := map[string]bool{}
	type usedRerun struct {
		chart string
		run   int
		rr    *RunInput
	}
	var used []usedRerun
	taken := 0
	visited := map[string]bool{}
	for _, id := range in.FrameOrder {
		if !active[id] {
			continue
		}
		visited[id] = true
		type verdict struct {
			cond     bool // the chart is rerun in this run
			why      string
			w0       bool
			w0why    string
			und      []string
			named    bool
			rerun    *RunInput
			rerunW0  bool
			rerunWhy string
			after    string
		}
		vs := make([]verdict, len(runs))
		doomed := false
		var evidence *verdict
		var evidenceRun string
		for i := range runs {
			ch := findBase(runs[i].Report, id)
			if ch == nil {
				continue // a missing chart is the completeness check's error
			}
			w0, w0why, und, why := rerunCondition(ch, runs[i].Report.RunsRequired)
			vs[i].w0, vs[i].w0why, vs[i].und, vs[i].why, vs[i].cond = w0, w0why, und, why, why != ""
			rid, named := refs[id+"|"+runs[i].ID]
			if !vs[i].cond {
				if named {
					return nil, nil, fmt.Errorf("chart %q: run %s names rerun %s, but run %s leaves no chart-world of %q undecided and its W0 is 3 of 3; a chart is rerun if and only if its run leaves a chart-world undecided or its W0 not 3 of 3 (no choice of which data to use)", id, runs[i].ID, rid, runs[i].ID, id)
				}
				continue
			}
			vs[i].named = named
			if !named {
				continue
			}
			usedRef[id+"|"+runs[i].ID] = true
			rr := rerunRun[rid]
			if rr == nil {
				return nil, nil, fmt.Errorf("chart %q: the rerun %s of run %s is not a rerun of the manifest", id, rid, runs[i].ID)
			}
			if err := checkRerun(rr, &runs[i], id); err != nil {
				return nil, nil, err
			}
			vs[i].rerun = rr
			rch := findBase(rr.Report, id)
			var rund []string
			vs[i].rerunW0, vs[i].rerunWhy, rund, _ = rerunCondition(rch, rr.Report.RunsRequired)
			switch {
			case !vs[i].rerunW0:
				vs[i].after = "W0 not 3 of 3 (" + vs[i].rerunWhy + ")"
			case len(rund) > 0:
				vs[i].after = strings.Join(rund, " and ") + " undecided again"
			default:
				vs[i].after = "decided"
			}
			if !vs[i].w0 && !vs[i].rerunW0 && !doomed {
				doomed = true
				evidence, evidenceRun = &vs[i], runs[i].ID
			}
		}
		for i := range runs {
			if !vs[i].cond || vs[i].named || doomed {
				continue
			}
			if !final {
				s.Pending = append(s.Pending, PendingRerun{Chart: id, Run: runs[i].ID, Why: vs[i].why})
				continue
			}
			what := fmt.Sprintf("run %s leaves %s undecided", runs[i].ID, strings.Join(vs[i].und, " and "))
			if !vs[i].w0 {
				what = fmt.Sprintf("W0 was not 3 of 3 in run %s (%s)", runs[i].ID, vs[i].w0why)
				if len(vs[i].und) > 0 {
					what += fmt.Sprintf(" and it leaves %s undecided", strings.Join(vs[i].und, " and "))
				}
			}
			return nil, nil, fmt.Errorf("chart %q: %s, and no rerun of the chart is named in the replacements file. "+
				"Dispatch an observe run restricted to %q (chart_ids) after run %s ended, and name it as a rerun of run %s: a chart is rerun once in a run that leaves a chart-world undecided or its W0 not 3 of 3 (reruns, or replacements when W0 fails again)",
				id, what, id, runs[i].ID, runs[i].ID)
		}
		rep := byChart[id]
		if doomed {
			if rep == nil {
				return nil, nil, fmt.Errorf("chart %q: W0 was not 3 of 3 in run %s (%s) and again in its rerun %s (%s), so the rule replaces it with the next reserve; no such replacement is in the replacements file",
					id, evidenceRun, evidence.why, evidence.rerun.ID, evidence.rerunWhy)
			}
			where := fmt.Sprintf("replacement %s -> %s", rep.Chart, rep.Reserve)
			if taken >= len(in.Reserves) {
				return nil, nil, fmt.Errorf("%s: the frame has no reserve left", where)
			}
			if want := in.Reserves[taken]; rep.Reserve != want {
				return nil, nil, fmt.Errorf("%s: reserves are taken in list order, the next reserve is %q", where, want)
			}
			if rep.Rerun != evidence.rerun.ID || rep.Run != evidenceRun {
				return nil, nil, fmt.Errorf("%s: the file names run %s and rerun %s, the evidence is run %s and rerun %s", where, rep.Run, rep.Rerun, evidenceRun, evidence.rerun.ID)
			}
			taken++
			for k, a := range s.Active {
				if a == id {
					s.Active[k] = rep.Reserve
				}
			}
			active[rep.Reserve] = true
			s.Replaced[id] = true
			for i := range vs {
				if vs[i].rerun != nil {
					s.Reruns = append(s.Reruns, RerunUse{Chart: id, Run: runs[i].ID, Rerun: vs[i].rerun.ID, Failed: !vs[i].rerunW0, Why: vs[i].why, After: vs[i].after})
					used = append(used, usedRerun{id, i, vs[i].rerun})
				}
			}
			continue
		}
		if rep != nil {
			return nil, nil, fmt.Errorf("replacement %s -> %s: the chart's W0 is 3 of 3 (in every run, or on its rerun), so the rule does not replace it", rep.Chart, rep.Reserve)
		}
		// not replaced: the rerun stands in for the run's chart, wholesale
		for i := range vs {
			if vs[i].rerun == nil {
				continue
			}
			s.Reruns = append(s.Reruns, RerunUse{Chart: id, Run: runs[i].ID, Rerun: vs[i].rerun.ID, Failed: false, Why: vs[i].why, After: vs[i].after})
			used = append(used, usedRerun{id, i, vs[i].rerun})
			for k := range runs[i].Report.Charts {
				if runs[i].Report.Charts[k].Chart == id && runs[i].Report.Charts[k].FixOf == "" {
					runs[i].Report.Charts[k] = *findBase(vs[i].rerun.Report, id)
				}
			}
			if runs[i].HasRecords {
				var keep []Record
				for _, r := range runs[i].Records {
					if r.Chart != id {
						keep = append(keep, r)
					}
				}
				if vs[i].rerun.HasRecords {
					for _, r := range vs[i].rerun.Records {
						if r.Chart == id {
							keep = append(keep, r)
						}
					}
				}
				runs[i].Records = keep
			}
		}
	}
	for _, r := range in.Replacements {
		if !visited[r.Chart] {
			return nil, nil, fmt.Errorf("replacement %s -> %s: %q is not a chart of the sample at that point (a primary chart, or a reserve taken earlier)", r.Chart, r.Reserve, r.Chart)
		}
	}
	for key, rid := range refs {
		if !usedRef[key] {
			chart, run, _ := strings.Cut(key, "|")
			given := false
			for _, r := range runs {
				if r.ID == run {
					given = true
				}
			}
			if !given {
				return nil, nil, fmt.Errorf("rerun %s of %s in run %s is named, but run %s is not given to this analysis", rid, chart, run, run)
			}
			return nil, nil, fmt.Errorf("rerun %s of %s in run %s is named but the rule needs none: that run leaves no chart-world of the chart undecided and its W0 is 3 of 3 (or the chart is not in the sample)", rid, chart, run)
		}
	}
	for id := range rerunRun {
		named := false
		for _, rid := range refs {
			if rid == id {
				named = true
			}
		}
		if !named {
			return nil, nil, fmt.Errorf("rerun run %s is in the manifest but the replacements file names it for no chart", id)
		}
	}
	for _, u := range used {
		if err := checkRerunTiming(u.rr, u.run, runs, u.chart); err != nil {
			return nil, nil, err
		}
	}
	return s, runs, nil
}

// checkRerunTiming is the literal reading of rule 1 for a rerun (INTERPRETATIONS.md
// 27, a definition beyond the document): the 12 h rule keeps using the original
// runs' times, so a cell is still "two workflow runs at least 12 h apart" only if
// the rerun of a run-1 chart is done before run 2 starts, and the rerun of a
// run-2 chart starts at least 12 h after run 1 ended. Anything else is an error.
// It applies once both runs are given.
func checkRerunTiming(rr *RunInput, run int, runs []RunInput, chart string) error {
	if len(runs) != 2 {
		return nil
	}
	switch run {
	case 0:
		if !rr.Ended.Before(runs[1].Started) {
			return fmt.Errorf("chart %q: its rerun %s of run %s ended at %s, not before run 2 (%s) started at %s; the rerun of a run-1 chart must be done before run 2 starts, or the cell's two observations are no longer two workflow runs at least %d h apart (rule 1; INTERPRETATIONS.md 27)",
				chart, rr.ID, runs[0].ID, rr.Ended.Format(time.RFC3339), runs[1].ID, runs[1].Started.Format(time.RFC3339), GapHours)
		}
	case 1:
		if gap := rr.Started.Sub(runs[0].Ended); gap < GapHours*time.Hour {
			return fmt.Errorf("chart %q: its rerun %s of run %s started at %s, only %.2f h after run 1 (%s) ended at %s; the rerun of a run-2 chart must start at least %d h after run 1 ended (rule 1; INTERPRETATIONS.md 27)",
				chart, rr.ID, runs[1].ID, rr.Started.Format(time.RFC3339), gap.Hours(), runs[0].ID, runs[0].Ended.Format(time.RFC3339), GapHours)
		}
	}
	return nil
}

// findBase is findChart for a base chart (not a fix entry).
func findBase(rep *Report, id string) *ChartReport {
	for i := range rep.Charts {
		if rep.Charts[i].Chart == id && rep.Charts[i].FixOf == "" {
			return &rep.Charts[i]
		}
	}
	return nil
}

// checkRerun: the rerun is a completed observe run of the same sample after
// the run it re-runs, restricted to the one chart (no other chart, no fix
// entry), with the same number of required runs.
func checkRerun(rr, run *RunInput, chart string) error {
	if rr.Ended.Before(rr.Started) {
		return fmt.Errorf("rerun %s: ended before it started", rr.ID)
	}
	if rr.Started.Before(run.Ended) {
		return fmt.Errorf("rerun %s of run %s started at %s, before run %s ended at %s: it is not a rerun", rr.ID, run.ID, rr.Started.Format(time.RFC3339), run.ID, run.Ended.Format(time.RFC3339))
	}
	if rr.Report.RunsRequired != run.Report.RunsRequired {
		return fmt.Errorf("rerun %s requires %d runs per world, run %s requires %d", rr.ID, rr.Report.RunsRequired, run.ID, run.Report.RunsRequired)
	}
	for _, c := range rr.Report.Charts {
		if c.Chart != chart || c.FixOf != "" {
			return fmt.Errorf("rerun %s of %q holds %q: a rerun is restricted to the one chart (chart_ids), no other chart, no fix entry", rr.ID, chart, c.Chart)
		}
	}
	if findBase(rr.Report, chart) == nil {
		return fmt.Errorf("rerun %s holds no report for %q", rr.ID, chart)
	}
	return nil
}

// checkComplete is the completeness rule: every chart of the sample is in
// every run's report with W0 records and with W1 and W3, and the report holds
// no chart outside the sample (replaced charts are ignored). Whether a W0 that
// was not 3 of 3 is acceptable is the replacement rule's business (buildSample).
func checkComplete(s *Sample, runs []RunInput) error {
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
				}
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the reports do not cover the frozen sample:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}
