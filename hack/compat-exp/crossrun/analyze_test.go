package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

func TestRule1BothRunsAndGap(t *testing.T) {
	both := []ChartReport{chart("a", wr("W3", bad(wk)))}
	oneRun := []ChartReport{chart("a", wr("W3", ok(wk)))}
	cases := []struct {
		name    string
		r2      []ChartReport
		gap     time.Duration
		status  string
		holds   bool
		warning bool
	}{
		{"both runs, 13 h", both, 12 * time.Hour, StUnresolved, true, false},
		{"gap exactly 12 h", both, 12 * time.Hour, StUnresolved, true, false},
		{"run 1 only differs", oneRun, 13 * time.Hour, StNotReproduce, false, false},
		{"run 2 only differs", oneRun, 13 * time.Hour, StNotReproduce, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r1 := both
			if tc.name == "run 2 only differs" {
				r1, tc.r2 = oneRun, both
			}
			f := newFixture(r1, tc.r2, nil)
			f.in.Runs[1].Started = f.in.Runs[0].Ended.Add(tc.gap)
			f.in.Runs[1].Ended = f.in.Runs[1].Started.Add(time.Hour)
			res := f.analyze(t)
			c := cellOf(t, res, "a", "W3", wk)
			if c.Status != tc.status || c.Holds != tc.holds {
				t.Fatalf("status %s holds %v, want %s %v (%+v)", c.Status, c.Holds, tc.status, tc.holds, c)
			}
			if strings.Contains(strings.Join(res.Warnings, "|"), "12 h") != tc.warning {
				t.Fatalf("warnings %v", res.Warnings)
			}
			if res.GapOK == tc.warning {
				t.Fatalf("gapOk %v", res.GapOK)
			}
		})
	}
}

func TestW0ThreeOfThreeIsRequired(t *testing.T) {
	c := chart("a", wr("W3", bad(wk)))
	c.Worlds[0].OracleRunsUsed = []int{1, 2}
	f := newFixture([]ChartReport{c}, []ChartReport{chart("a", wr("W3", bad(wk)))}, nil)
	if msg := f.analyzeErr(t); !strings.Contains(msg, "agreeing oracle runs") {
		t.Fatal(msg)
	}
	c2 := chart("a", wr("W3", bad(wk)))
	c2.Worlds[0].Keys[0].OracleValue = nil
	f = newFixture([]ChartReport{chart("a", wr("W3", bad(wk)))}, []ChartReport{c2}, nil)
	if msg := f.analyzeErr(t); !strings.Contains(msg, "agreeing oracle runs") {
		t.Fatal(msg)
	}
	f = newFixture([]ChartReport{chart("a", wr("W3", bad(wk)))}, []ChartReport{chart("a", wr("W3", bad(wk)))}, nil)
	f.in.Runs[1].Report.RunsRequired = 2
	if msg := f.analyzeErr(t); !strings.Contains(msg, "rule 1 needs 3 of 3") {
		t.Fatal(msg)
	}
}

func TestSingleRunAwaitsRun2(t *testing.T) {
	f := newFixture([]ChartReport{chart("a", wr("W3", bad(wk)))}, nil, nil)
	res := f.analyze(t)
	c := cellOf(t, res, "a", "W3", wk)
	if c.Status != StAwaitRun2 || c.Holds {
		t.Fatalf("%+v", c)
	}
	// it still feeds the futility bound
	if res.Futility.PendingCells != 1 || res.Futility.NMax != 1 || !res.Futility.StopDrop {
		t.Fatalf("%+v", res.Futility)
	}
}

func TestMechanicalExclusions(t *testing.T) {
	d := func(k string) KeyReport { return fk(k, "true", "false", "false", "false") }
	cases := []struct {
		name   string
		r1, r2 WorldReport
		reason string
	}{
		{"pull failure", wr("W3", d(wk)), uw("W3", ledger.ReasonPullFailure, d(wk)), ledger.ReasonPullFailure},
		{"capacity", wr("W3", d(wk)), uw("W3", ledger.ReasonCapacity, d(wk)), ledger.ReasonCapacity},
		{"undecided", wr("W3", d(wk)), uw("W3", ledger.ReasonUndecided, d(wk)), ledger.ReasonUndecided},
		{"flaky in run 2", wr("W3", d(wk)), wr("W3", fk(wk, "true", "false", "true", "false")), ledger.ReasonFlaky},
		{"flaky in run 1", wr("W3", fk(wk, "true", "false", "true", "false")), wr("W3", ok(wk)), ledger.ReasonFlaky},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture([]ChartReport{chart("a", tc.r1)}, []ChartReport{chart("a", tc.r2)}, nil)
			c := cellOf(t, f.analyze(t), "a", "W3", wk)
			if c.Status != StExcluded || len(c.Reasons) != 1 || c.Reasons[0] != tc.reason || c.Holds {
				t.Fatalf("%+v", c)
			}
		})
	}
	t.Run("pull failure outranks capacity and undecided", func(t *testing.T) {
		w := uw("W3", ledger.ReasonCapacity, d(wk))
		w.OracleRunsLeftOut = []LeftOut{{Run: 2, Reason: "pull-failure"}}
		f := newFixture([]ChartReport{chart("a", wr("W3", d(wk)))}, []ChartReport{chart("a", w)}, nil)
		c := cellOf(t, f.analyze(t), "a", "W3", wk)
		if c.Reasons[0] != ledger.ReasonPullFailure {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("an undecided key whose values agree with the oracle is not a cell", func(t *testing.T) {
		f := newFixture([]ChartReport{chart("a", wr("W3", ok(wk)))}, []ChartReport{chart("a", uw("W3", ledger.ReasonPullFailure, ok(wk)))}, nil)
		res := f.analyze(t)
		if len(res.Cells) != 0 || len(res.UndecidedWorld) != 1 || res.UndecidedWorld[0].Kind != ledger.ReasonPullFailure {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("fix entries in a sample report are not cells", func(t *testing.T) {
		f := newFixture([]ChartReport{chart("a", wr("W3", ok(wk))), fixEntry("a-fix", "a", "W3", bad(wk))}, []ChartReport{chart("a", wr("W3", ok(wk)))}, nil)
		if res := f.analyze(t); len(res.Cells) != 0 {
			t.Fatalf("%+v", res.Cells)
		}
	})
	t.Run("a chart outside the frame stops", func(t *testing.T) {
		f := newFixture([]ChartReport{chart("a", wr("W3", ok(wk)))}, []ChartReport{chart("a", wr("W3", ok(wk)))}, nil)
		delete(f.in.Publisher, "a")
		if msg := f.analyzeErr(t); !strings.Contains(msg, `"a" is not in the frame`) {
			t.Fatal(msg)
		}
	})
}

// worldUniversalFixture has n qualified charts that all have an E1 key in W1;
// the first `differ` of them differ in both runs.
func worldUniversalFixture(n, differ int) *fixture {
	var r []ChartReport
	for i, name := range names(n, "c") {
		k := ok("apiserver:svc:s:443:reachable")
		if i < differ {
			k = bad("apiserver:svc:s:443:reachable")
		}
		r = append(r, chart(name, wr("W1", k, ok(wk))))
	}
	return newFixture(r, r, nil)
}

func TestWorldUniversalClassThreshold(t *testing.T) {
	cases := []struct {
		n, differ int
		universal bool
	}{
		{10, 9, true},   // exactly 90%
		{10, 8, false},  // 80%
		{20, 18, true},  // exactly 90%
		{20, 17, false}, // 85%
		{11, 10, true},  // 90.9%
		{11, 9, false},  // 81.8%
		{1, 1, true},
		{3, 2, false}, // 66.7%
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d of %d", tc.differ, tc.n), func(t *testing.T) {
			res := worldUniversalFixture(tc.n, tc.differ).analyze(t)
			var row *UniversalRow
			for i := range res.Universal {
				if res.Universal[i].Kind == "class" && res.Universal[i].Name == "apiserver" && res.Universal[i].World == "W1" {
					row = &res.Universal[i]
				}
			}
			if row == nil {
				t.Fatal("no apiserver row")
			}
			if row.Numerator != tc.differ || row.Denominator != tc.n || row.Universal != tc.universal {
				t.Fatalf("%+v", row)
			}
			for _, c := range res.Cells {
				want := StUnresolved
				if tc.universal {
					want = StExcluded
				}
				if c.Status != want {
					t.Fatalf("cell %+v, want %s", c, want)
				}
				if tc.universal && c.Reasons[0] != ledger.ReasonWorldUniversal {
					t.Fatalf("%+v", c)
				}
			}
		})
	}
}

func TestWorldUniversalDenominatorCountsOnlyChartsWithTheClass(t *testing.T) {
	// 9 charts have the class and differ; 5 have no E1 keys at all.
	var r []ChartReport
	for _, n := range names(9, "e") {
		r = append(r, chart(n, wr("W1", bad("apiserver:svc:s:443:reachable"))))
	}
	for _, n := range names(5, "n") {
		r = append(r, chart(n, wr("W1", ok(wk))))
	}
	res := newFixture(r, r, nil).analyze(t)
	for _, u := range res.Universal {
		if u.Name == "apiserver" && (u.Denominator != 9 || !u.Universal) {
			t.Fatalf("%+v", u)
		}
	}
	// unqualified charts are not in the denominator
	f := newFixture(r, r, nil)
	f.in.Qualified["e01"] = false
	delete(f.in.Qualified, "e01")
	f.in.Qualified["e01"] = false
	if msg := f.analyzeErr(t); !strings.Contains(msg, "not qualified") {
		t.Fatal(msg)
	}
}

func TestWorldUniversalIsPerWorldAndPerClass(t *testing.T) {
	var r []ChartReport
	for _, n := range names(10, "c") {
		r = append(r, chart(n, wr("W1", bad("helm:test")), wr("W3", ok("helm:test"), bad("port:s:80:passive"))))
	}
	res := newFixture(r, r, nil).analyze(t)
	if got := cellOf(t, res, "c01", "W1", "helm:test").Status; got != StExcluded {
		t.Fatalf("helm:test in W1: %s", got)
	}
	// the port class differs in W3 for every chart, so it is universal there too
	if got := cellOf(t, res, "c01", "W3", "port:s:80:passive").Status; got != StExcluded {
		t.Fatalf("ports in W3: %s", got)
	}
	// workload is not one of the four classes the rule names
	r2 := []ChartReport{}
	for _, n := range names(10, "c") {
		r2 = append(r2, chart(n, wr("W1", bad(wk))))
	}
	if got := cellOf(t, newFixture(r2, r2, nil).analyze(t), "c01", "W1", wk).Status; got != StUnresolved {
		t.Fatalf("workload class: %s", got)
	}
}

func TestWorldUniversalCauseKey(t *testing.T) {
	// ten charts flagged by the check of "protocol" in W3; nine have a held cell credited to it
	build := func(credited int) *fixture {
		var r []ChartReport
		var rows []ledger.Row
		var st []StaticResult
		for i, n := range names(10, "c") {
			if i < credited {
				r = append(r, chart(n, wr("W3", bad(wk))))
				rows = append(rows, ledgerOpen(n, "W3", wk, "protocol"))
			} else {
				r = append(r, chart(n, wr("W3", ok(wk))))
			}
			st = append(st, stat(n, "W3", "protocol", true))
		}
		f := newFixture(r, r, nil)
		f.in.Ledger.Rows = rows
		f.in.Static, f.in.StaticGiven = st, true
		return f
	}
	res := build(9).analyze(t)
	if c := cellOf(t, res, "c01", "W3", wk); c.Status != StExcluded || c.Reasons[0] != ledger.ReasonWorldUniversal {
		t.Fatalf("%+v", c)
	}
	res = build(8).analyze(t)
	if c := cellOf(t, res, "c01", "W3", wk); c.Status != StUnresolved {
		t.Fatalf("%+v", c)
	}
}

func ledgerOpen(chart, world, key, cause string) ledger.Row {
	return ledger.Row{Chart: chart, World: world, Key: key, Cause: cause, Status: ledger.StatusUnresolved}
}

func TestCOptout(t *testing.T) {
	const testKey = "helm:test"
	mk := func(meta string, recs bool, asserted bool) *fixture {
		r := []ChartReport{chart("podinfo", wr("W1", bad(testKey)))}
		f := newFixture(r, r, nil)
		f.pad(9)
		if recs {
			pm := PodMesh{Pod: "podinfo-test", Owner: "Pod/podinfo-test"}
			if meta != "" {
				pm.MeshMetadata = []struct {
					Kind  string `json:"kind"`
					Key   string `json:"key"`
					Value string `json:"value"`
				}{{"annotation", "sidecar.istio.io/inject", meta}}
			}
			client := PodMesh{Pod: "client", Client: true}
			f.in.Runs[0].HasRecords, f.in.Runs[1].HasRecords = true, true
			f.in.Runs[0].Records = []Record{{World: "W1", Chart: "podinfo", Run: 1, PodMesh: []PodMesh{pm, client}}}
			f.in.Runs[1].Records = f.in.Runs[0].Records
		}
		if asserted {
			f.in.Ledger.Rows = []ledger.Row{{Chart: "podinfo", World: "W1", Key: testKey, Status: ledger.StatusExcluded,
				Exclusion: &ledger.Exclusion{Reason: ledger.ReasonCOptout, Evidence: "test pods opt out", Callers: []string{"podinfo-test"}},
				Reviewer:  &ledger.Reviewer{Verdict: ledger.VerdictAgree, Run: "rv"}}}
		}
		return f
	}
	t.Run("asserted with podMesh evidence", func(t *testing.T) {
		res := mk("false", true, true).analyze(t)
		c := cellOf(t, res, "podinfo", "W1", testKey)
		if c.Status != StExcluded || c.Reasons[0] != ledger.ReasonCOptout {
			t.Fatalf("%+v", c)
		}
		if len(res.OptOut) != 1 || res.OptOut[0].Effective != 2 || !res.OptOut[0].Asserted {
			t.Fatalf("%+v", res.OptOut)
		}
	})
	t.Run("not asserted stays open, evidence still printed", func(t *testing.T) {
		res := mk("false", true, false).analyze(t)
		if c := cellOf(t, res, "podinfo", "W1", testKey); c.Status != StUnresolved {
			t.Fatalf("%+v", c)
		}
		if len(res.OptOut) != 1 || res.OptOut[0].Asserted {
			t.Fatalf("%+v", res.OptOut)
		}
	})
	t.Run("asserted without records", func(t *testing.T) {
		if msg := mk("", false, true).analyzeErr(t); !strings.Contains(msg, "c-optout asserted") || !strings.Contains(msg, "podinfo/W1/helm:test") {
			t.Fatal(msg)
		}
	})
	t.Run("asserted but no pod opts out", func(t *testing.T) {
		if msg := mk("", true, true).analyzeErr(t); !strings.Contains(msg, "does not carry the opt-out") {
			t.Fatal(msg)
		}
	})
	t.Run("opt-out value true is not an opt-out", func(t *testing.T) {
		if msg := mk("true", true, true).analyzeErr(t); !strings.Contains(msg, "does not carry the opt-out") {
			t.Fatal(msg)
		}
	})
	t.Run("ambient opt-out is another label", func(t *testing.T) {
		if isOptOut("W3", "sidecar.istio.io/inject", "false") || !isOptOut("W3", "istio.io/dataplane-mode", "none") ||
			isOptOut("W1", "istio.io/dataplane-mode", "none") || !isOptOut("W1", "sidecar.istio.io/inject", "false") {
			t.Fatal("opt-out per world")
		}
	})
}

func TestMetrics(t *testing.T) {
	// helper: n cells, each its own chart/publisher unless pubs given
	type spec struct {
		chart, cause, typ string
		pub               string
	}
	run := func(specs []spec, mod func(rows []ledger.Row, f *fixture)) *Result {
		var r []ChartReport
		pubs := map[string]string{}
		var rows []ledger.Row
		for _, s := range specs {
			r = append(r, chart(s.chart, wr("W3", bad(wk))))
			pubs[s.chart] = s.pub
			rw := row(s.chart, "W3", wk, s.cause, s.typ)
			rw.Publisher = s.pub
			rows = append(rows, rw)
		}
		f := newFixture(r, r, pubs)
		f.pad(10)
		if mod != nil {
			mod(rows, f)
		}
		f.withCerts(rows...)
		return f.analyze(t)
	}
	t.Run("ambient-np is left out of every metric", func(t *testing.T) {
		res := run([]spec{{"a", AmbientNP, "defect", "p1"}, {"b", AmbientNP, "defect", "p2"}, {"c", "protocol", "gap", "p3"}}, nil)
		if res.Metrics != (Metrics{N: 1, P: 1}) {
			t.Fatalf("%v", res.Metrics)
		}
		if res.Prevalence.Cells != 2 || len(res.Prevalence.Charts) != 2 {
			t.Fatalf("%+v", res.Prevalence)
		}
	})
	t.Run("distinct keys and publishers", func(t *testing.T) {
		res := run([]spec{
			{"a", "protocol", "defect", "p1"}, {"b", "protocol", "defect", "p2"}, {"c", "protocol", "gap", "p2"},
			{"d", "uid-1337", "gap", "p1"}, {"e", "bind-address", "defect", "p3"},
		}, nil)
		want := Metrics{N: 3, P: 3, D: 2, PD: 3}
		if res.Metrics != want {
			t.Fatalf("%v, want %v", res.Metrics, want)
		}
		if res.Region.Region != "A" {
			t.Fatalf("region %s", res.Region.Region)
		}
	})
	t.Run("a key is a defect key when any counted finding is a defect", func(t *testing.T) {
		res := run([]spec{{"a", "protocol", "gap", "p1"}, {"b", "protocol", "defect", "p2"}}, nil)
		if res.Metrics.D != 1 || res.Metrics.PD != 1 {
			t.Fatalf("%v", res.Metrics)
		}
	})
	t.Run("reviewer disagree-type makes a defect a gap", func(t *testing.T) {
		res := run([]spec{{"a", "protocol", "defect", "p1"}}, func(rows []ledger.Row, f *fixture) {
			rows[0].Reviewer.Verdict = ledger.VerdictDisagreeType
		})
		if res.Metrics.D != 0 || res.Metrics.PD != 0 || res.Metrics.N != 1 {
			t.Fatalf("%v", res.Metrics)
		}
		if c := cellOf(t, res, "a", "W3", wk); c.Type != ledger.TypeGap {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("a new key counts like a frozen one, from the ledger's newKeys", func(t *testing.T) {
		res := run([]spec{{"a", "tls-origination", "gap", "p1"}}, func(rows []ledger.Row, f *fixture) {
			f.in.Ledger.NewKeys = []ledger.NewKey{{Key: "tls-origination"}}
		})
		if res.Metrics.N != 1 {
			t.Fatalf("%v", res.Metrics)
		}
	})
	t.Run("not counted rows do not count", func(t *testing.T) {
		res := run([]spec{{"a", "protocol", "defect", "p1"}, {"b", "uid-1337", "defect", "p2"}}, func(rows []ledger.Row, f *fixture) {
			rows[1].Status = ledger.StatusUnexplain
		})
		if res.Metrics.N != 1 || len(res.Unexplained) != 1 {
			t.Fatalf("%v %v", res.Metrics, res.Unexplained)
		}
	})
}

// staticFixture: `same` charts that were same in W3, a counted finding of
// protocol in chart "hit", flagged or not, and `flaggedSame` of the same charts flagged.
func staticFixture(same, flaggedSame int, hitFlagged bool) *fixture {
	r := []ChartReport{chart("hit", wr("W3", bad(wk)))}
	var st []StaticResult
	st = append(st, stat("hit", "W3", "protocol", hitFlagged))
	for i, n := range names(same, "s") {
		r = append(r, chart(n, wr("W3", ok(wk))))
		st = append(st, stat(n, "W3", "protocol", i < flaggedSame))
	}
	f := newFixture(r, r, nil)
	rw := row("hit", "W3", wk, "protocol", "gap")
	f.withCerts(rw)
	f.in.Static, f.in.StaticGiven = st, true
	return f
}

func TestStaticCheckS(t *testing.T) {
	cases := []struct {
		name        string
		same, flag  int
		hitFlagged  bool
		qualifies   bool
		wantS       int
		wantFlagged int
	}{
		{"flags the finding, 1 of 10 same charts (10%)", 10, 1, true, true, 1, 1},
		{"flags the finding, 2 of 10 (20%)", 10, 2, true, false, 0, 2},
		{"flags the finding, 1 of 9 (11%)", 9, 1, true, false, 0, 1},
		{"flags the finding, 1 of 11 (9%)", 11, 1, true, true, 1, 1},
		{"misses the finding", 10, 0, false, false, 0, 0},
		{"flags nothing at all", 10, 0, true, true, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := staticFixture(tc.same, tc.flag, tc.hitFlagged).analyze(t)
			if res.Metrics.S != tc.wantS || !res.SComputed {
				t.Fatalf("S %d, want %d (%+v)", res.Metrics.S, tc.wantS, res.Static)
			}
			if len(res.Static) != 1 || res.Static[0].Qualifies != tc.qualifies || res.Static[0].Worlds[0].FlaggedSame != tc.wantFlagged || res.Static[0].Worlds[0].SameCharts != tc.same {
				t.Fatalf("%+v", res.Static)
			}
		})
	}
	t.Run("a chart that was not same in that world is not in the 10%", func(t *testing.T) {
		f := staticFixture(9, 0, true)
		f.in.Runs[1].Report.Charts[1].Worlds[0] = wr("W3", fk(wk, "true", "false", "true", "false")) // flaky in run 2: not same
		f.in.Static[1].Flagged = true
		res := f.analyze(t)
		if res.Static[0].Worlds[0].SameCharts != 8 || res.Static[0].Worlds[0].FlaggedSame != 0 {
			t.Fatalf("%+v", res.Static[0].Worlds)
		}
	})
	t.Run("static results must cover the counted cell", func(t *testing.T) {
		f := staticFixture(2, 0, true)
		f.in.Static = f.in.Static[1:]
		if msg := f.analyzeErr(t); !strings.Contains(msg, `lack check "protocol" for hit/W3`) {
			t.Fatal(msg)
		}
	})
	t.Run("static results must cover the same charts", func(t *testing.T) {
		f := staticFixture(2, 0, true)
		f.in.Static = f.in.Static[:2]
		if msg := f.analyzeErr(t); !strings.Contains(msg, "a chart that was same in W3") {
			t.Fatal(msg)
		}
	})
	t.Run("a check that does not apply to the world cannot flag the finding", func(t *testing.T) {
		f := staticFixture(2, 0, true)
		na := false
		for i := range f.in.Static {
			f.in.Static[i].Applicable = &na
			f.in.Static[i].Flagged = true // meaningless when not applicable
		}
		res := f.analyze(t)
		if res.Metrics.S != 0 {
			t.Fatalf("S %d: a check that does not apply to W3 cannot count in W3", res.Metrics.S)
		}
		sk := res.Static[0]
		if len(sk.NotApplicable) != 1 || len(sk.Unflagged) != 0 || sk.FlagsAll || !sk.Worlds[0].NotApplicable {
			t.Fatalf("not applicable is its own state, not unflagged: %+v", sk)
		}
	})
	t.Run("causes.json worlds make a check not applicable even when the file says it applies", func(t *testing.T) {
		f := staticFixture(2, 0, true)
		c, _ := ledger.ParseCauses([]byte(`{"keys":[{"key":"ambient-np","worlds":["W3"],"staticCheck":"x"},{"key":"protocol","worlds":["W1"],"staticCheck":"protocol"}]}`))
		f.in.Causes = c
		res := f.analyze(t)
		if res.Metrics.S != 0 || len(res.Static[0].NotApplicable) != 1 {
			t.Fatalf("%v %+v", res.Metrics, res.Static)
		}
	})
	t.Run("not applicable same charts are not flagged same charts", func(t *testing.T) {
		// the check applies in W3; only the 10% test counts flagged charts
		f := staticFixture(10, 1, true)
		if res := f.analyze(t); res.Metrics.S != 1 || res.Static[0].Worlds[0].FlaggedSame != 1 {
			t.Fatalf("%v %+v", res.Metrics, res.Static)
		}
	})
	t.Run("a key without a static check is never in S and needs no static rows", func(t *testing.T) {
		r := []ChartReport{chart("h0", wr("W3", bad(wk)))}
		for _, n := range names(10, "s") {
			r = append(r, chart(n, wr("W3", ok(wk))))
		}
		f := newFixture(r, r, nil)
		f.withCerts(row("h0", "W3", wk, "bind-address", "gap"))
		f.in.Static, f.in.StaticGiven = []StaticResult{}, true
		res := f.analyze(t)
		var found bool
		for _, sk := range res.Static {
			if sk.Key == "bind-address" {
				found = sk.NoCheck && !sk.Qualifies
			}
		}
		if !found || res.Metrics.S != 0 {
			t.Fatalf("%v %+v", res.Metrics, res.Static)
		}
	})
	t.Run("without a static file S is 0 and says so", func(t *testing.T) {
		f := staticFixture(2, 0, true)
		f.in.Static, f.in.StaticGiven = nil, false
		res := f.analyze(t)
		if res.SComputed || res.Metrics.S != 0 || !strings.Contains(strings.Join(res.Warnings, "|"), "S is 0") {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("S reaches region B", func(t *testing.T) {
		var r []ChartReport
		var st []StaticResult
		var rows []ledger.Row
		for i, cause := range []string{"protocol", "uid-1337"} {
			n := fmt.Sprintf("h%d", i)
			r = append(r, chart(n, wr("W3", bad(wk))))
			st = append(st, stat(n, "W3", cause, true))
			rows = append(rows, row(n, "W3", wk, cause, "gap"))
		}
		f := newFixture(r, r, nil)
		f.withCerts(rows...)
		f.in.Static, f.in.StaticGiven = st, true
		res := f.analyze(t)
		if res.Metrics != (Metrics{N: 2, P: 2, S: 2}) || res.Region.Region != "B" {
			t.Fatalf("%v %s", res.Metrics, res.Region.Region)
		}
	})
}

func TestFutility(t *testing.T) {
	build := func(counted, open, undecided int) *Result {
		var r []ChartReport
		var rows []ledger.Row
		for i := 0; i < counted; i++ {
			n := fmt.Sprintf("k%02d", i)
			r = append(r, chart(n, wr("W3", bad(wk))))
			rows = append(rows, row(n, "W3", wk, []string{"protocol", "uid-1337", "bind-address", "reserved-port"}[i], "gap"))
		}
		var r2 []ChartReport
		r2 = append(r2, r...)
		for i := 0; i < open; i++ {
			n := fmt.Sprintf("o%02d", i)
			r = append(r, chart(n, wr("W3", bad(wk))))
			r2 = append(r2, chart(n, wr("W3", bad(wk))))
		}
		for i := 0; i < undecided; i++ {
			n := fmt.Sprintf("u%02d", i)
			r = append(r, chart(n, wr("W3", bad(wk))))
			r2 = append(r2, chart(n, uw("W3", ledger.ReasonUndecided, bad(wk))))
		}
		f := newFixture(r, r2, nil)
		f.pad(10)
		f.withCerts(rows...)
		return f.analyze(t)
	}
	t.Run("N_max is counted new keys plus open cells, undecided included", func(t *testing.T) {
		res := build(1, 2, 3)
		fu := res.Futility
		if fu.PendingCells != 5 || fu.NMax != 6 {
			t.Fatalf("%+v", fu)
		}
	})
	t.Run("N_max at 1 stops", func(t *testing.T) {
		res := build(1, 0, 0)
		if !res.Futility.StopDrop || !res.Futility.OnlyE || res.Futility.NMax != 1 {
			t.Fatalf("%+v", res.Futility)
		}
	})
	t.Run("N_max at 2 does not stop, but A is out of reach", func(t *testing.T) {
		res := build(1, 1, 0)
		fu := res.Futility
		if fu.StopDrop || fu.NMax != 2 || strings.Join(fu.Reachable, "") != "BE" || !fu.PrevalenceCertificatesOff {
			t.Fatalf("%+v", fu)
		}
	})
	t.Run("pending cells in the same publisher add one P_max", func(t *testing.T) {
		var r []ChartReport
		pubs := map[string]string{}
		for _, n := range []string{"a", "b", "c"} {
			r = append(r, chart(n, wr("W3", bad(wk))))
			pubs[n] = "same-pub"
		}
		res := newFixture(r, r, pubs).analyze(t)
		if res.Futility.NMax != 3 || res.Futility.PMax != 1 || !res.Futility.StopDrop {
			t.Fatalf("%+v", res.Futility)
		}
	})
	t.Run("ambient-np cells that are counted add nothing", func(t *testing.T) {
		r := []ChartReport{chart("a", wr("W3", bad(wk)))}
		f := newFixture(r, r, nil)
		f.pad(10)
		f.withCerts(row("a", "W3", wk, AmbientNP, "defect"))
		res := f.analyze(t)
		if res.Futility.NMax != 0 || res.Metrics.N != 0 {
			t.Fatalf("%+v", res.Futility)
		}
	})
	t.Run("excluded cells other than undecided add nothing", func(t *testing.T) {
		r := []ChartReport{chart("a", wr("W3", bad(wk)))}
		r2 := []ChartReport{chart("a", uw("W3", ledger.ReasonPullFailure, bad(wk)))}
		res := newFixture(r, r2, nil).analyze(t)
		if res.Futility.NMax != 0 || res.Futility.PendingCells != 0 {
			t.Fatalf("%+v", res.Futility)
		}
	})
}

func TestUndecidedShareBoundary(t *testing.T) {
	build := func(undecided, total int) *Result {
		var r, r2 []ChartReport
		for i := 0; i < total; i++ {
			n := fmt.Sprintf("c%02d", i)
			r = append(r, chart(n, wr("W3", bad(wk))))
			if i < undecided {
				r2 = append(r2, chart(n, uw("W3", ledger.ReasonUndecided, bad(wk))))
			} else {
				r2 = append(r2, chart(n, wr("W3", bad(wk))))
			}
		}
		return newFixture(r, r2, nil).analyze(t)
	}
	cases := []struct {
		u, n int
		over bool
	}{
		{3, 20, false}, // exactly 15%
		{4, 20, true},  // 20%
		{3, 19, true},  // 15.8%
		{0, 20, false},
		{7, 47, false}, // 14.9%
		{8, 53, true},  // hmm 15.09%
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d of %d", tc.u, tc.n), func(t *testing.T) {
			got := build(tc.u, tc.n).Undecided
			if got.Undecided != tc.u || got.Cells != tc.n || got.Over != tc.over {
				t.Fatalf("%+v", got)
			}
		})
	}
	if got := newFixture([]ChartReport{chart("a", wr("W3", ok(wk)))}, []ChartReport{chart("a", wr("W3", ok(wk)))}, nil).analyze(t).Undecided; got.Over || got.Cells != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestIfAllCounted(t *testing.T) {
	// three unresolved cells of three charts and publishers, no ledger rows:
	// each would be its own new key of unknown type
	var r []ChartReport
	for _, n := range []string{"a", "b", "c"} {
		r = append(r, chart(n, wr("W3", bad(wk))))
	}
	res := newFixture(r, r, nil).analyze(t)
	if res.Metrics.N != 0 || res.Region.Region != "E" || len(res.Unresolved) != 3 {
		t.Fatalf("%v %s", res.Metrics, res.Region.Region)
	}
	if res.IfAllCounted.Metrics != (Metrics{N: 3, P: 3}) || res.IfAllCounted.Region.Region != "C" || res.IfAllCounted.Cells != 3 {
		t.Fatalf("%+v", res.IfAllCounted)
	}
	// a ledger row with a cause and a defect type feeds D
	f := newFixture(r, r, nil)
	rows := []ledger.Row{ledgerOpen("a", "W3", wk, "protocol"), ledgerOpen("b", "W3", wk, "uid-1337")}
	rows[0].Type, rows[1].Type = "defect", "defect"
	f.in.Ledger.Rows = rows
	res = f.analyze(t)
	if res.IfAllCounted.Metrics.D != 2 || res.IfAllCounted.Metrics.PD != 2 || res.IfAllCounted.Region.Region != "A" {
		t.Fatalf("%+v", res.IfAllCounted)
	}
	if res.Region.Region != "E" {
		t.Fatal("the decision uses counted findings only")
	}
}

func TestDisagreements(t *testing.T) {
	base := func() (*fixture, *ledger.Row) {
		r := []ChartReport{chart("a", wr("W3", bad(wk)))}
		f := newFixture(r, r, nil)
		f.pad(10)
		rw := row("a", "W3", wk, "protocol", "gap")
		f.withCerts(rw)
		return f, &f.in.Ledger.Rows[0]
	}
	if f, _ := base(); f.analyze(t).Metrics.N != 1 {
		t.Fatal("base fixture does not count")
	}
	cases := []struct {
		name string
		mod  func(f *fixture, r *ledger.Row)
		want string
	}{
		{"counted but differs in run 1 only", func(f *fixture, r *ledger.Row) {
			f.in.Runs[1].Report.Charts[0] = chart("a", wr("W3", ok(wk)))
		}, "does not differ in both runs"},
		{"counted but flaky", func(f *fixture, r *ledger.Row) {
			f.in.Runs[1].Report.Charts[0] = chart("a", wr("W3", fk(wk, "true", "false", "true", "false")))
		}, "excluded by flaky"},
		{"counted but only run 1 given", func(f *fixture, r *ledger.Row) {
			f.in.Runs = f.in.Runs[:1]
		}, "only run 1 is given"},
		{"row for a cell nobody saw", func(f *fixture, r *ledger.Row) { r.Key = "workload:Deployment/zz:ready" }, "not a finding in any run"},
		{"run 1 id differs", func(f *fixture, r *ledger.Row) { r.Runs.Run1 = "other" }, "runs.run1 is other"},
		{"run 2 id differs", func(f *fixture, r *ledger.Row) { r.Runs.Run2 = "other" }, "runs.run2 is other"},
		{"publisher differs from the frame", func(f *fixture, r *ledger.Row) { r.Publisher = "someone-else" }, `the frame says "pub-a"`},
		{"certification run unknown", func(f *fixture, r *ledger.Row) { r.Runs.Certification = []string{"c1", "c9"} }, "certification run c9 is not in the manifest"},
		{"fix entry missing from the run", func(f *fixture, r *ledger.Row) { r.Fix.Entry = "nope" }, `fix entry "nope" is not in certification run`},
		{"fixOf is another chart", func(f *fixture, r *ledger.Row) {
			f.in.Certification[0].Report.Charts[0].FixOf = "other"
		}, `has fixOf "other"`},
		{"certificate absent", func(f *fixture, r *ledger.Row) {
			f.in.Certification[1].Report.Charts[0].Certificate = nil
		}, "has no certificate"},
		{"certificate not earned", func(f *fixture, r *ledger.Row) {
			c := f.in.Certification[1].Report.Charts[0].Certificate
			c.Certified, c.Problems = false, []string{"W1 still differs"}
		}, "not earned: W1 still differs"},
		{"certificate covers other worlds", func(f *fixture, r *ledger.Row) {
			f.in.Certification[0].Report.Charts[0].Certificate.CellWorlds = []string{"W1"}
		}, "covers worlds"},
		{"world not cleared", func(f *fixture, r *ledger.Row) {
			f.in.Certification[0].Report.Charts[0].Certificate.Cleared = nil
		}, "does not list W3 as cleared"},
		{"certificate ignored this cell", func(f *fixture, r *ledger.Row) {
			f.in.Certification[0].Report.Charts[0].Certificate.Ignored = []IgnoreCell{{Chart: "a", World: "W3", Key: wk, Reason: "flaky"}}
		}, "ignored this very cell"},
		{"certificate passed the world uncompared", func(f *fixture, r *ledger.Row) {
			f.in.Certification[0].Report.Charts[0].Certificate.WorldsNotCompared = []IgnoreCell{{Chart: "a", World: "W3", Key: "*", Reason: "x"}}
		}, "without comparing it"},
		{"recheck report does not show it", func(f *fixture, r *ledger.Row) {
			f.in.Recheck = &CertInput{ID: "rc", Report: report(chart("a", wr("W3", ok(wk))))}
		}, "recheck report does not show the cell differing"},
		{"recheck run id differs", func(f *fixture, r *ledger.Row) {
			f.in.Recheck = &CertInput{ID: "zz", Report: report(chart("a", wr("W3", bad(wk))))}
		}, "recheck.run is rc"},
		{"recheck version differs", func(f *fixture, r *ledger.Row) {
			f.in.Recheck = &CertInput{ID: "rc", Istio: "1.31.9", Report: report(chart("a", wr("W3", bad(wk))))}
		}, "recheck.version is 1.32.0"},
		{"excluded as flaky but the reports say differs", func(f *fixture, r *ledger.Row) {
			*r = ledger.Row{Chart: "a", World: "W3", Key: wk, Status: ledger.StatusExcluded, Exclusion: &ledger.Exclusion{Reason: ledger.ReasonFlaky, Evidence: "x"}}
		}, "excluded as flaky, the reports do not show it"},
		{"excluded as world-universal but it is not", func(f *fixture, r *ledger.Row) {
			*r = ledger.Row{Chart: "a", World: "W3", Key: wk, Status: ledger.StatusExcluded, Exclusion: &ledger.Exclusion{Reason: ledger.ReasonWorldUniversal, Evidence: "x"}}
		}, "excluded as world-universal"},
		{"excluded as pull-failure but no run was left out", func(f *fixture, r *ledger.Row) {
			*r = ledger.Row{Chart: "a", World: "W3", Key: wk, Status: ledger.StatusExcluded, Exclusion: &ledger.Exclusion{Reason: ledger.ReasonPullFailure, Evidence: "x"}}
		}, "excluded as pull-failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, r := base()
			tc.mod(f, r)
			msg := f.analyzeErr(t)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("want %q in:\n%s", tc.want, msg)
			}
			if !strings.Contains(msg, "a/W3/") {
				t.Fatalf("error does not name the row: %s", msg)
			}
		})
	}
	t.Run("counted but world-universal", func(t *testing.T) {
		f := worldUniversalFixture(10, 10)
		rw := row("c01", "W1", "apiserver:svc:s:443:reachable", "apiserver-caller", "gap")
		f.withCerts(rw)
		if msg := f.analyzeErr(t); !strings.Contains(msg, "excluded by world-universal") {
			t.Fatal(msg)
		}
	})
	t.Run("a ledger that agrees leaves the ledger untouched", func(t *testing.T) {
		f, _ := base()
		before := fmt.Sprintf("%+v", f.in.Ledger)
		f.analyze(t)
		if fmt.Sprintf("%+v", f.in.Ledger) != before {
			t.Fatal("crossrun changed the ledger")
		}
	})
}

func TestDeterministic(t *testing.T) {
	build := func(order []string) []byte {
		var r []ChartReport
		for _, n := range order {
			r = append(r, chart(n, wr("W3", bad(wk), bad("helm:install")), wr("W1", bad("helm:test"))))
		}
		f := newFixture(r, r, nil)
		f.pad(3)
		f.in.FrameOrder = append(append([]string{}, order...), names(3, "z")...)
		res := f.analyze(t)
		b, err := RenderJSON(res)
		if err != nil {
			t.Fatal(err)
		}
		return append(b, RenderMarkdown(res)...)
	}
	a := build([]string{"a", "b", "c", "d"})
	b := build([]string{"d", "c", "b", "a"})
	if string(a) != string(b) {
		t.Fatal("output depends on input order")
	}
	if string(a) != string(build([]string{"a", "b", "c", "d"})) {
		t.Fatal("output differs between runs")
	}
}

func TestLedgerExcludedRowsAreConfirmedByReports(t *testing.T) {
	d := fk(wk, "true", "false", "false", "false")
	f := newFixture([]ChartReport{chart("a", wr("W3", d))}, []ChartReport{chart("a", uw("W3", ledger.ReasonPullFailure, d))}, nil)
	f.in.Ledger.Rows = []ledger.Row{{Chart: "a", World: "W3", Key: wk, Status: ledger.StatusExcluded,
		Exclusion: &ledger.Exclusion{Reason: ledger.ReasonPullFailure, Evidence: "docker.io/x refused"}}}
	c := cellOf(t, f.analyze(t), "a", "W3", wk)
	if c.Status != StExcluded || c.Reasons[0] != ledger.ReasonPullFailure {
		t.Fatalf("%+v", c)
	}
}
