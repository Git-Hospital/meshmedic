package main

import (
	"strings"
	"testing"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

// Section 7's 15% can be read over cells or over chart-worlds. Both are
// reported, labelled; UndecidedStop names the one that drives the stop.
func TestUndecidedShareBothReadings(t *testing.T) {
	// chart a, W3: two differing keys in run 1, undecided in run 2: two undecided cells, one undecided chart-world.
	r1 := []ChartReport{chart("a", wr("W3", bad(wk), bad("helm:install")))}
	r2 := []ChartReport{chart("a", uw("W3", ledger.ReasonUndecided, bad(wk), bad("helm:install")))}
	f := newFixture(r1, r2, nil)
	f.pad(9) // 10 charts x W1, W3 = 20 compared chart-worlds
	f.rerunUndecided()
	res := f.analyze(t)
	u := res.Undecided
	if u.ByCells.Undecided != 2 || u.ByCells.Of != 2 || !u.ByCells.Over {
		t.Fatalf("cells reading %+v", u.ByCells)
	}
	if u.ByChartWorlds.Undecided != 1 || u.ByChartWorlds.Of != 20 || u.ByChartWorlds.Over {
		t.Fatalf("chart-worlds reading %+v", u.ByChartWorlds)
	}
	if UndecidedStop != ReadingChartWorlds || u.Drives != ReadingChartWorlds || u.Over != u.ByChartWorlds.Over || u.Over {
		t.Fatalf("the chart-worlds reading drives the stop, and it is below 15%% here: %+v", u)
	}
	md := string(RenderMarkdown(res))
	for _, want := range []string{"cells reading: 2 of 2", "chart-worlds reading: 1 of 20", "drives the stop: chart-worlds"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, md)
		}
	}
}

// undecidedFixture has 20 charts (40 chart-worlds, W1 and W3). The first n have
// an undecided W1 in run 1 (no keys, so no undecided cell); with two set, run 2
// has the same undecided worlds.
func undecidedFixture(n int, two bool) *fixture {
	build := func() []ChartReport {
		var cs []ChartReport
		for i, name := range names(20, "c") {
			if i < n {
				cs = append(cs, chart(name, uw("W1", ledger.ReasonUndecided)))
			} else {
				cs = append(cs, chart(name, wr("W1", ok(wk)), wr("W3", ok(wk))))
			}
		}
		return cs
	}
	var r2 []ChartReport
	if two {
		r2 = build()
	}
	f := newFixture(build(), r2, nil)
	f.rerunUndecided() // undecided again after the rerun: the chart-world stays undecided and counts
	return f
}

func TestUndecidedStopBoundary(t *testing.T) {
	for _, two := range []bool{false, true} {
		// 6 of 40 is exactly 15%: not over. 7 of 40 is over.
		res := undecidedFixture(6, two).analyze(t)
		u := res.Undecided
		if u.ByChartWorlds.Undecided != 6 || u.ByChartWorlds.Of != 40 || u.ByChartWorlds.Over || u.Over {
			t.Fatalf("two=%v: 6 of 40 must not stop: %+v", two, u)
		}
		res = undecidedFixture(7, two).analyze(t)
		u = res.Undecided
		if u.ByChartWorlds.Undecided != 7 || u.ByChartWorlds.Of != 40 || !u.ByChartWorlds.Over || !u.Over {
			t.Fatalf("two=%v: 7 of 40 must stop: %+v", two, u)
		}
	}
}

// A chart-world undecided in both runs counts once.
func TestUndecidedChartWorldCountsOnce(t *testing.T) {
	u := undecidedFixture(6, true).analyze(t).Undecided
	if u.ByChartWorlds.Undecided != 6 {
		t.Fatalf("%+v", u.ByChartWorlds)
	}
}

// The readings disagree the other way: chart-worlds above 15% (7 of 40) while
// the cells reading is 0 of 0. The stop follows the chart-worlds reading.
func TestUndecidedReadingsDisagreeChartWorldsOver(t *testing.T) {
	res := undecidedFixture(7, true).analyze(t)
	u := res.Undecided
	if u.ByCells.Over || !u.ByChartWorlds.Over || !u.Over || u.Drives != ReadingChartWorlds {
		t.Fatalf("%+v", u)
	}
}

// Run 1 only: the share is the share of that run, and it still drives the stop.
func TestUndecidedRun1Only(t *testing.T) {
	res := undecidedFixture(7, false).analyze(t)
	if res.Final {
		t.Fatal("run 1 only must not be final")
	}
	if !res.Undecided.Over || res.Undecided.ByChartWorlds.Of != 40 {
		t.Fatalf("%+v", res.Undecided)
	}
	md := string(RenderMarkdown(res))
	for _, want := range []string{"chart-worlds reading: 7 of 40", "(OVER 15%)", "drives the stop: chart-worlds"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, md)
		}
	}
}
