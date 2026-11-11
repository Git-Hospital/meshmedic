package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleFixture: primary charts a, b, c in both runs, reserves r1, r2 in the frame.
func sampleFixture() *fixture {
	mk := func() []ChartReport {
		return []ChartReport{chart("a", wr("W3", ok(wk))), chart("b", wr("W3", ok(wk))), chart("c", wr("W3", ok(wk)))}
	}
	f := newFixture(mk(), mk(), nil)
	for _, r := range []string{"r1", "r2"} {
		f.in.Publisher[r], f.in.Qualified[r] = "pub-"+r, true
		f.in.FrameOrder = append(f.in.FrameOrder, r)
		f.in.Reserves = append(f.in.Reserves, r)
	}
	return f
}

// w0Failed makes a chart's W0 2 of 3 in the named run's report.
func w0Failed(f *fixture, run int, id string) {
	for i := range f.in.Runs[run].Report.Charts {
		if f.in.Runs[run].Report.Charts[i].Chart == id {
			for k := range f.in.Runs[run].Report.Charts[i].Worlds {
				f.in.Runs[run].Report.Charts[i].Worlds[k].OracleRunsUsed = []int{1, 2}
			}
		}
	}
}

func dropChart(f *fixture, run int, id string) {
	var keep []ChartReport
	for _, c := range f.in.Runs[run].Report.Charts {
		if c.Chart != id {
			keep = append(keep, c)
		}
	}
	f.in.Runs[run].Report.Charts = keep
}

func addChart(f *fixture, run int, c ChartReport) {
	f.in.Runs[run].Report.Charts = append(f.in.Runs[run].Report.Charts, c)
}

// F6: a missing chart is an error naming chart and world, not a smaller denominator.
func TestSampleMustBeComplete(t *testing.T) {
	t.Run("complete", func(t *testing.T) { sampleFixture().analyze(t) })
	t.Run("a chart missing from run 2", func(t *testing.T) {
		f := sampleFixture()
		dropChart(f, 1, "b")
		msg := f.analyzeErr(t)
		if !strings.Contains(msg, `run r2: chart "b" is missing`) {
			t.Fatal(msg)
		}
	})
	t.Run("a world missing from run 1", func(t *testing.T) {
		f := sampleFixture()
		for i := range f.in.Runs[0].Report.Charts {
			if f.in.Runs[0].Report.Charts[i].Chart == "c" {
				f.in.Runs[0].Report.Charts[i].Worlds = f.in.Runs[0].Report.Charts[i].Worlds[:1] // W3 only
			}
		}
		if msg := f.analyzeErr(t); !strings.Contains(msg, `run r1: chart "c" has no world W1`) {
			t.Fatal(msg)
		}
	})
	t.Run("a world with no W0 runs", func(t *testing.T) {
		f := sampleFixture()
		f.in.Runs[0].Report.Charts[0].Worlds[0].OracleRunsUsed = nil
		if msg := f.analyzeErr(t); !strings.Contains(msg, `chart "a" world W3 has no W0 runs`) {
			t.Fatal(msg)
		}
	})
	t.Run("a chart outside the sample is an error", func(t *testing.T) {
		f := sampleFixture()
		addChart(f, 0, chart("r1", wr("W3", ok(wk))))
		if msg := f.analyzeErr(t); !strings.Contains(msg, `chart "r1" is in the report but not in the sample`) {
			t.Fatal(msg)
		}
	})
	t.Run("W0 not 3 of 3 and not replaced is a warning, not a smaller sample", func(t *testing.T) {
		f := sampleFixture()
		w0Failed(f, 0, "a")
		res := f.analyze(t)
		if !strings.Contains(strings.Join(res.Warnings, "|"), "not replaced") {
			t.Fatal(res.Warnings)
		}
	})
}

func TestReplacements(t *testing.T) {
	// a's W0 failed in run 1; r1 replaces it in both runs
	build := func() *fixture {
		f := sampleFixture()
		w0Failed(f, 0, "a")
		for run := 0; run < 2; run++ {
			dropChart(f, run, "a")
			addChart(f, run, chart("r1", wr("W3", ok(wk))))
		}
		// run 1 still shows a, with its failed W0, as the evidence
		addChart(f, 0, chart("a", wr("W3", ok(wk))))
		w0Failed(f, 0, "a")
		f.in.Replacements = []Replacement{{Chart: "a", Reserve: "r1", Reason: "W0 install failed", Run: "r1"}}
		return f
	}
	t.Run("valid", func(t *testing.T) {
		build().analyze(t)
	})
	t.Run("the replaced chart's cells are dropped", func(t *testing.T) {
		f := build()
		for i := range f.in.Runs[0].Report.Charts {
			if f.in.Runs[0].Report.Charts[i].Chart == "a" {
				f.in.Runs[0].Report.Charts[i].Worlds[0].Keys = []KeyReport{bad(wk)}
			}
		}
		if res := f.analyze(t); len(res.Cells) != 0 {
			t.Fatalf("%+v", res.Cells)
		}
	})
	t.Run("the reserve must be in both runs", func(t *testing.T) {
		f := build()
		dropChart(f, 1, "r1")
		if msg := f.analyzeErr(t); !strings.Contains(msg, `run r2: chart "r1" is missing`) {
			t.Fatal(msg)
		}
	})
	t.Run("W0 was 3 of 3: no replacement", func(t *testing.T) {
		f := build()
		for i := range f.in.Runs[0].Report.Charts {
			f.in.Runs[0].Report.Charts[i].Worlds[0].OracleRunsUsed = []int{1, 2, 3}
			f.in.Runs[0].Report.Charts[i].Worlds[1].OracleRunsUsed = []int{1, 2, 3}
		}
		if msg := f.analyzeErr(t); !strings.Contains(msg, "it did not fail") {
			t.Fatal(msg)
		}
	})
	t.Run("reserves in list order", func(t *testing.T) {
		f := build()
		f.in.Replacements[0].Reserve = "r2"
		if msg := f.analyzeErr(t); !strings.Contains(msg, `the next reserve is "r1"`) {
			t.Fatal(msg)
		}
	})
	t.Run("evidence run must be a manifest run", func(t *testing.T) {
		f := build()
		f.in.Replacements[0].Run = "999"
		if msg := f.analyzeErr(t); !strings.Contains(msg, "evidence run 999 is not a run of the manifest") {
			t.Fatal(msg)
		}
	})
	t.Run("no report of the chart in the evidence run", func(t *testing.T) {
		f := build()
		dropChart(f, 0, "a")
		if msg := f.analyzeErr(t); !strings.Contains(msg, "cannot be verified") {
			t.Fatal(msg)
		}
	})
	t.Run("only a chart of the sample can be replaced", func(t *testing.T) {
		f := build()
		f.in.Replacements[0].Chart = "zz"
		if msg := f.analyzeErr(t); !strings.Contains(msg, "is not a chart of the sample") {
			t.Fatal(msg)
		}
	})
	t.Run("a taken reserve can itself be replaced by the next one", func(t *testing.T) {
		f := build()
		// r1's W0 failed in run 2; r2 replaces it in both runs
		dropChart(f, 1, "r1")
		addChart(f, 1, chart("r1", wr("W3", ok(wk))))
		w0Failed(f, 1, "r1")
		for run := 0; run < 2; run++ {
			dropChart(f, run, "r1")
			addChart(f, run, chart("r2", wr("W3", ok(wk))))
		}
		addChart(f, 1, chart("r1", wr("W3", ok(wk))))
		w0Failed(f, 1, "r1")
		f.in.Replacements = append(f.in.Replacements, Replacement{Chart: "r1", Reserve: "r2", Reason: "W0 failed again", Run: "r2"})
		f.analyze(t)
	})
}

func TestReplacementsFileSchema(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"empty", `{"schema":1,"replacements":[]}`, ""},
		{"one", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":"W0 failed","run":"12"}]}`, ""},
		{"unknown field", `{"schema":1,"replacements":[],"x":1}`, "unknown field"},
		{"schema", `{"schema":2,"replacements":[]}`, "schema 2"},
		{"no reason", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":" ","run":"12"}]}`, "reason is empty"},
		{"bad run", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":"x","run":"1a"}]}`, "digits only"},
		{"bad chart", `{"schema":1,"replacements":[{"chart":"A b","reserve":"r1","reason":"x","run":"1"}]}`, "not a chart id"},
		{"twice", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":"x","run":"1"},{"chart":"a","reserve":"r2","reason":"x","run":"1"}]}`, "replaced twice"},
		{"reserve twice", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":"x","run":"1"},{"chart":"b","reserve":"r1","reason":"x","run":"1"}]}`, "used twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "r.json")
			_ = os.WriteFile(p, []byte(tc.body), 0o644)
			_, err := LoadReplacements(p)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("%v", err)
			}
		})
	}
}
