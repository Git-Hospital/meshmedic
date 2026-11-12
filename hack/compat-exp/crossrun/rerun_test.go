package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

// Point 4 of "Open before the freeze": the undecided stop is taken "after one
// rerun". A chart is rerun in a run if and only if the run leaves one of its
// chart-worlds undecided or its W0 not 3 of 3; the rerun's records replace the
// chart's records of the run wholesale (INTERPRETATIONS.md 27).

// undecidedChart is chart id with W3 undecided and W1 fine.
func undecidedChart(id string) ChartReport {
	return chart(id, wr("W1", ok(wk)), uw("W3", ledger.ReasonUndecided))
}

func setChart(f *fixture, run int, c ChartReport) {
	for i := range f.in.Runs[run].Report.Charts {
		if f.in.Runs[run].Report.Charts[i].Chart == c.Chart {
			f.in.Runs[run].Report.Charts[i] = c
		}
	}
}

// the sample a, b, c has 6 chart-worlds: one undecided is 16.7%, over 15%.
func TestRerunOfAnUndecidedWorldDecidesIt(t *testing.T) {
	f := sampleFixture()
	setChart(f, 0, undecidedChart("a"))
	addRerun(f, "a", 0, true, nil) // a clean rerun
	res := f.analyze(t)
	u := res.Undecided
	if u.BeforeReruns.Undecided != 1 || u.BeforeReruns.Of != 6 || !u.BeforeReruns.Over {
		t.Fatalf("before the rerun: 1 of 6 undecided, over 15%%: %+v", u.BeforeReruns)
	}
	if u.ByChartWorlds.Undecided != 0 || u.ByChartWorlds.Of != 6 || u.ByChartWorlds.Over || u.Over {
		t.Fatalf("after the rerun the chart-world is decided and the share is 0 of 6: %+v", u)
	}
	if len(res.UndecidedWorld) != 0 {
		t.Fatalf("the rerun decided it, so no undecided world is left: %+v", res.UndecidedWorld)
	}
	if len(res.Sample.Reruns) != 1 || res.Sample.Reruns[0].Chart != "a" || res.Sample.Reruns[0].Failed {
		t.Fatalf("%+v", res.Sample.Reruns)
	}
	md := string(RenderMarkdown(res))
	for _, want := range []string{"before the reruns: 1 of 6", "after the reruns: 0 of 6"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestChartUndecidedAgainAfterTheRerunStaysUndecided(t *testing.T) {
	f := sampleFixture()
	setChart(f, 0, undecidedChart("a"))
	addRerun(f, "a", 0, true, func(c *ChartReport) { *c = undecidedChart("a") })
	res := f.analyze(t)
	u := res.Undecided
	if u.ByChartWorlds.Undecided != 1 || !u.ByChartWorlds.Over || !u.Over || u.BeforeReruns.Undecided != 1 {
		t.Fatalf("undecided again after the rerun it counts: %+v", u)
	}
	if len(res.UndecidedWorld) != 1 || res.UndecidedWorld[0].Chart != "a" || res.UndecidedWorld[0].World != "W3" {
		t.Fatalf("%+v", res.UndecidedWorld)
	}
	if res.Sample.Reruns[0].Failed {
		t.Fatal("W0 was fine in the run, so the chart is not replaced whatever the rerun shows")
	}
}

// The rerun is one run's whole chart: the records replace the chart's records
// of the run wholesale, never world by world.
func TestRerunReplacesTheChartsRecordsOfTheRunWholesale(t *testing.T) {
	f := sampleFixture()
	// run 1: W3 shows a finding, W1 is undecided
	setChart(f, 0, chart("a", uw("W1", ledger.ReasonUndecided), wr("W3", bad(wk))))
	setChart(f, 1, chart("a", wr("W1", ok(wk)), wr("W3", bad(wk))))
	addRerun(f, "a", 0, true, nil) // clean in both worlds
	c := cellOf(t, f.analyze(t), "a", "W3", wk)
	if c.Run1 != "same" || c.Run2 != "finding" || c.Holds {
		t.Fatalf("the rerun replaces the whole chart of run 1, so run 1's W3 finding is gone (same in run 1): %+v", c)
	}
	// a rerun that keeps the finding in W3 and decides W1 gives a cell that holds in both runs
	f = sampleFixture()
	setChart(f, 0, chart("a", uw("W1", ledger.ReasonUndecided), wr("W3", ok(wk))))
	setChart(f, 1, chart("a", wr("W1", ok(wk)), wr("W3", bad(wk))))
	addRerun(f, "a", 0, true, func(c *ChartReport) { *c = chart("a", wr("W1", ok(wk)), wr("W3", bad(wk))) })
	c = cellOf(t, f.analyze(t), "a", "W3", wk)
	if !c.Holds {
		t.Fatalf("%+v", c)
	}
}

func TestRerunForAChartThatMeetsNoConditionIsAnError(t *testing.T) {
	f := sampleFixture()
	addRerun(f, "b", 1, true, nil) // b is healthy in run 2
	msg := f.analyzeErr(t)
	for _, want := range []string{`chart "b"`, "run r2", "leaves no chart-world"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("want %q in: %s", want, msg)
		}
	}
}

func TestMissingRerunInAFinalAnalysisIsAnError(t *testing.T) {
	f := sampleFixture()
	setChart(f, 1, undecidedChart("c"))
	msg := f.analyzeErr(t)
	for _, want := range []string{`chart "c"`, "run r2", "W3", "undecided", "no rerun"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("want %q in: %s", want, msg)
		}
	}
}

func TestMissingRerunWithRun1OnlyIsPending(t *testing.T) {
	f := sampleFixture()
	f.in.Runs = f.in.Runs[:1]
	setChart(f, 0, undecidedChart("c"))
	res := f.analyze(t)
	if res.Final || len(res.Sample.PendingReruns) != 1 {
		t.Fatalf("final %v, pending %+v", res.Final, res.Sample.PendingReruns)
	}
	p := res.Sample.PendingReruns[0]
	if p.Chart != "c" || p.Run != "r1" || !strings.Contains(p.Why, "W3") {
		t.Fatalf("%+v", p)
	}
	u := res.Undecided
	if u.RerunsPending != 1 || u.BeforeReruns.Undecided != 1 || u.ByChartWorlds.Undecided != 1 || u.BeforeReruns.Of != 6 {
		t.Fatalf("%+v", u)
	}
	md := string(RenderMarkdown(res))
	for _, want := range []string{"rerun pending", "before the reruns: 1 of 6", "after the reruns: 1 of 6"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, md)
		}
	}
	// the rerun, once it exists, is applied in a run-1 analysis too
	addRerun(f, "c", 0, true, nil)
	res = f.analyze(t)
	if len(res.Sample.PendingReruns) != 0 || res.Undecided.ByChartWorlds.Undecided != 0 || res.Undecided.BeforeReruns.Undecided != 1 {
		t.Fatalf("%+v %+v", res.Sample.PendingReruns, res.Undecided)
	}
}

func TestBeforeEqualsAfterWithoutReruns(t *testing.T) {
	f := undecidedFixture(7, true)
	f.in.Reruns, f.in.RerunRefs = nil, nil
	f.in.Runs = f.in.Runs[:1] // run 1 only: the reruns are pending, none applied
	u := f.analyze(t).Undecided
	if u.RerunsPending != 7 || u.BeforeReruns != u.ByChartWorlds {
		t.Fatalf("%+v vs %+v", u.BeforeReruns, u.ByChartWorlds)
	}
}

func TestSecondRerunOfTheSameChartAndRunIsAnError(t *testing.T) {
	f := sampleFixture()
	setChart(f, 0, undecidedChart("a"))
	addRerun(f, "a", 0, true, nil)
	// a second dispatched run for the same chart and run
	second := f.in.Reruns[0]
	second.ID = "rr1b-a"
	f.in.Reruns = append(f.in.Reruns, second)
	f.in.RerunRefs = append(f.in.RerunRefs, RerunRef{Chart: "a", Run: "r1", Rerun: "rr1b-a"})
	if msg := f.analyzeErr(t); !strings.Contains(msg, "two reruns") {
		t.Fatal(msg)
	}
}

func TestRerunTiming(t *testing.T) {
	// a run-1 rerun must end before run 2 starts
	f := sampleFixture()
	setChart(f, 0, undecidedChart("a"))
	addRerun(f, "a", 0, true, nil)
	f.analyze(t)
	f.in.Reruns[0].Started = f.in.Runs[1].Started.Add(-time.Hour)
	f.in.Reruns[0].Ended = f.in.Runs[1].Started.Add(time.Minute)
	msg := f.analyzeErr(t)
	for _, want := range []string{`"a"`, "before run 2 starts", "r1"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("want %q in: %s", want, msg)
		}
	}
	f.in.Reruns[0].Ended = f.in.Runs[1].Started // not strictly before
	if msg := f.analyzeErr(t); !strings.Contains(msg, "before run 2 starts") {
		t.Fatal(msg)
	}
	// a run-2 rerun must start at least 12 h after run 1 ended
	run1 := RunInput{ID: "r1", Started: t0, Ended: t0.Add(time.Hour)}
	run2 := RunInput{ID: "r2", Started: t0.Add(13 * time.Hour), Ended: t0.Add(14 * time.Hour)}
	rr := &RunInput{ID: "x", Started: t0.Add(14 * time.Hour), Ended: t0.Add(15 * time.Hour)}
	if err := checkRerunTiming(rr, 1, []RunInput{run1, run2}, "a"); err != nil {
		t.Fatal(err)
	}
	rr.Started = t0.Add(12 * time.Hour) // 11 h after run 1 ended
	if err := checkRerunTiming(rr, 1, []RunInput{run1, run2}, "a"); err == nil || !strings.Contains(err.Error(), "at least 12 h after run 1 ended") {
		t.Fatal(err)
	}
	// a rerun of run 2 listed in a run-2 analysis is fine at the real position
	f = sampleFixture()
	setChart(f, 1, undecidedChart("a"))
	addRerun(f, "a", 1, true, nil)
	f.analyze(t)
}

// a chart whose W0 fails in the run and in its rerun is replaced (the
// replacement rule is unchanged); the same list of reruns serves both rules
func TestOneListOfRerunsServesTheReplacementRuleAndTheStop(t *testing.T) {
	f := sampleFixture()
	addReserve(f, "r1")
	setChart(f, 0, undecidedChart("c")) // c: undecided only, its rerun decides it
	pullLoss(f, 0, "b")                 // b: W0 fails in run 1
	addRerun(f, "c", 0, true, nil)
	addRerun(f, "b", 0, false, nil) // and again in its rerun
	replacing(f, "b", "r1", 0)
	res := f.analyze(t)
	if len(res.Sample.Replacements) != 1 || res.Sample.Replacements[0].Chart != "b" {
		t.Fatalf("%+v", res.Sample)
	}
	if len(res.Sample.Reruns) != 2 {
		t.Fatalf("%+v", res.Sample.Reruns)
	}
	if res.Undecided.ByChartWorlds.Undecided != 0 || res.Undecided.ByChartWorlds.Of != 6 {
		t.Fatalf("%+v", res.Undecided)
	}
}
