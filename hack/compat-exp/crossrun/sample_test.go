package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		if msg := f.analyzeErr(t); !strings.Contains(msg, `W0 was not 3 of 3 in run r1 (W3: 0 of 3 W0 runs usable)`) {
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
}

// W0 helpers for the replacement rule (sample.go, INTERPRETATIONS.md 22).

// pullLoss makes a chart's W0 lose run 3 to a pull failure in the named run
// (index 0 or 1): two of three usable runs in every world, as difftable
// reports it.
func pullLoss(f *fixture, run int, id string) {
	for i := range f.in.Runs[run].Report.Charts {
		ch := &f.in.Runs[run].Report.Charts[i]
		if ch.Chart != id || ch.FixOf != "" {
			continue
		}
		for k := range ch.Worlds {
			ch.Worlds[k].OracleRunsUsed = []int{1, 2}
			ch.Worlds[k].OracleRunsLeftOut = []LeftOut{{Run: 3, Reason: "pull-failure", Detail: "docker.io/x"}}
		}
	}
}

// installFailed makes the chart's W0 installs fail in every run of the report.
func installFailed(ch *ChartReport) {
	ch.OracleInstall = OracleInstall{Runs: 3, Failed: 3, Status: "failed"}
}

// addRerun adds a rerun of the chart's W0 job for the run (0 or 1): an observe
// run restricted to the chart, after the run, with the chart's report as given
// (good: W0 3 of 3). It names the rerun in the replacements file's reruns list
// and returns its id.
func addRerun(f *fixture, id string, run int, good bool, mod func(*ChartReport)) string {
	rid := fmt.Sprintf("rr%d-%s", run+1, id)
	ch := chart(id, wr("W1", ok(wk)), wr("W3", ok(wk)))
	if !good {
		for k := range ch.Worlds {
			ch.Worlds[k].OracleRunsUsed = []int{1}
			ch.Worlds[k].OracleRunsLeftOut = []LeftOut{{Run: 2, Reason: "pull-failure", Detail: "docker.io/x"}, {Run: 3, Reason: "pull-failure", Detail: "docker.io/x"}}
		}
	}
	if mod != nil {
		mod(&ch)
	}
	start := f.in.Runs[run].Ended.Add(time.Hour)
	f.in.Reruns = append(f.in.Reruns, RunInput{ID: rid, Started: start, Ended: start.Add(30 * time.Minute), Report: report(ch)})
	f.in.RerunRefs = append(f.in.RerunRefs, RerunRef{Chart: id, Run: f.in.Runs[run].ID, Rerun: rid})
	return rid
}

// replacing moves the rerun ref of the chart into a replacement entry.
func replacing(f *fixture, id, reserve string, run int) {
	var refs []RerunRef
	rerun := ""
	for _, r := range f.in.RerunRefs {
		if r.Chart == id && r.Run == f.in.Runs[run].ID {
			rerun = r.Rerun
			continue
		}
		refs = append(refs, r)
	}
	f.in.RerunRefs = refs
	f.in.Replacements = append(f.in.Replacements, Replacement{Chart: id, Reserve: reserve, Reason: "W0 not 3 of 3 in the run and its rerun", Run: f.in.Runs[run].ID, Rerun: rerun})
}

// addReserve adds the reserve's reports to both runs.
func addReserve(f *fixture, id string) {
	for run := 0; run < 2; run++ {
		addChart(f, run, chart(id, wr("W1", ok(wk)), wr("W3", ok(wk))))
	}
}

func TestWithoutRerunsNothingIsReplaced(t *testing.T) {
	f := sampleFixture()
	res := f.analyze(t)
	if len(res.Sample.Replacements) != 0 || len(res.Sample.Reruns) != 0 {
		t.Fatalf("%+v", res.Sample)
	}
}

// R4, the auditor's scenario: charts a and b each lost one W0 run to a pull
// failure in run 2. Replacing a deleted its finding; under the rule neither is
// replaced unless the rerun of its W0 job fails too, and the choice of which
// to replace is gone.
func TestOnePullFailureIsNotAReplacement(t *testing.T) {
	build := func() *fixture {
		mk := func() []ChartReport {
			return []ChartReport{chart("a", wr("W1", bad("port:a:80:http")), wr("W3", ok(wk))), chart("b", wr("W3", ok(wk))), chart("c", wr("W3", ok(wk)))}
		}
		f := newFixture(mk(), mk(), nil)
		f.in.Reserves = []string{"r1"}
		f.in.FrameOrder = []string{"a", "b", "c", "r1"}
		f.in.Publisher["r1"], f.in.Qualified["r1"] = "pub-r1", true
		pullLoss(f, 1, "a")
		pullLoss(f, 1, "b")
		return f
	}
	t.Run("both reruns are fine: neither is replaced, the finding of a stays, the rerun's records are used", func(t *testing.T) {
		f := build()
		addRerun(f, "a", 1, true, func(c *ChartReport) { c.Worlds[0] = wr("W1", bad("port:a:80:http")) })
		addRerun(f, "b", 1, true, nil)
		res := f.analyze(t)
		if len(res.Sample.Replacements) != 0 || len(res.Sample.Reruns) != 2 || res.Sample.Reruns[0].Failed {
			t.Fatalf("%+v", res.Sample)
		}
		c := cellOf(t, res, "a", "W1", "port:a:80:http")
		if c.Run2 != "finding" {
			t.Fatalf("the finding of a must stay, from the rerun's data: %+v", c)
		}
	})
	t.Run("the analyst cannot pick a to replace: the rule says no", func(t *testing.T) {
		f := build()
		addRerun(f, "a", 1, true, nil)
		addRerun(f, "b", 1, true, nil)
		replacing(f, "a", "r1", 1)
		addReserve(f, "r1")
		if msg := f.analyzeErr(t); !strings.Contains(msg, "the rule does not replace it") {
			t.Fatal(msg)
		}
	})
	t.Run("without a rerun the chart is neither replaced nor accepted", func(t *testing.T) {
		f := build()
		addRerun(f, "b", 1, true, nil)
		msg := f.analyzeErr(t)
		if !strings.Contains(msg, `chart "a": W0 was not 3 of 3 in run r2`) || !strings.Contains(msg, "no rerun") {
			t.Fatal(msg)
		}
	})
	t.Run("the rerun of a fails too: a is replaced by the next reserve, b is not", func(t *testing.T) {
		f := build()
		addRerun(f, "a", 1, false, nil)
		addRerun(f, "b", 1, true, nil)
		replacing(f, "a", "r1", 1)
		addReserve(f, "r1")
		res := f.analyze(t)
		if len(res.Sample.Replacements) != 1 || len(res.Sample.Reruns) != 2 || !res.Sample.Reruns[0].Failed || len(res.Cells) != 0 {
			t.Fatalf("%+v %+v", res.Sample, res.Cells)
		}
	})
	t.Run("a replacement is required when the rerun failed too", func(t *testing.T) {
		f := build()
		addRerun(f, "a", 1, false, nil)
		addRerun(f, "b", 1, true, nil)
		if msg := f.analyzeErr(t); !strings.Contains(msg, "no such replacement is in the replacements file") {
			t.Fatal(msg)
		}
	})
}

func TestReplacementRule(t *testing.T) {
	// a's W0 fails in run 1 and in its rerun; r1 replaces it in both runs
	build := func() *fixture {
		f := sampleFixture()
		for run := 0; run < 2; run++ {
			dropChart(f, run, "a")
		}
		addChart(f, 0, chart("a", wr("W1", ok(wk)), wr("W3", ok(wk))))
		pullLoss(f, 0, "a")
		addReserve(f, "r1")
		f.in.Runs[1].Started = f.in.Runs[0].Ended.Add(14 * time.Hour)
		f.in.Runs[1].Ended = f.in.Runs[1].Started.Add(time.Hour)
		addRerun(f, "a", 0, false, nil)
		replacing(f, "a", "r1", 0)
		return f
	}
	t.Run("valid", func(t *testing.T) { build().analyze(t) })
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
	t.Run("reserves in list order", func(t *testing.T) {
		f := build()
		f.in.Replacements[0].Reserve = "r2"
		if msg := f.analyzeErr(t); !strings.Contains(msg, `the next reserve is "r1"`) {
			t.Fatal(msg)
		}
	})
	t.Run("the file must name the rerun of the manifest", func(t *testing.T) {
		f := build()
		f.in.Replacements[0].Rerun = "999"
		if msg := f.analyzeErr(t); !strings.Contains(msg, "is not a rerun of the manifest") {
			t.Fatal(msg)
		}
	})
	t.Run("install failures count: W0 runs usable but the installs failed", func(t *testing.T) {
		f := build()
		// a's W0 is 3 usable runs in run 1 but helm install failed in all of them, and again in the rerun
		for i := range f.in.Runs[0].Report.Charts {
			ch := &f.in.Runs[0].Report.Charts[i]
			if ch.Chart == "a" {
				for k := range ch.Worlds {
					ch.Worlds[k].OracleRunsUsed, ch.Worlds[k].OracleRunsLeftOut = []int{1, 2, 3}, nil
				}
				installFailed(ch)
			}
		}
		for i := range f.in.Reruns {
			for k := range f.in.Reruns[i].Report.Charts[0].Worlds {
				f.in.Reruns[i].Report.Charts[0].Worlds[k].OracleRunsUsed, f.in.Reruns[i].Report.Charts[0].Worlds[k].OracleRunsLeftOut = []int{1, 2, 3}, nil
			}
			installFailed(&f.in.Reruns[i].Report.Charts[0])
		}
		res := f.analyze(t)
		if len(res.Sample.Replacements) != 1 || !strings.Contains(res.Sample.Reruns[0].Why, "installs 0 of 3") && !strings.Contains(res.Sample.Reruns[0].Why, "0 of 3 ok") {
			t.Fatalf("%+v", res.Sample)
		}
	})
	t.Run("a taken reserve can itself be replaced by the next one", func(t *testing.T) {
		f := build()
		// r1 loses W0 in run 2 and in its rerun; r2 replaces it in both runs
		addReserve(f, "r2")
		pullLoss(f, 1, "r1")
		addRerun(f, "r1", 1, false, nil)
		replacing(f, "r1", "r2", 1)
		f.analyze(t)
		// and r2 cannot be taken first
		g := build()
		addReserve(g, "r2")
		pullLoss(g, 1, "r1")
		addRerun(g, "r1", 1, false, nil)
		replacing(g, "r1", "r2", 1)
		g.in.Replacements[0].Reserve = "r2"
		if msg := g.analyzeErr(t); !strings.Contains(msg, `the next reserve is "r1"`) {
			t.Fatal(msg)
		}
	})
}

func TestRerunEvidence(t *testing.T) {
	build := func() *fixture {
		f := sampleFixture()
		pullLoss(f, 1, "b")
		addRerun(f, "b", 1, true, nil)
		return f
	}
	t.Run("a good rerun stands in", func(t *testing.T) { build().analyze(t) })
	t.Run("the rerun holds another chart", func(t *testing.T) {
		f := build()
		f.in.Reruns[0].Report.Charts = append(f.in.Reruns[0].Report.Charts, chart("c", wr("W3", ok(wk))))
		if msg := f.analyzeErr(t); !strings.Contains(msg, "restricted to the one chart") {
			t.Fatal(msg)
		}
	})
	t.Run("the rerun started before the run ended", func(t *testing.T) {
		f := build()
		f.in.Reruns[0].Started = f.in.Runs[1].Started
		if msg := f.analyzeErr(t); !strings.Contains(msg, "it is not a rerun") {
			t.Fatal(msg)
		}
	})
	t.Run("a rerun for a run in which W0 was 3 of 3 is refused", func(t *testing.T) {
		f := build()
		addRerun(f, "c", 1, true, nil)
		if msg := f.analyzeErr(t); !strings.Contains(msg, "leaves no chart-world") {
			t.Fatal(msg)
		}
	})
	t.Run("a rerun in the manifest that no entry names", func(t *testing.T) {
		f := build()
		f.in.RerunRefs = nil
		msg := f.analyzeErr(t)
		if !strings.Contains(msg, "no rerun") {
			t.Fatal(msg)
		}
	})
	t.Run("a run-1 rerun is fine when it ends before run 2 starts, an error when it does not", func(t *testing.T) {
		f := sampleFixture()
		pullLoss(f, 0, "b")
		addRerun(f, "b", 0, true, nil) // ends 1.5 h after run 1 ended; run 2 starts 12 h after run 1 ended
		f.analyze(t)
		f.in.Reruns[0].Ended = f.in.Runs[1].Started.Add(time.Minute)
		if msg := f.analyzeErr(t); !strings.Contains(msg, "before run 2 starts") {
			t.Fatal(msg)
		}
	})
	t.Run("the rerun's records stand in for the run's", func(t *testing.T) {
		f := build()
		f.in.Runs[1].HasRecords = true
		f.in.Runs[1].Records = []Record{{Chart: "b", World: "W3", Run: 1}, {Chart: "a", World: "W3", Run: 1}}
		f.in.Reruns[0].HasRecords = true
		f.in.Reruns[0].Records = []Record{{Chart: "b", World: "W3", Run: 2}}
		res, err := Analyze(f.in)
		if err != nil || res == nil {
			t.Fatal(err)
		}
		// the caller's inputs are not mutated
		if len(f.in.Runs[1].Records) != 2 {
			t.Fatal("Analyze changed its input")
		}
	})
}

func TestReplacementsFileSchema(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"empty", `{"schema":1,"replacements":[]}`, ""},
		{"one", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":"W0 failed","run":"12","rerun":"15"}]}`, ""},
		{"reruns list", `{"schema":1,"reruns":[{"chart":"a","run":"12","rerun":"15"}],"replacements":[]}`, ""},
		{"unknown field", `{"schema":1,"replacements":[],"x":1}`, "unknown field"},
		{"schema", `{"schema":2,"replacements":[]}`, "schema 2"},
		{"no reason", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":" ","run":"12","rerun":"15"}]}`, "reason is empty"},
		{"bad run", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":"x","run":"1a","rerun":"15"}]}`, "digits only"},
		{"no rerun", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":"x","run":"12"}]}`, "rerun"},
		{"rerun is the run", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":"x","run":"12","rerun":"12"}]}`, "the rerun is the run itself"},
		{"bad chart", `{"schema":1,"replacements":[{"chart":"A b","reserve":"r1","reason":"x","run":"1","rerun":"2"}]}`, "not a chart id"},
		{"twice", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":"x","run":"1","rerun":"2"},{"chart":"a","reserve":"r2","reason":"x","run":"1","rerun":"3"}]}`, "replaced twice"},
		{"reserve twice", `{"schema":1,"replacements":[{"chart":"a","reserve":"r1","reason":"x","run":"1","rerun":"2"},{"chart":"b","reserve":"r1","reason":"x","run":"1","rerun":"3"}]}`, "used twice"},
		{"two reruns of one run", `{"schema":1,"reruns":[{"chart":"a","run":"1","rerun":"2"},{"chart":"a","run":"1","rerun":"3"}],"replacements":[]}`, "one rerun only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "r.json")
			_ = os.WriteFile(p, []byte(tc.body), 0o644)
			_, _, err := LoadReplacements(p)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("%v", err)
			}
		})
	}
}
