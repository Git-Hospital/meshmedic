package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

// Pilot 1's real outcome (docs/compat-pilot.md): redis and rabbitmq differ in
// W3 and are cleared by their np-15008 fixes, podinfo fails helm test in W1
// because its test pods opt out of the mesh, argo-cd's dex-server was flaky
// once. The reports are difftable -json over the stored records of workflow
// runs 40109270484 (run 4) and 40119399052 (run 6), trimmed to few keys; the
// podMesh records and the 12 h gap are hand-built because the stored records
// predate podMesh and the pilot did not wait.
func TestPilotOne(t *testing.T) {
	in, err := LoadInputs(filepath.Join("testdata", "pilot", "inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Analyze(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Metrics != (Metrics{}) {
		t.Fatalf("pilot 1 has no new keys, got %v", res.Metrics)
	}
	if res.Region.Region != "E" || !res.Trace[len(res.Trace)-1].Matched {
		t.Fatalf("region %+v", res.Region)
	}
	if res.Prevalence.Cells != 8 || strings.Join(res.Prevalence.Charts, ",") != "rabbitmq,redis" {
		t.Fatalf("prevalence %+v", res.Prevalence)
	}
	c := cellOf(t, res, "podinfo", "W1", "helm:test")
	if c.Status != StExcluded || len(c.Reasons) != 1 || c.Reasons[0] != ledger.ReasonCOptout {
		t.Fatalf("podinfo %+v", c)
	}
	if len(res.OptOut) != 1 || res.OptOut[0].Effective != 12 || !res.OptOut[0].Asserted {
		t.Fatalf("opt-out evidence %+v", res.OptOut)
	}
	// redis, W3: three cells, credited and counted as ambient-np
	for _, k := range []string{"helm:install", "workload:StatefulSet/redis-replicas:ready", "workload:StatefulSet/redis-replicas:restarts"} {
		if c := cellOf(t, res, "redis", "W3", k); c.Status != StCounted || c.Credit != AmbientNP || !c.Holds {
			t.Fatalf("redis %+v", c)
		}
	}
	// argo-cd's dex-server restart was flaky in run 1 only
	flaky := 0
	for _, c := range res.Cells {
		if c.Chart == "argo-cd" {
			flaky++
			if c.Status != StExcluded || c.Reasons[0] != ledger.ReasonFlaky {
				t.Fatalf("argo-cd %+v", c)
			}
		}
	}
	if flaky == 0 {
		t.Fatal("argo-cd's flaky cell is missing")
	}
	// the futility test: nothing open, so the experiment could not reach any region but E
	if res.Futility.NMax != 0 || !res.Futility.StopDrop || !res.Futility.OnlyE {
		t.Fatalf("%+v", res.Futility)
	}
	if res.Undecided.Undecided != 0 || res.Undecided.Over {
		t.Fatalf("%+v", res.Undecided)
	}
	if len(res.Unresolved)+len(res.Unexplained) != 0 || res.IfAllCounted.Region.Region != "E" {
		t.Fatalf("%+v %+v", res.Unresolved, res.IfAllCounted)
	}
	for _, c := range res.Cells {
		if c.Status != StCounted && c.Status != StExcluded {
			t.Fatalf("unexpected status %+v", c)
		}
	}
}

func TestPilotOneGolden(t *testing.T) {
	dir := filepath.Join("testdata", "pilot")
	tmp := t.TempDir()
	jsonA, mdA := filepath.Join(tmp, "a.json"), filepath.Join(tmp, "a.md")
	jsonB, mdB := filepath.Join(tmp, "b.json"), filepath.Join(tmp, "b.md")
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	for _, out := range [][2]string{{jsonA, mdA}, {jsonB, mdB}} {
		if code := run([]string{"-inputs", filepath.Join(dir, "inputs.json"), "-json", out[0], "-md", out[1]}, devnull, devnull); code != 0 {
			t.Fatalf("exit %d", code)
		}
	}
	for _, p := range [][2]string{{jsonA, jsonB}, {mdA, mdB}} {
		a, _ := os.ReadFile(p[0])
		b, _ := os.ReadFile(p[1])
		if string(a) != string(b) {
			t.Fatalf("%s differs between two runs", p[0])
		}
	}
	for _, g := range []struct{ got, golden string }{{jsonA, "result.golden.json"}, {mdA, "result.golden.md"}} {
		got, _ := os.ReadFile(g.got)
		path := filepath.Join(dir, g.golden)
		if os.Getenv("UPDATE_GOLDEN") != "" {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s differs from the golden file; rerun with UPDATE_GOLDEN=1 if the change is intended", g.golden)
		}
	}
}

func TestCLIExitCodes(t *testing.T) {
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	dir := filepath.Join("testdata", "pilot")
	if code := run(nil, devnull, devnull); code != 2 {
		t.Fatalf("no args: %d", code)
	}
	if code := run([]string{"-inputs", filepath.Join(dir, "missing.json")}, devnull, devnull); code != 2 {
		t.Fatalf("missing manifest: %d", code)
	}
	if code := run([]string{"-inputs", filepath.Join(dir, "inputs.json")}, devnull, devnull); code != 0 {
		t.Fatalf("default output: %d", code)
	}
	// a ledger that disagrees with the reports exits 1
	tmp := t.TempDir()
	data, _ := os.ReadFile(filepath.Join(dir, "ledger.json"))
	bad := strings.Replace(string(data), `"key": "helm:install"`, `"key": "helm:nothing"`, 1)
	if bad == string(data) {
		t.Fatal("test edit did not apply")
	}
	for _, name := range []string{"run1.report.json", "run2.report.json", "frame.json"} {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		_ = os.WriteFile(filepath.Join(tmp, name), b, 0o644)
	}
	for _, name := range []string{"records-run1", "records-run2"} {
		_ = os.MkdirAll(filepath.Join(tmp, name), 0o755)
		files, _ := filepath.Glob(filepath.Join(dir, name, "*.json"))
		for _, f := range files {
			b, _ := os.ReadFile(f)
			_ = os.WriteFile(filepath.Join(tmp, name, filepath.Base(f)), b, 0o644)
		}
	}
	_ = os.WriteFile(filepath.Join(tmp, "ledger.json"), []byte(bad), 0o644)
	causes, _ := filepath.Abs(filepath.Join("testdata", "causes.json"))
	man, _ := os.ReadFile(filepath.Join(dir, "inputs.json"))
	_ = os.WriteFile(filepath.Join(tmp, "inputs.json"), []byte(strings.Replace(string(man), "../causes.json", causes, 1)), 0o644)
	if code := run([]string{"-inputs", filepath.Join(tmp, "inputs.json")}, devnull, devnull); code != 1 {
		t.Fatalf("disagreement: %d", code)
	}
}
