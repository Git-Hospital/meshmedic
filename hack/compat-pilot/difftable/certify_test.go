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
	if c.Certified() || len(c.Cleared) != 0 || !strings.Contains(strings.Join(c.Problems, ";"), "has no finding for this fix to clear") {
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
		if recs[i].Chart == "redis-fix" && recs[i].World == "W2" {
			recs[i].CapacityPending = true
		}
	}
	cell := []string{"W3"}
	listed := []IgnoreCell{ic("W3", probe, "world-universal")}
	c, err := certOf(t, recs, CertifyOpts{CellWorlds: cell, Ignore: listed})
	if err != nil || c.Certified() || !strings.Contains(strings.Join(c.Problems, ";"), "W2 undecided") {
		t.Fatalf("want a failure on the undecided W2, got %v %+v", err, c)
	}
	c, err = certOf(t, recs, CertifyOpts{CellWorlds: cell, Ignore: append(listed, ic("W2", "*", "capacity-Pending"))})
	if err != nil || !c.Certified() {
		t.Fatalf("want certified with the world listed, got %v %+v", err, c)
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
		if r.Chart == "redis-fix" && r.World == "W3" && r.Run == 1 {
			r.Obs = o("true", "ok")
		}
		out = append(out, r)
	}
	// Run 1 of the fix in W3 now has the probe passing: the probe is flaky.
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
