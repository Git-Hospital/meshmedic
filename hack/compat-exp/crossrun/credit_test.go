package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

// difftableReport runs the real difftable (hack/compat-pilot/difftable) over
// records written in observe.sh's shape and returns its -json report, as a
// certification run would store it. Nothing in it is typed by hand.
func difftableReport(t *testing.T, recs []map[string]any) *Report {
	t.Helper()
	dir := t.TempDir()
	for i, r := range recs {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("%s.%s.run%d.%d.json", r["chart"], r["world"], r["run"], i)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "run", "../../compat-pilot/difftable", "-json", "-worlds", "W1,W3", "-cell-worlds", "W3", dir)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("difftable: %v\n%s", err, errb.String())
	}
	var rep Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	return &rep
}

const (
	ck1 = "workload:Deployment/k1:ready"
	ck2 = "workload:Deployment/k2:ready"
	ck3 = "workload:Deployment/k3:ready"
)

// creditRecords are the records of chart a and one fix entry of it: W0 and W1
// behave, W3 shows the given value per key (true is W0's value).
func creditRecords(entry string, w3 map[string]string) []map[string]any {
	var out []map[string]any
	add := func(chart, fixOf string, world string, obs map[string]string) {
		for run := 1; run <= 3; run++ {
			out = append(out, map[string]any{"chart": chart, "chartVersion": "1.0.0", "fixOf": fixOf, "world": world, "run": run,
				"obs": obs, "environment": map[string]string{"istio": "1.31.3", "kubernetes": "1.36.0"}})
		}
	}
	good := map[string]string{ck1: "true", ck2: "true", ck3: "true"}
	bad := map[string]string{ck1: "false", ck2: "false", ck3: "false"}
	for _, w := range []string{"W0", "W1"} {
		add("a", "", w, good)
		if entry != "" {
			add(entry, "a", w, good)
		}
	}
	add("a", "", "W3", bad)
	if entry != "" {
		add(entry, "a", "W3", w3)
	}
	return out
}

// creditFixture builds the situation of rule 3 with certificates difftable
// issued. Chart a in W3 differs on k1, k2 and k3. Two keys' fixes are needed:
//   - the protocol part alone returns k1 to W0's value and leaves k2 and k3;
//   - the uid-1337 part alone returns k2 and leaves k1 and k3;
//   - only both together return k3, and the combined fix returns all three.
//
// So no part is certified on its own (each leaves a cell differing), and only
// the combined entry earns a certificate for any of the cells.
func creditFixture(t *testing.T) *fixture {
	t.Helper()
	w3with := func(keys ...string) map[string]string {
		m := map[string]string{ck1: "false", ck2: "false", ck3: "false"}
		for _, k := range keys {
			m[k] = "true"
		}
		return m
	}
	both := difftableReport(t, creditRecords("a-both", w3with(ck1, ck2, ck3)))
	proto := difftableReport(t, creditRecords("a-proto", w3with(ck1)))
	uid := difftableReport(t, creditRecords("a-uid", w3with(ck2)))
	for _, rep := range []*Report{proto, uid} {
		for _, ch := range rep.Charts {
			if ch.Certificate != nil && ch.Certificate.Certified {
				t.Fatalf("difftable certified the part %s, which leaves cells differing: %+v", ch.Chart, ch.Certificate)
			}
		}
	}
	if c := findChart(both, "a-both"); c == nil || c.Certificate == nil || !c.Certificate.Certified {
		t.Fatalf("difftable did not certify the combined fix: %+v", c)
	}
	r := []ChartReport{chart("a", wr("W3", bad(ck1), bad(ck2), bad(ck3)))}
	f := newFixture(r, r, nil)
	f.pad(10)
	f.in.Certification = []CertInput{
		{ID: "c1", Report: both}, {ID: "c2", Report: both},
		{ID: "pa1", Report: proto}, {ID: "pa2", Report: proto},
		{ID: "pb1", Report: uid}, {ID: "pb2", Report: uid},
	}
	return f
}

// creditRow is a counted row of cell key, credited by the analyst to cause
// from parts protocol (entry a-proto) and uid-1337 (entry a-uid); clearsA and
// clearsB are the analyst's statements for the parts.
func creditRow(key, cause string, clearsProto, clearsUID bool) ledger.Row {
	r := row("a", "W3", key, cause, ledger.TypeGap)
	r.Fix.Entry = "a-both"
	r.Combined = &ledger.Combined{Entry: "a-both", Certification: []string{"c1", "c2"}}
	r.Parts = []ledger.Part{
		{Cause: "protocol", Entry: "a-proto", Runs: []string{"pa1", "pa2"}, ClearsAlone: &clearsProto},
		{Cause: "uid-1337", Entry: "a-uid", Runs: []string{"pb1", "pb2"}, ClearsAlone: &clearsUID},
	}
	return r
}

// Rule 3 on certificates difftable really issues. Before the combined case
// was modelled, the ledger required the credited part's own certificate, which
// a part that leaves the cell differing cannot have.
func TestCreditRule3(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		cause string
		a, b  bool // the analyst's clearsAlone for protocol, uid-1337
		want  string
		err   string
	}{
		{"part A alone clears the cell", ck1, "protocol", true, false, "protocol", ""},
		{"part B alone clears the cell (the later key in the list)", ck2, "uid-1337", false, true, "uid-1337", ""},
		{"neither part alone clears it: the first key in the list", ck3, "protocol", false, false, "protocol", ""},
		{"ledger credits the wrong key when A alone clears", ck1, "uid-1337", true, false, "", "rule 3 credits the cell to \"protocol\""},
		{"ledger credits the later key when neither clears", ck3, "uid-1337", false, false, "", "rule 3 credits the cell to \"protocol\""},
		{"ledger says A clears alone, the runs say it does not", ck3, "protocol", true, false, "", "clearsAlone true"},
		{"ledger says B does not clear alone, the runs say it does", ck2, "uid-1337", false, false, "", "clearsAlone false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := creditFixture(t)
			f.in.Ledger.Rows = []ledger.Row{creditRow(tc.key, tc.cause, tc.a, tc.b)}
			if tc.err != "" {
				if msg := f.analyzeErr(t); !strings.Contains(msg, tc.err) {
					t.Fatal(msg)
				}
				return
			}
			res := f.analyze(t)
			c := cellOf(t, res, "a", "W3", tc.key)
			if c.Credit != tc.want || c.Status != StCounted {
				t.Fatalf("%+v", c)
			}
		})
	}
	t.Run("the combined entry's certificate is what rule 2 asks for", func(t *testing.T) {
		f := creditFixture(t)
		f.in.Ledger.Rows = []ledger.Row{creditRow(ck3, "protocol", false, false)}
		// the old shape: the credited part's own entry as the fix
		f.in.Ledger.Rows[0].Fix.Entry = "a-proto"
		if msg := f.analyzeErr(t); !strings.Contains(msg, "not the combined entry") {
			t.Fatal(msg)
		}
	})
	t.Run("a combined certificate that is not earned refuses the cell", func(t *testing.T) {
		f := creditFixture(t)
		f.in.Ledger.Rows = []ledger.Row{creditRow(ck3, "protocol", false, false)}
		f.in.Certification[0].Report = f.in.Certification[2].Report // c1 holds the part's report: no a-both
		if msg := f.analyzeErr(t); !strings.Contains(msg, "a-both") {
			t.Fatal(msg)
		}
	})
	t.Run("new keys rank after the frozen list, in ledger order", func(t *testing.T) {
		f := creditFixture(t)
		f.in.Ledger.NewKeys = []ledger.NewKey{{Key: "zz-new"}}
		r := creditRow(ck3, "protocol", false, false)
		r.Parts[1].Cause = "zz-new"
		f.in.Ledger.Rows = []ledger.Row{r}
		res := f.analyze(t)
		if c := cellOf(t, res, "a", "W3", ck3); c.Credit != "protocol" {
			t.Fatalf("%+v", c)
		}
		if f.in.Causes.Order(f.in.Ledger, "zz-new") <= f.in.Causes.Order(f.in.Ledger, "ambient-l7") {
			t.Fatal("new key does not rank last")
		}
	})
	t.Run("both parts clear it: the first key in the list", func(t *testing.T) {
		f := creditFixture(t)
		// k1 is cleared by the protocol part; make the uid-1337 part's runs show k1 cleared too
		uid := difftableReport(t, creditRecords("a-uid", map[string]string{ck1: "true", ck2: "true", ck3: "false"}))
		f.in.Certification[4].Report, f.in.Certification[5].Report = uid, uid
		f.in.Ledger.Rows = []ledger.Row{creditRow(ck1, "protocol", true, true)}
		res := f.analyze(t)
		if c := cellOf(t, res, "a", "W3", ck1); c.Credit != "protocol" {
			t.Fatalf("%+v", c)
		}
	})
}
