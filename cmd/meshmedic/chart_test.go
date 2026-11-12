package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const chartFixtures = "../../pkg/chartcheck/testdata"

func runChartCheck(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = chartCheck(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestChartCheckExitCodes(t *testing.T) {
	def := filepath.Join(chartFixtures, "np-redis-default.yaml")
	fixed := filepath.Join(chartFixtures, "np-redis-fixed.yaml")
	tests := []struct {
		name   string
		args   []string
		stdin  string
		code   int
		stderr string // a substring
	}{
		{"candidate in ambient", []string{"--manifests", def}, "", 1, ""},
		{"same input, sidecar only, is clean", []string{"--manifests", def, "--mesh", "sidecar"}, "", 0, ""},
		{"a probe note alone is not a candidate", []string{"--manifests", filepath.Join(chartFixtures, "np-rule-no-ports.yaml")}, "", 0, ""},
		{"fixed input is clean", []string{"--manifests", fixed}, "", 0, ""},
		{"stdin", []string{"--manifests", "-", "--mesh", "ambient"}, mustRead(t, def), 1, ""},
		{"json", []string{"--manifests", def, "--format", "json"}, "", 1, ""},
		{"nothing given", nil, "", 2, "usage"},
		{"bad mesh", []string{"--manifests", def, "--mesh", "waypoint"}, "", 2, "--mesh"},
		{"bad format", []string{"--manifests", def, "--format", "yaml"}, "", 2, "--format"},
		{"missing file", []string{"--manifests", filepath.Join(t.TempDir(), "none.yaml")}, "", 2, "reading the manifests"},
		{"empty stdin is not clean", []string{"--manifests", "-"}, "", 2, "nothing was checked"},
		{"invalid yaml", []string{"--manifests", "-"}, "kind: [unclosed\n", 2, "not valid"},
		{"chart and manifests", []string{"./chart", "--manifests", def}, "", 2, "not both"},
		{"helm flags with manifests", []string{"--manifests", def, "--set", "a=b"}, "", 2, "no effect"},
		{"two charts", []string{"a", "b"}, "", 2, "one chart"},
		{"unknown flag", []string{"--nope"}, "", 2, "flag provided but not defined"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runChartCheck(t, tc.stdin, tc.args...)
			if code != tc.code {
				t.Errorf("exit %d, want %d (stderr: %s)", code, tc.code, stderr)
			}
			if !strings.Contains(stderr, tc.stderr) {
				t.Errorf("stderr %q does not contain %q", stderr, tc.stderr)
			}
		})
	}
}

func TestChartCheckJSONAndText(t *testing.T) {
	def := filepath.Join(chartFixtures, "np-redis-default.yaml")
	_, out, _ := runChartCheck(t, "", "--manifests", def, "--format", "json")
	var rep struct {
		SchemaVersion int    `json:"schemaVersion"`
		IstioVersion  string `json:"istioVersion"`
		Input         string `json:"input"`
		Candidates    int    `json:"candidates"`
		Notes         int    `json:"notes"`
		Findings      []struct {
			ID   string `json:"id"`
			Mode string `json:"mode"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if rep.SchemaVersion != 1 || rep.IstioVersion != "1.31" || rep.Candidates != 1 || rep.Findings[0].ID != "MM-CHART-001" || rep.Findings[0].Mode != "ambient" {
		t.Errorf("report: %+v", rep)
	}
	if rep.Input != "manifests "+def {
		t.Errorf("input = %q", rep.Input)
	}

	_, text, _ := runChartCheck(t, "", "--manifests", def)
	for _, want := range []string{"MM-CHART-001", "ambient", "candidates found by reading manifests", "limits:", "Istio 1.31"} {
		if !strings.Contains(text, want) {
			t.Errorf("text report lacks %q", want)
		}
	}
}

// Same input, same bytes.
func TestChartCheckDeterministic(t *testing.T) {
	def := filepath.Join(chartFixtures, "np-redis-default.yaml")
	for _, format := range []string{"text", "json"} {
		_, first, _ := runChartCheck(t, "", "--manifests", def, "--format", format)
		for i := 0; i < 3; i++ {
			if _, again, _ := runChartCheck(t, "", "--manifests", def, "--format", format); again != first {
				t.Fatalf("%s output differs between runs", format)
			}
		}
	}
}

// With a chart argument and no helm on the PATH the error names the other way.
func TestChartCheckHelmMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	code, _, stderr := runChartCheck(t, "", "./some-chart")
	if code != 2 || !strings.Contains(stderr, "--manifests") || !strings.Contains(stderr, "helm was not found") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

// A fake helm records its arguments, so the chart path is tested without helm
// and without a shell in between.
func TestChartCheckRendersWithHelm(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> \"" + argsFile + "\"; done\ncat \"" + filepath.Join(chartFixtures, "np-redis-default.yaml") + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, out, stderr := runChartCheck(t, "", "./chart", "-f", "a.yaml", "--values", "b.yaml", "--set", "x=1; touch pwned", "--namespace", "ns1", "--release-name", "r1", "--kube-version", "1.33.0", "--mesh", "ambient")
	if code != 1 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "read chart ./chart") {
		t.Errorf("output: %s", out)
	}
	got, _ := os.ReadFile(argsFile)
	want := "template\nr1\n./chart\n--namespace\nns1\n--kube-version\n1.33.0\n--values\na.yaml\n--values\nb.yaml\n--set\nx=1; touch pwned\n--include-crds\n"
	if string(got) != want {
		t.Errorf("helm argv:\n%q\nwant\n%q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("a value was run by a shell")
	}
}

func TestChartCheckHelmFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte("#!/bin/sh\necho 'Error: path \"x\" not found' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	code, _, stderr := runChartCheck(t, "", "x")
	if code != 2 || !strings.Contains(stderr, "not found") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
