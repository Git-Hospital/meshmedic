package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Git-Hospital/meshmedic/pkg/demo"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

func demoOut(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	t.Setenv("MESHMEDIC_CATALOG", "")
	var out, errb bytes.Buffer
	code := runDemo(args, &out, &errb)
	return out.String(), errb.String(), code
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./cmd/meshmedic -run Demo -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s; if the detection or the catalog changed on purpose, rerun with -update and read the diff.\n--- got ---\n%s", name, path, got)
	}
}

// One golden file per scenario: the whole screen, byte for byte.
func TestDemoGolden(t *testing.T) {
	for _, n := range demo.Names() {
		t.Run(n, func(t *testing.T) {
			out, errs, code := demoOut(t, "--scenario", n)
			if code != 0 || errs != "" {
				t.Fatalf("exit %d, stderr %q", code, errs)
			}
			golden(t, n, out)
			again, _, _ := demoOut(t, "--scenario", n)
			if again != out {
				t.Fatal("output differs between two runs")
			}
		})
	}
	t.Run("list", func(t *testing.T) {
		out, _, code := demoOut(t, "--list")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		golden(t, "list", out)
	})
	t.Run("verbose", func(t *testing.T) {
		out, _, _ := demoOut(t, "--verbose")
		golden(t, "canary-latency-rollback-verbose", out)
	})
}

// Every screen says it is a replay and ends with the two real next steps.
func TestDemoEveryScreenIsLabelled(t *testing.T) {
	screens := [][]string{{}, {"--list"}, {"--verbose"}}
	for _, n := range demo.Names() {
		screens = append(screens, []string{"--scenario", n})
	}
	for _, args := range screens {
		out, _, code := demoOut(t, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}
		if !strings.Contains(out, "REPLAY") || !strings.Contains(out, "Nothing is being watched and nothing is changed") {
			t.Errorf("%v: no replay notice", args)
		}
		if strings.Contains(out, "CONSTRUCTED") == false && len(args) != 1 {
			t.Errorf("%v: does not say the data is constructed", args)
		}
		if !strings.HasSuffix(out, "  "+demo.NextWatch+"\n") || !strings.Contains(out, "  "+demo.NextCheck) {
			t.Errorf("%v: does not end with the check and watch commands", args)
		}
	}
}

func TestDemoExitCodes(t *testing.T) {
	if _, errs, code := demoOut(t, "--scenario", "no-such-demo"); code != 1 || !strings.Contains(errs, "--list") {
		t.Errorf("unknown scenario: exit %d, stderr %q", code, errs)
	}
	if _, _, code := demoOut(t, "--no-such-flag"); code != 2 {
		t.Errorf("bad flag: exit %d, want 2", code)
	}
	if _, _, code := demoOut(t, "stray"); code != 2 {
		t.Errorf("positional argument: exit %d, want 2", code)
	}
	// The blind replay shows check's refusal; the demo itself succeeded.
	if _, _, code := demoOut(t, "--scenario", "blind-no-telemetry"); code != 0 {
		t.Errorf("blind replay: exit %d, want 0", code)
	}
	if _, errs, code := demoOut(t, "--catalog", t.TempDir()); code != 1 || !strings.Contains(errs, "catalog") {
		t.Errorf("empty catalog dir: exit %d, stderr %q", code, errs)
	}
}

// Without --out nothing is written; with it, the files the PR would carry.
func TestDemoWritesOnlyWithOut(t *testing.T) {
	work := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	if _, _, code := demoOut(t); code != 0 {
		t.Fatal(code)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Fatalf("a run without --out wrote %d entries", len(entries))
	}

	dir := filepath.Join(work, "dossier")
	out, _, code := demoOut(t, "--out", dir)
	if code != 0 {
		t.Fatal(code)
	}
	for _, f := range []string{"canary-latency-rollback-report.md", "canary-latency-rollback-patch.yaml", "canary-latency-rollback-resolved.md"} {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil || len(data) == 0 {
			t.Errorf("%s: %v", f, err)
		}
		if !strings.Contains(out, "Wrote "+filepath.Join(dir, f)) {
			t.Errorf("screen does not list %s", f)
		}
	}
	patch, _ := os.ReadFile(filepath.Join(dir, "canary-latency-rollback-patch.yaml"))
	if !strings.HasPrefix(string(patch), "apiVersion: networking.istio.io/v1\nkind: VirtualService") {
		t.Errorf("patch file is not the rendered patch:\n%s", patch)
	}
	if _, _, code := demoOut(t, "--out", filepath.Join(work, "dossier", "canary-latency-rollback-report.md", "x")); code != 1 {
		t.Errorf("unwritable --out: exit %d, want 1", code)
	}
}
