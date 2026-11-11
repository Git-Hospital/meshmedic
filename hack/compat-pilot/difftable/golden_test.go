package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func chartRecs(chart, version, fixOf string, rs []Record) []Record {
	for i := range rs {
		rs[i].Chart, rs[i].ChartVersion, rs[i].FixOf = chart, version, fixOf
		rs[i].Environment = map[string]string{"kubernetes": "1.36.0", "istio": "1.31.3"}
	}
	return rs
}

// fixtureRecords is a small experiment: redis differs in W3 and its fix
// clears it, web has a flaky key in W1 and a pull failure in W3, db has a
// difference in W1 and a W3 left with too few runs.
func fixtureRecords() []Record {
	ok := map[string]string{"helm:install": "ok", "helm:test": "ok", "workload:StatefulSet/redis:ready": "true"}
	bad := map[string]string{"helm:install": "ok", "helm:test": "ok", "workload:StatefulSet/redis:ready": "false"}
	var rs []Record
	for _, w := range []string{"W0", "W1", "W2"} {
		rs = append(rs, chartRecs("redis", "1.0.0", "", same(w, 3, ok))...)
		rs = append(rs, chartRecs("redis-fix", "1.0.0", "redis", same(w, 3, ok))...)
	}
	rs = append(rs, chartRecs("redis", "1.0.0", "", same("W3", 3, bad))...)
	rs = append(rs, chartRecs("redis-fix", "1.0.0", "redis", same("W3", 3, ok))...)

	web := map[string]string{"helm:install": "ok", "workload:Deployment/web:ready": "true"}
	flaky := []map[string]string{
		{"helm:install": "ok", "workload:Deployment/web:ready": "false"}, web, web}
	rs = append(rs, chartRecs("web", "2.3.1", "", same("W0", 3, web))...)
	rs = append(rs, chartRecs("web", "2.3.1", "", runs("W1", flaky...))...)
	rs = append(rs, chartRecs("web", "2.3.1", "", same("W2", 3, web))...)
	rs = append(rs, chartRecs("web", "2.3.1", "", pullFailed(same("W3", 3, web), "docker.io/web:1"))...)

	db := map[string]string{"helm:install": "ok", "port:db:3306:passive": "bytes", "port:db:3306:http": "none"}
	dbBad := map[string]string{"helm:install": "ok", "port:db:3306:passive": "silent", "port:db:3306:http": "502"}
	rs = append(rs, chartRecs("db", "9.0.0", "", same("W0", 3, db))...)
	rs = append(rs, chartRecs("db", "9.0.0", "", same("W1", 3, dbBad))...)
	rs = append(rs, chartRecs("db", "9.0.0", "", same("W2", 3, db))...)
	rs = append(rs, chartRecs("db", "9.0.0", "", same("W3", 2, db))...)
	return rs
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the golden file; run with -update only if the change is intended\n--- got ---\n%s", name, got)
	}
}

// The human-readable table is what the pilot workflows publish. Its golden
// file was written by the version before -json existed.
func TestTableGolden(t *testing.T) {
	var b bytes.Buffer
	if err := render(&b, fixtureRecords(), 3); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "table.golden.md", b.Bytes())
}

func jsonOf(t *testing.T, records []Record) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := renderJSON(&b, records, 3); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestJSONGolden(t *testing.T) {
	checkGolden(t, "report.golden.json", jsonOf(t, fixtureRecords()))
}

// Two runs of the same input give the same bytes, whatever order the records
// were read in.
func TestJSONIsStable(t *testing.T) {
	recs := fixtureRecords()
	want := jsonOf(t, recs)
	rev := make([]Record, len(recs))
	for i, r := range recs {
		rev[len(recs)-1-i] = r
	}
	if got := jsonOf(t, rev); !bytes.Equal(got, want) {
		t.Fatalf("the report depends on record order:\n%s", got)
	}
	if got := jsonOf(t, fixtureRecords()); !bytes.Equal(got, want) {
		t.Fatal("the report differs between two runs")
	}
}

func TestJSONFields(t *testing.T) {
	var rep struct {
		Schema int
		Charts []struct {
			Chart, FixOf string
			Worlds       []struct {
				World, Verdict string
				RunsUsed       []int
				RunsLeftOut    []RunLeftOut
				OracleRunsUsed []int
				Keys           []struct {
					Key, Class, Verdict, Reason string
					OracleValue                 *string
					WorldValues                 []string
				}
			}
			Certificate *struct {
				Certified bool
				Cleared   []string
			}
		}
	}
	if err := json.Unmarshal(jsonOf(t, fixtureRecords()), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Schema != reportSchema {
		t.Fatalf("schema %d", rep.Schema)
	}
	byName := map[string]int{}
	for i, c := range rep.Charts {
		byName[c.Chart] = i
	}
	web := rep.Charts[byName["web"]]
	w3 := web.Worlds[2]
	if w3.World != "W3" || w3.Verdict != "undecided" || len(w3.RunsUsed) != 0 || len(w3.RunsLeftOut) != 3 ||
		w3.RunsLeftOut[0].Reason != "pull-failure" || w3.RunsLeftOut[0].Detail != "docker.io/web:1" {
		t.Fatalf("web W3: %+v", w3)
	}
	if k := w3.Keys[0]; k.Verdict != "undecided" || k.Class != "helm" {
		t.Fatalf("undecided key: %+v", k)
	}
	w1 := web.Worlds[0]
	if w1.Verdict != "flaky" || len(w1.RunsUsed) != 3 {
		t.Fatalf("web W1: %+v", w1)
	}
	db := rep.Charts[byName["db"]].Worlds[0]
	var sawLeftOut bool
	for _, k := range db.Keys {
		if k.Key == "port:db:3306:http" {
			sawLeftOut = k.Verdict == "left out" && k.Reason != "" && k.Class == "port"
		}
		if k.Key == "port:db:3306:passive" && (k.Verdict != "differs" || k.OracleValue == nil || *k.OracleValue != "bytes" || k.WorldValues[0] != "silent") {
			t.Fatalf("db passive: %+v", k)
		}
	}
	if !sawLeftOut {
		t.Fatal("the http probe of a non-http port should be left out with a reason")
	}
	fix := rep.Charts[byName["redis-fix"]]
	if fix.FixOf != "redis" || fix.Certificate == nil || !fix.Certificate.Certified || fix.Certificate.Cleared[0] != "W3" {
		t.Fatalf("redis-fix: %+v", fix)
	}
}

func TestKeyClass(t *testing.T) {
	for key, want := range map[string]string{
		"helm:test": "helm", "apiserver:svc:443": "apiserver", "late:workload:a:ready": "late",
		"port:db:3306:http": "port", "plain": "plain",
	} {
		if got := keyClass(key); got != want {
			t.Errorf("keyClass(%q) = %q, want %q", key, got, want)
		}
	}
}

// The oracle and the mesh world each name the runs they left out.
func TestJSONNamesOracleRunsLeftOut(t *testing.T) {
	ok := map[string]string{"helm:install": "ok"}
	w0 := append(same("W0", 3, ok), capacityPending(runs("W0", ok))...)
	w0[3].Run = 4
	recs := append(w0, same("W1", 3, ok)...)
	var b bytes.Buffer
	if err := renderJSON(&b, recs, 3); err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(b.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	w := rep.Charts[0].Worlds[0]
	if len(w.OracleRunsLeftOut) != 1 || w.OracleRunsLeftOut[0].Reason != "capacity" || w.OracleRunsLeftOut[0].Run != 4 || len(w.OracleRunsUsed) != 3 {
		t.Fatalf("%+v", w)
	}
}
