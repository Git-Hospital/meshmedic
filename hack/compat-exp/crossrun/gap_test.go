package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// F3: a second run that started under 12 h after the first ended is an
// operator slip, not a result. No region and no futility verdict come out.
func TestShortGapIsAnErrorNotADrop(t *testing.T) {
	both := []ChartReport{chart("a", wr("W3", bad(wk)))}
	for _, gap := range []time.Duration{11*time.Hour + 59*time.Minute, time.Hour, 0, -time.Hour} {
		f := newFixture(both, both, nil)
		f.in.Runs[1].Started = f.in.Runs[0].Ended.Add(gap)
		f.in.Runs[1].Ended = f.in.Runs[1].Started.Add(time.Hour)
		res, err := Analyze(f.in)
		if err == nil || res != nil {
			t.Fatalf("gap %v: want an error and no result, got %v", gap, res)
		}
		msg := err.Error()
		for _, want := range []string{f.in.Runs[0].Ended.Format(time.RFC3339), f.in.Runs[1].Started.Format(time.RFC3339), "12 h"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("gap %v: error %q lacks %q", gap, msg, want)
			}
		}
	}
	// exactly 12 h still passes
	f := newFixture(both, both, nil)
	f.in.Runs[1].Started = f.in.Runs[0].Ended.Add(12 * time.Hour)
	f.in.Runs[1].Ended = f.in.Runs[1].Started.Add(time.Hour)
	f.analyze(t)
}

func TestShortGapWritesNoOutputFiles(t *testing.T) {
	dir := t.TempDir()
	man, err := os.ReadFile(filepath.Join("testdata", "pilot", "inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	causes, _ := filepath.Abs(filepath.Join("..", "causes.json"))
	body := strings.Replace(string(man), "../../../causes.json", causes, 1)
	body = strings.Replace(body, "2026-10-25T00:00:00Z", "2026-10-24T11:00:00Z", 1)
	for _, name := range []string{"run1.report.json", "run2.report.json", "frame.json", "ledger.json"} {
		b, _ := os.ReadFile(filepath.Join("testdata", "pilot", name))
		_ = os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
	for _, name := range []string{"records-run1", "records-run2"} {
		_ = os.MkdirAll(filepath.Join(dir, name), 0o755)
		files, _ := filepath.Glob(filepath.Join("testdata", "pilot", name, "*.json"))
		for _, f := range files {
			b, _ := os.ReadFile(f)
			_ = os.WriteFile(filepath.Join(dir, name, filepath.Base(f)), b, 0o644)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "inputs.json"), []byte(body), 0o644)
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	out := filepath.Join(dir, "out.json")
	if code := run([]string{"-inputs", filepath.Join(dir, "inputs.json"), "-json", out, "-md", filepath.Join(dir, "out.md")}, devnull, devnull); code == 0 {
		t.Fatal("exit 0 for a run 2 that started too early")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("a result file was written")
	}
}

// Run 1 only (futility check "after run 1", section 7): differing cells are
// pending toward N_max and P_max, and no region is final.
func TestRunOneOnly(t *testing.T) {
	r := []ChartReport{chart("a", wr("W3", bad(wk))), chart("b", wr("W3", bad(wk))), chart("c", wr("W3", ok(wk)))}
	f := newFixture(r, nil, nil)
	res := f.analyze(t)
	if res.Final {
		t.Fatal("run 1 only must not be final")
	}
	if res.Futility.PendingCells != 2 || res.Futility.NMax != 2 || res.Futility.PMax != 2 || res.Futility.StopDrop {
		t.Fatalf("%+v", res.Futility)
	}
	for _, c := range res.Cells {
		if c.Status != StAwaitRun2 || c.Holds {
			t.Fatalf("%+v", c)
		}
	}
	if res.Region.Region != "none" {
		t.Fatalf("a region %q was declared", res.Region.Region)
	}
	md := string(RenderMarkdown(res))
	if !strings.Contains(md, "run 1 only") || strings.Contains(md, "Region **") {
		t.Fatal(md)
	}
	// with run 2 the result is final
	if !newFixture(r, r, nil).analyze(t).Final {
		t.Fatal("two runs must be final")
	}
}
