package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

var t0 = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

const wk = "workload:Deployment/x:ready"

func classOfKey(k string) string {
	if i := strings.Index(k, ":"); i >= 0 {
		return k[:i]
	}
	return k
}

func sp(s string) *string { return &s }

// fk is a key report: differs when every value differs from the oracle, flaky
// when some do, same when none.
func fk(key, oracle string, vals ...string) KeyReport {
	miss := 0
	for _, v := range vals {
		if v != oracle {
			miss++
		}
	}
	verdict := "same"
	switch {
	case miss == len(vals) && miss > 0:
		verdict = "differs"
	case miss > 0:
		verdict = "flaky"
	}
	return KeyReport{Key: key, Class: classOfKey(key), Verdict: verdict, OracleValue: sp(oracle), WorldValues: vals, Misses: miss}
}

// bad is a differing key; ok a matching one.
func bad(key string) KeyReport { return fk(key, "true", "false", "false", "false") }
func ok(key string) KeyReport  { return fk(key, "true", "true", "true", "true") }

func wr(world string, keys ...KeyReport) WorldReport {
	v := "same"
	for _, k := range keys {
		if k.Verdict == "flaky" && v == "same" {
			v = "flaky"
		}
		if k.Verdict == "differs" {
			v = "differs"
		}
	}
	return WorldReport{World: world, Verdict: v, RunsUsed: []int{1, 2, 3}, OracleRunsUsed: []int{1, 2, 3}, Keys: keys}
}

// uw is a world with two of three runs left, undecided for the given kind
// (pull-failure, capacity, undecided). Every key is undecided; keys given as
// bad(...) keep their values for the evidence test.
func uw(world, kind string, keys ...KeyReport) WorldReport {
	w := WorldReport{World: world, Verdict: "undecided", UndecidedReason: world + " has 2 of 3 runs", RunsUsed: []int{1, 2}, OracleRunsUsed: []int{1, 2, 3}}
	switch kind {
	case ledger.ReasonPullFailure:
		w.RunsLeftOut = []LeftOut{{Run: 3, Reason: "pull-failure", Detail: "docker.io/x"}}
	case ledger.ReasonCapacity:
		w.RunsLeftOut = []LeftOut{{Run: 3, Reason: "capacity", Detail: "pod: Insufficient cpu"}}
	}
	for _, k := range keys {
		k.Verdict = "undecided"
		k.WorldValues = k.WorldValues[:2]
		k.Misses = 0
		for _, v := range k.WorldValues {
			if k.OracleValue != nil && v != *k.OracleValue {
				k.Misses++
			}
		}
		w.Keys = append(w.Keys, k)
	}
	return w
}

// chart is a chart report; a required world the caller did not give is added
// as a world without keys (the sample must be complete, sample.go).
func chart(name string, worlds ...WorldReport) ChartReport {
	for _, req := range requiredWorlds {
		have := false
		for _, w := range worlds {
			if w.World == req {
				have = true
			}
		}
		if !have {
			worlds = append(worlds, wr(req))
		}
	}
	return ChartReport{Chart: name, ChartVersion: "1.0.0", Worlds: worlds, OracleInstall: OracleInstall{Runs: 3, Ok: 3, Status: "ok"}}
}

func report(charts ...ChartReport) *Report {
	r := &Report{Schema: 1, Oracle: "W0", Worlds: []string{"W1", "W3"}, RunsRequired: 3}
	data, err := json.Marshal(charts)
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(data, &r.Charts); err != nil { // a deep copy: tests edit runs apart
		panic(err)
	}
	return r
}

// fixEntry is a certified fix entry of base, clearing world.
func fixEntry(fix, base, world string, keys ...KeyReport) ChartReport {
	c := chart(fix, wr(world, keys...))
	c.FixOf = base
	c.Certificate = &CertReport{Fix: fix, Base: base, Certified: true, Cleared: []string{world}, CellWorlds: []string{world},
		Problems: []string{}, Ignored: []IgnoreCell{}, WorldsNotCompared: []IgnoreCell{}}
	return c
}

type fixture struct {
	in *Inputs
}

// newFixture builds Inputs with run 1 and run 2 (twelve hours apart),
// publishers pub-<chart> unless pubs overrides, no ledger rows.
func newFixture(r1, r2 []ChartReport, pubs map[string]string) *fixture {
	in := &Inputs{
		Causes:    mustCauses(),
		Ledger:    &ledger.File{Schema: ledger.Schema},
		Publisher: map[string]string{}, Qualified: map[string]bool{},
	}
	add := func(cs []ChartReport) {
		for _, c := range cs {
			if c.FixOf != "" {
				continue
			}
			if _, ok := in.Publisher[c.Chart]; !ok {
				p := "pub-" + c.Chart
				if v, ok := pubs[c.Chart]; ok {
					p = v
				}
				in.Publisher[c.Chart] = p
				in.Qualified[c.Chart] = true
				in.FrameOrder = append(in.FrameOrder, c.Chart)
			}
		}
	}
	add(r1)
	add(r2)
	in.Runs = []RunInput{{ID: "r1", Started: t0, Ended: t0.Add(time.Hour), Report: report(r1...)}}
	if r2 != nil {
		in.Runs = append(in.Runs, RunInput{ID: "r2", Started: t0.Add(13 * time.Hour), Ended: t0.Add(14 * time.Hour), Report: report(r2...)})
	}
	return &fixture{in}
}

// mustCauses is causes.json's shape with every key's static check named after
// the key and applying in both mesh worlds, except bind-address (null, as in
// the real file); apiserver-caller carries needsNewKeyRule, as in the real file. The real file is read by TestRealCausesFile.
func mustCauses() *ledger.Causes {
	var items []string
	for _, k := range []string{"ambient-np", "protocol", "bind-address", "uid-1337", "reserved-port", "startup-order", "job-lifecycle", "apiserver-caller", "ambient-l7"} {
		sc := `"` + k + `"`
		if k == "bind-address" {
			sc = "null"
		}
		flag := ""
		if k == "apiserver-caller" {
			flag = `,"needsNewKeyRule":true` // as in the real file
		}
		items = append(items, `{"key":"`+k+`","worlds":["W1","W3"],"staticCheck":`+sc+flag+`}`)
	}
	c, err := ledger.ParseCauses([]byte(`{"keys":[` + strings.Join(items, ",") + `]}`))
	if err != nil {
		panic(err)
	}
	return c
}

// stat is a static result row for an applicable check.
func stat(chart, world, check string, flagged bool) StaticResult {
	app := true
	return StaticResult{Chart: chart, World: world, Check: check, Applicable: &app, Flagged: flagged}
}

// row is a counted ledger row consistent with fixEntry certificates in runs c1, c2.
func row(chart, world, key, cause, typ string) ledger.Row {
	return ledger.Row{
		Chart: chart, World: world, Key: key, Cause: cause, Type: typ,
		TypeBasis: &ledger.TypeBasis{Doc: "d"},
		Fix:       &ledger.Fix{Entry: chart + "-fix-" + cause, Form: ledger.FormValues, HelmLint: true},
		Runs:      &ledger.Runs{Run1: "r1", Run2: "r2", Certification: []string{"c1", "c2"}},
		Recheck:   &ledger.Recheck{Version: "1.32.0", Result: ledger.RecheckReproduces, Run: "rc"},
		Upstream:  &ledger.Upstream{},
		Reviewer:  &ledger.Reviewer{Verdict: ledger.VerdictAgree, Run: "rv"},
		Status:    ledger.StatusCounted, Publisher: "pub-" + chart,
	}
}

// withCerts adds certification runs c1 and c2 certifying each row's fix.
func (f *fixture) withCerts(rows ...ledger.Row) {
	var cs []ChartReport
	for _, r := range rows {
		if r.Fix != nil {
			cs = append(cs, fixEntry(r.Fix.Entry, r.Chart, r.World, ok(r.Key)))
		}
	}
	f.in.Certification = []CertInput{{ID: "c1", Report: report(cs...)}, {ID: "c2", Report: report(cs...)}}
	f.in.Ledger.Rows = append(f.in.Ledger.Rows, rows...)
}

func (f *fixture) analyze(t *testing.T) *Result {
	t.Helper()
	res, err := Analyze(f.in)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return res
}

func (f *fixture) analyzeErr(t *testing.T) string {
	t.Helper()
	_, err := Analyze(f.in)
	if err == nil {
		t.Fatal("Analyze succeeded, want an error")
	}
	return err.Error()
}

func cellOf(t *testing.T, r *Result, chart, world, key string) CellResult {
	t.Helper()
	for _, c := range r.Cells {
		if c.Chart == chart && c.World == world && c.Key == key {
			return c
		}
	}
	t.Fatalf("no cell %s/%s/%s in %d cells", chart, world, key, len(r.Cells))
	return CellResult{}
}

func names(n int, prefix string) []string {
	var out []string
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("%s%02d", prefix, i))
	}
	return out
}

// pad adds n qualified charts that are the same in both runs in W1 and W3.
func (f *fixture) pad(n int) {
	for _, name := range names(n, "z") {
		f.in.Publisher[name] = "pub-" + name
		f.in.Qualified[name] = true
		f.in.FrameOrder = append(f.in.FrameOrder, name)
		for i := range f.in.Runs {
			f.in.Runs[i].Report.Charts = append(f.in.Runs[i].Report.Charts, chart(name, wr("W1", ok(wk), ok("helm:test")), wr("W3", ok(wk), ok("helm:test"))))
		}
	}
}

// rerunUndecided gives every chart that a run leaves with an undecided
// chart-world (and W0 3 of 3) the rerun the rule demands, showing the chart
// again exactly as the run had it: undecided again after the rerun, so the
// chart stays undecided and counts, as it did before reruns existed.
func (f *fixture) rerunUndecided() {
	for i := range f.in.Runs {
		for _, ch := range f.in.Runs[i].Report.Charts {
			if ch.FixOf != "" {
				continue
			}
			if w0, _ := w0Met(&ch, 3); !w0 || len(undecidedRequired(&ch)) == 0 {
				continue
			}
			rid := fmt.Sprintf("rr%d-%s", i+1, ch.Chart)
			start := f.in.Runs[i].Ended.Add(time.Hour)
			f.in.Reruns = append(f.in.Reruns, RunInput{ID: rid, Started: start, Ended: start.Add(30 * time.Minute), Report: report(ch)})
			f.in.RerunRefs = append(f.in.RerunRefs, RerunRef{Chart: ch.Chart, Run: f.in.Runs[i].ID, Rerun: rid})
		}
	}
}
