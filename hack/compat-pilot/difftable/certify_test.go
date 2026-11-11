package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	cause = "workload:StatefulSet/redis:ready"
	probe = "helm:test"
)

// world builds the observations of one world: the cause key and the probe key.
type obsAt map[string]map[string]string

func o(ready, test string) map[string]string {
	return map[string]string{cause: ready, probe: test, "port:redis:6379:passive": "silent"}
}

// certRecords builds redis and redis-fix with three runs in every world W0 to
// W3. The base differs from W0 on the cause in W3 and on the probe in W1 and
// W3 (the probe stands for a rule-5 cell). The fix clears the cause and, as
// given, whatever else the caller changes through the maps.
func certRecords(base, fix obsAt) []Record {
	var rs []Record
	for _, w := range []string{"W0", "W1", "W2", "W3"} {
		rs = append(rs, chartRecs("redis", "1.0.0", "", same(w, 3, base[w]))...)
		rs = append(rs, chartRecs("redis-fix", "1.0.0", "redis", same(w, 3, fix[w]))...)
	}
	return rs
}

func defaultBase() obsAt {
	return obsAt{"W0": o("true", "ok"), "W1": o("true", "failed"), "W2": o("true", "ok"), "W3": o("false", "failed")}
}

func defaultFix() obsAt {
	return obsAt{"W0": o("true", "ok"), "W1": o("true", "failed"), "W2": o("true", "ok"), "W3": o("true", "failed")}
}

func withMeshWorlds(t *testing.T, oracle string, worlds ...string) {
	t.Helper()
	o, w := oracleWorld, meshWorlds
	t.Cleanup(func() { oracleWorld, meshWorlds = o, w })
	oracleWorld, meshWorlds = oracle, worlds
}

func certOf(t *testing.T, rs []Record, opts CertifyOpts) (Certificate, error) {
	t.Helper()
	a, err := analyze(rs, 3, opts)
	if err != nil {
		return Certificate{}, err
	}
	if len(a.certs) != 1 {
		t.Fatalf("want one certificate, got %d", len(a.certs))
	}
	return a.certs[0], nil
}

func ic(world, key, reason string) IgnoreCell {
	return IgnoreCell{Chart: "redis", World: world, Key: key, Reason: reason}
}

func TestCertifyWithoutNewFlagsIsUnchanged(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	c, err := certOf(t, certRecords(defaultBase(), defaultFix()), CertifyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Certified() || len(c.Problems) != 2 ||
		c.Problems[0] != "W1 still differs from W0: helm:test" || c.Problems[1] != "W3 still differs from W0: helm:test" {
		t.Fatalf("a remaining finding in any mesh world must still fail: %+v", c)
	}
}

func TestCertifyLimitedToCell(t *testing.T) {
	cell := []string{"W3"}
	allIgnored := []IgnoreCell{ic("W3", probe, "world-universal"), ic("W1", probe, "C-optout")}

	tests := []struct {
		name      string
		base, fix obsAt
		opts      CertifyOpts
		want      bool
		problem   string // substring of the problems, when it must fail
		cleared   string
	}{
		{"certifies with ignored cells present", defaultBase(), defaultFix(),
			CertifyOpts{CellWorlds: cell, Ignore: allIgnored}, true, "", "W3"},
		{"fails when the cell's remaining cell is not listed", defaultBase(), defaultFix(),
			CertifyOpts{CellWorlds: cell, Ignore: []IgnoreCell{ic("W1", probe, "C-optout")}}, false, "W3 still differs from W0: helm:test", ""},
		{"a difference outside the cell that the base has is not new", defaultBase(), defaultFix(),
			CertifyOpts{CellWorlds: cell, Ignore: []IgnoreCell{ic("W3", probe, "world-universal")}}, true, "", "W3"},
		{"fails on a new difference in a world outside the cell", defaultBase(),
			obsAt{"W0": o("true", "ok"), "W1": o("true", "failed"), "W2": o("false", "ok"), "W3": o("true", "failed")},
			CertifyOpts{CellWorlds: cell, Ignore: allIgnored}, false, "W2 differs from W0 where redis does not: " + cause, ""},
		{"an ignored key hides nothing in other keys of the same world", defaultBase(),
			obsAt{"W0": o("true", "ok"), "W1": o("false", "failed"), "W2": o("true", "ok"), "W3": o("true", "failed")},
			CertifyOpts{CellWorlds: cell, Ignore: allIgnored}, false, "W1 differs from W0 where redis does not: " + cause, ""},
		{"fails when W0 changes", defaultBase(),
			obsAt{"W0": o("true", "changed"), "W1": o("true", "failed"), "W2": o("true", "ok"), "W3": o("true", "failed")},
			CertifyOpts{CellWorlds: cell, Ignore: allIgnored}, false, "changes the chart in W0: helm:test", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withMeshWorlds(t, "W0", "W1", "W2", "W3")
			c, err := certOf(t, certRecords(tc.base, tc.fix), tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if c.Certified() != tc.want {
				t.Fatalf("certified = %v, want %v: %+v", c.Certified(), tc.want, c)
			}
			if tc.want && (len(c.Cleared) != 1 || c.Cleared[0] != tc.cleared) {
				t.Fatalf("cleared %v, want %s", c.Cleared, tc.cleared)
			}
			if !tc.want && !strings.Contains(strings.Join(c.Problems, "; "), tc.problem) {
				t.Fatalf("problems %q lack %q", c.Problems, tc.problem)
			}
		})
	}
}

// Ignoring the very cell the fix is for must not make a certificate: the
// ignore list is for what the fix is not about.
func TestIgnoredCellDoesNotCountAsCleared(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	base := obsAt{"W0": o("true", "ok"), "W1": o("true", "failed"), "W2": o("true", "ok"), "W3": o("false", "ok")}
	fix := obsAt{"W0": o("true", "ok"), "W1": o("true", "failed"), "W2": o("true", "ok"), "W3": o("true", "ok")}
	c, err := certOf(t, certRecords(base, fix), CertifyOpts{CellWorlds: []string{"W3"},
		Ignore: []IgnoreCell{ic("W3", cause, "flaky"), ic("W1", probe, "C-optout")}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Certified() || len(c.Cleared) != 0 || !strings.Contains(strings.Join(c.Problems, ";"), "ignored cell changed under the fix") {
		t.Fatalf("an ignored cause was counted as cleared: %+v", c)
	}
}

func TestCertificateListsIgnoredCellsEverywhere(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	opts := CertifyOpts{CellWorlds: []string{"W3"}, Ignore: []IgnoreCell{
		ic("W3", probe, "world-universal"), ic("W1", probe, "C-optout")}}
	recs := certRecords(defaultBase(), defaultFix())
	a, err := analyze(recs, 3, opts)
	if err != nil {
		t.Fatal(err)
	}
	var table bytes.Buffer
	if err := a.table(&table); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cell worlds: W3",
		"- `redis-fix` ignores W1 `helm:test` of redis: C-optout",
		"- `redis-fix` ignores W3 `helm:test` of redis: world-universal",
		"| redis-fix | redis | W3 | certified |",
	} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, table.String())
		}
	}
	var js bytes.Buffer
	if err := a.writeJSON(&js); err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(js.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	var cert *CertReport
	for _, c := range rep.Charts {
		if c.Certificate != nil {
			cert = c.Certificate
		}
	}
	if cert == nil || !cert.Certified || len(cert.Ignored) != 2 || cert.Ignored[0].World != "W1" ||
		cert.Ignored[1].Reason != "world-universal" || len(cert.CellWorlds) != 1 || cert.CellWorlds[0] != "W3" {
		t.Fatalf("certificate JSON: %+v", cert)
	}
}

func TestStaleIgnoreEntryIsAnError(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	good := []IgnoreCell{ic("W3", probe, "world-universal"), ic("W1", probe, "C-optout")}
	for name, extra := range map[string]IgnoreCell{
		"a key nobody differs on":       ic("W3", "no:such", "x"),
		"a world with no such key":      ic("W2", probe, "x"),
		"a chart with no fix":           {Chart: "other", World: "W3", Key: probe, Reason: "x"},
		"a wildcard on a decided world": ic("W3", "*", "x"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := certOf(t, certRecords(defaultBase(), defaultFix()),
				CertifyOpts{CellWorlds: []string{"W3"}, Ignore: append(append([]IgnoreCell{}, good...), extra)})
			if err == nil || !strings.Contains(err.Error(), "matches no cell") {
				t.Fatalf("want a stale entry error, got %v", err)
			}
		})
	}
}

func TestIgnoreEntriesAreValidated(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	for name, list := range map[string][]IgnoreCell{
		"no reason":     {ic("W3", probe, "")},
		"no key":        {ic("W3", "", "x")},
		"unknown world": {ic("W9", probe, "x")},
		"the oracle":    {ic("W0", probe, "x")},
		"twice":         {ic("W3", probe, "x"), ic("W3", probe, "y")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := analyze(certRecords(defaultBase(), defaultFix()), 3, CertifyOpts{Ignore: list}); err == nil {
				t.Fatal("an invalid entry was accepted")
			}
		})
	}
}

// A world the fix could not be measured in (a registry refusal, a full runner)
// is not a difference, but the certificate must not pass it silently: it needs
// an entry for the whole world, written "*".
func TestUndecidedWorldOutsideTheCellNeedsAnEntry(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	recs := certRecords(defaultBase(), defaultFix())
	for i := range recs {
		if (recs[i].Chart == "redis-fix" || recs[i].Chart == "redis") && recs[i].World == "W2" {
			recs[i].CapacityPending = append(recs[i].CapacityPending, struct {
				Pod     string `json:"pod"`
				Message string `json:"message"`
			}{Pod: "x-0", Message: "Insufficient cpu"})
		}
	}
	cell := []string{"W3"}
	listed := []IgnoreCell{ic("W3", probe, "world-universal")}
	c, err := certOf(t, recs, CertifyOpts{CellWorlds: cell, Ignore: listed})
	if err != nil || c.Certified() || !strings.Contains(strings.Join(c.Problems, ";"), "W2 undecided") {
		t.Fatalf("want a failure on the undecided W2, got %v %+v", err, c)
	}
	c, err = certOf(t, recs, CertifyOpts{CellWorlds: cell, Ignore: append(listed, ic("W2", "*", "capacity-Pending"))})
	if err != nil || !c.Certified() || len(c.Ignored) != 1 || len(c.NotCompared) != 1 || c.NotCompared[0].World != "W2" {
		t.Fatalf("want certified with the world listed apart, got %v %+v", err, c)
	}
}

func TestLoadIgnoreFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	got, err := loadIgnore(write("ok.json", `[{"chart":"redis","world":"W3","key":"helm:test","reason":"world-universal"}]`))
	if err != nil || len(got) != 1 || got[0].Reason != "world-universal" {
		t.Fatalf("%v %+v", err, got)
	}
	if got, err := loadIgnore(write("empty.json", `[]`)); err != nil || len(got) != 0 {
		t.Fatalf("an empty list is allowed: %v %+v", err, got)
	}
	for name, body := range map[string]string{
		"unknown field": `[{"chart":"a","world":"W3","key":"k","reason":"r","why":"x"}]`,
		"not a list":    `{"chart":"a"}`,
		"trailing":      `[] []`,
	} {
		if _, err := loadIgnore(write(name+".json", body)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := loadIgnore(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestValidCellWorlds(t *testing.T) {
	for _, tc := range []struct {
		cell, all []string
		ok        bool
	}{
		{[]string{"W3"}, []string{"W1", "W3"}, true},
		{[]string{"W1", "W3"}, []string{"W1", "W3"}, true},
		{[]string{"W2"}, []string{"W1", "W3"}, false},
		{[]string{"W3", "W3"}, []string{"W1", "W3"}, false},
		{[]string{""}, []string{"W1", "W3"}, false},
	} {
		if got := validCellWorlds(tc.cell, tc.all); got != tc.ok {
			t.Errorf("validCellWorlds(%v, %v) = %v", tc.cell, tc.all, got)
		}
	}
}

// A key that comes and goes in the cell is flaky; it fails the certificate
// unless the list names it, and then the list shows it.
func TestFlakyKeyInTheCell(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	recs := certRecords(defaultBase(), defaultFix())
	var out []Record
	for _, r := range recs {
		if r.World == "W3" && r.Run == 1 {
			r.Obs = o(map[string]string{"redis": "false", "redis-fix": "true"}[r.Chart], "ok")
		}
		out = append(out, r)
	}
	// Run 1 of both charts in W3 now has the probe passing: the probe is flaky
	// for the base and for the fix alike.
	base := []IgnoreCell{ic("W1", probe, "C-optout")}
	cell := []string{"W3"}
	c, err := certOf(t, out, CertifyOpts{CellWorlds: cell, Ignore: base})
	if err != nil || c.Certified() || !strings.Contains(strings.Join(c.Problems, ";"), "W3 flaky: helm:test") {
		t.Fatalf("want a flaky failure, got %v %+v", err, c)
	}
	c, err = certOf(t, out, CertifyOpts{CellWorlds: cell, Ignore: append(base, ic("W3", probe, "flaky"))})
	if err != nil || !c.Certified() || len(c.Ignored) != 2 {
		t.Fatalf("want certified with the flaky cell listed, got %v %+v", err, c)
	}
}

// A whole world passed with a "*" entry is printed and reported apart from
// ordinary ignored cells.
func TestWorldNotComparedIsDistinct(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	recs := certRecords(defaultBase(), defaultFix())
	for i := range recs {
		if (recs[i].Chart == "redis-fix" || recs[i].Chart == "redis") && recs[i].World == "W2" {
			recs[i].CapacityPending = append(recs[i].CapacityPending, struct {
				Pod     string `json:"pod"`
				Message string `json:"message"`
			}{Pod: "x-0", Message: "Insufficient cpu"})
		}
	}
	a, err := analyze(recs, 3, CertifyOpts{CellWorlds: []string{"W3"},
		Ignore: []IgnoreCell{ic("W3", probe, "world-universal"), ic("W2", "*", "capacity-Pending")}})
	if err != nil {
		t.Fatal(err)
	}
	var table, js bytes.Buffer
	if err := a.table(&table); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "NOT COMPARED") || !strings.Contains(table.String(), "did not compare W2") ||
		strings.Contains(table.String(), "ignores W2") {
		t.Fatalf("table:\n%s", table.String())
	}
	if err := a.writeJSON(&js); err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(js.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	for _, c := range rep.Charts {
		if c.Certificate != nil && (len(c.Certificate.WorldsNotCompared) != 1 || len(c.Certificate.Ignored) != 1 ||
			c.Certificate.WorldsNotCompared[0].World != "W2") {
			t.Fatalf("%+v", c.Certificate)
		}
	}
}

// A record in the shape of hack/compat-exp/RECORD.md decodes, whatever the
// fields difftable does not read. load does not reject unknown fields.
func TestRecordInRecordMdShape(t *testing.T) {
	dir := t.TempDir()
	rec := `{"world":"W3","chart":"redis","chartVersion":"1.0.0","run":1,"fixOf":"",
	 "environment":{"istio":"1.31.3"},"helm":{"rc":0},"workloads":[],"jobs":[],"ports":[],"analyze":[],"events":[],
	 "pullFailures":[{"owner":"Job/x","image":"redis:8"}],
	 "capacityPending":[{"pod":"db-0","message":"0/3 nodes are available: 3 Insufficient cpu."}],
	 "patch":{"path":"hack/compat-exp/patches/a.diff","sha256":"ab","lines":10,"changedLines":4},
	 "podMesh":[{"pod":"a-0","client":false,"owner":"Deployment/a","meshMetadata":[{"kind":"label","key":"sidecar.istio.io/inject","value":"false"}],
	   "istioProxy":{"container":true,"initContainer":false,"native":false},"ambientRedirection":""}],
	 "lateWorkloads":[{"workload":"Deployment/a","pod":"a-0","phase":"Running","ready":true,"restarts":0,"reasons":[],"containers":[]}],
	 "apiserver":{"services":[{"service":"hook","port":443,"reachable":true}],"apiServices":[{"name":"v1.x","available":true}]},
	 "obs":{"helm:install":"ok","late:workload:Deployment/a:ready":"true","apiserver:svc:hook:443:reachable":"true",
	  "apiserver:apiservice:v1.x:available":"unknown"}}`
	if err := os.WriteFile(filepath.Join(dir, "redis.W3.run1.json"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	rs, err := load(dir)
	if err != nil || len(rs) != 1 {
		t.Fatalf("load: %v %d", err, len(rs))
	}
	if len(rs[0].CapacityPending) != 1 || len(rs[0].PullFailures) != 1 {
		t.Fatalf("%+v", rs[0])
	}
	if got := map[string]bool{}; true {
		for k := range rs[0].Obs {
			got[keyClass(k)] = true
		}
		for _, want := range []string{"helm", "late", "apiserver"} {
			if !got[want] {
				t.Errorf("class %s missing from %v", want, got)
			}
		}
	}
	if got, dropped := usableRuns("W3", rs); len(got) != 0 || len(dropped) != 1 || dropped[0].Reason != "pull-failure" {
		t.Fatalf("%v %+v", got, dropped)
	}
}

// The audit's scenario: the fix breaks StatefulSet readiness in W3, the base
// does not, and the ignore file lists that key. The list may hold only cells of
// the base chart, so the entry is an error, not a certificate.
func TestIgnoreOfAFixOnlyDifferenceIsAnError(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	base := obsAt{"W0": o("true", "ok"), "W1": o("true", "failed"), "W2": o("true", "ok"), "W3": o("true", "failed")}
	fix := obsAt{"W0": o("true", "ok"), "W1": o("true", "failed"), "W2": o("true", "ok"), "W3": o("false", "ok")}
	// Base: a probe finding in W3 only. Fix: probe fixed, readiness broken.
	_, err := certOf(t, certRecords(base, fix), CertifyOpts{CellWorlds: []string{"W3"},
		Ignore: []IgnoreCell{ic("W1", probe, "C-optout"), ic("W3", cause, "flaky")}})
	if err == nil || !strings.Contains(err.Error(), "only with the fix") || !strings.Contains(err.Error(), cause) ||
		!strings.Contains(err.Error(), "redis W3") {
		t.Fatalf("want an error naming the fix-only entry, got %v", err)
	}
}

// An ignored base finding that the fix leaves as it is certifies; the same
// cell changed by the fix fails the certificate.
func TestIgnoredCellChangedUnderTheFix(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	opts := CertifyOpts{CellWorlds: []string{"W3"}, Ignore: []IgnoreCell{
		ic("W3", probe, "world-universal"), ic("W1", probe, "C-optout")}}
	c, err := certOf(t, certRecords(defaultBase(), defaultFix()), opts)
	if err != nil || !c.Certified() || len(c.IgnoredChanged) != 0 {
		t.Fatalf("unchanged ignored cells must certify: %v %+v", err, c)
	}
	// The fix turns the W1 probe from failed to a different failure value.
	fix := defaultFix()
	fix["W1"] = o("true", "timeout")
	c, err = certOf(t, certRecords(defaultBase(), fix), opts)
	if err != nil || c.Certified() || len(c.IgnoredChanged) != 1 || c.IgnoredChanged[0].World != "W1" ||
		c.IgnoredChanged[0].Key != probe || !strings.Contains(strings.Join(c.Problems, ";"), "ignored cell changed under the fix: W1 helm:test") {
		t.Fatalf("a changed ignored cell must fail: %v %+v", err, c)
	}
	a, err := analyze(certRecords(defaultBase(), fix), 3, opts)
	if err != nil {
		t.Fatal(err)
	}
	var table, js bytes.Buffer
	if err := a.table(&table); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "ignored cell changed under the fix") {
		t.Fatalf("table lacks the changed cell:\n%s", table.String())
	}
	if err := a.writeJSON(&js); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js.String(), `"ignoredChanged"`) || !strings.Contains(js.String(), `"fixValues"`) {
		t.Fatalf("json lacks ignoredChanged:\n%s", js.String())
	}
}

// A "*" entry is accepted only when the base chart's world is itself
// undecided. On a world that is decided for the base it is an error, whether
// or not the fix's world is decided.
func TestWildcardOnABaseDecidedWorldIsAnError(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	good := []IgnoreCell{ic("W3", probe, "world-universal"), ic("W1", probe, "C-optout")}
	cell := []string{"W3"}
	if _, err := certOf(t, certRecords(defaultBase(), defaultFix()),
		CertifyOpts{CellWorlds: cell, Ignore: append(append([]IgnoreCell{}, good...), ic("W2", "*", "x"))}); err == nil {
		t.Fatal("a wildcard on a decided world was accepted")
	}
	recs := certRecords(defaultBase(), defaultFix())
	for i := range recs {
		if recs[i].Chart == "redis-fix" && recs[i].World == "W2" {
			recs[i].CapacityPending = append(recs[i].CapacityPending, struct {
				Pod     string `json:"pod"`
				Message string `json:"message"`
			}{Pod: "x-0", Message: "Insufficient cpu"})
		}
	}
	_, err := certOf(t, recs, CertifyOpts{CellWorlds: cell, Ignore: append(append([]IgnoreCell{}, good...), ic("W2", "*", "x"))})
	if err == nil || !strings.Contains(err.Error(), "only with the fix") || !strings.Contains(err.Error(), "redis W2") {
		t.Fatalf("want a fix-only error for a wildcard on a world only the fix lacks, got %v", err)
	}
}

// A chart whose oracle install failed in every run compares as "same", because
// the failure is identical in every world. The verdict is left alone; the
// report and the table say that the oracle installs failed.
func TestOracleInstallMakesAVacuousSameVisible(t *testing.T) {
	withMeshWorlds(t, "W0", "W1", "W2", "W3")
	failed := map[string]string{"helm:install": "failed"}
	good := map[string]string{"helm:install": "ok", "workload:Deployment/v:ready": "true"}
	var rs []Record
	for _, w := range []string{"W0", "W1", "W2", "W3"} {
		rs = append(rs, chartRecs("vacuous", "0.12.0", "", same(w, 3, failed))...)
		rs = append(rs, chartRecs("healthy", "1.0.0", "", same(w, 3, good))...)
	}
	mixed := chartRecs("mixed", "1.0.0", "", runs("W0", good, failed, good))
	for _, w := range []string{"W1", "W2", "W3"} {
		mixed = append(mixed, chartRecs("mixed", "1.0.0", "", same(w, 3, good))...)
	}
	rs = append(rs, mixed...)
	a, err := analyze(rs, 3, CertifyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ChartReport{}
	for _, c := range a.report().Charts {
		got[c.Chart] = c
	}
	if v := got["vacuous"]; v.OracleInstall != (OracleInstall{Runs: 3, Failed: 3, Status: "failed"}) {
		t.Fatalf("vacuous: %+v", v.OracleInstall)
	}
	for _, w := range got["vacuous"].Worlds {
		if w.Verdict != "same" {
			t.Fatalf("the verdict logic must stay as it is: %s %s", w.World, w.Verdict)
		}
	}
	if v := got["healthy"].OracleInstall; v.Status != "ok" || v.Ok != 3 {
		t.Fatalf("healthy: %+v", v)
	}
	if v := got["mixed"].OracleInstall; v.Status != "mixed" || v.Ok != 2 || v.Failed != 1 {
		t.Fatalf("mixed: %+v", v)
	}
	if v := oracleInstall(nil); v.Status != "unknown" {
		t.Fatalf("no oracle records: %+v", v)
	}
	var table bytes.Buffer
	if err := a.table(&table); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "| vacuous | 0.12.0 | 0/3 ok (FAILED) | same | same | same |") ||
		!strings.Contains(table.String(), "| healthy | 1.0.0 | 3/3 ok | same |") {
		t.Fatalf("table:\n%s", table.String())
	}
}
