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
	res := f.analyze(t)
	u := res.Undecided
	if u.ByCells.Undecided != 2 || u.ByCells.Of != 2 || !u.ByCells.Over {
		t.Fatalf("cells reading %+v", u.ByCells)
	}
	if u.ByChartWorlds.Undecided != 1 || u.ByChartWorlds.Of != 20 || u.ByChartWorlds.Over {
		t.Fatalf("chart-worlds reading %+v", u.ByChartWorlds)
	}
	if UndecidedStop != ReadingCells || u.Drives != ReadingCells || u.Over != u.ByCells.Over {
		t.Fatalf("the cell reading drives the stop: %+v", u)
	}
	md := string(RenderMarkdown(res))
	for _, want := range []string{"cells reading: 2 of 2", "chart-worlds reading: 1 of 20", "drives the stop: cells"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, md)
		}
	}
}
