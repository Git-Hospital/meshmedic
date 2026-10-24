package main

import (
	"bytes"
	"strings"
	"testing"
)

func rec(world string, run int, obs map[string]string) Record {
	return Record{World: world, Chart: "c", Run: run, Obs: obs}
}

func runs(world string, obs ...map[string]string) []Record {
	out := make([]Record, len(obs))
	for i, o := range obs {
		out[i] = rec(world, i+1, o)
	}
	return out
}

func same(world string, n int, obs map[string]string) []Record {
	all := make([]map[string]string, n)
	for i := range all {
		all[i] = obs
	}
	return runs(world, all...)
}

func TestConsistentDifferenceIsAFinding(t *testing.T) {
	w0 := same("W0", 3, map[string]string{"port:db:3306:passive": "bytes"})
	w1 := same("W1", 3, map[string]string{"port:db:3306:passive": "silent"})
	c, _ := Compare("c", "W1", w0, w1, 3)
	if len(c.Findings) != 1 || c.Findings[0].Key != "port:db:3306:passive" {
		t.Fatalf("want one finding on the passive probe, got %+v", c)
	}
	if c.Verdict() != "differs (1)" {
		t.Fatalf("verdict %q", c.Verdict())
	}
}

func TestDifferenceInSomeRunsIsFlakyNotAFinding(t *testing.T) {
	w0 := same("W0", 3, map[string]string{"workload:Deployment/a:ready": "true"})
	w3 := runs("W3",
		map[string]string{"workload:Deployment/a:ready": "false"},
		map[string]string{"workload:Deployment/a:ready": "true"},
		map[string]string{"workload:Deployment/a:ready": "true"},
	)
	c, _ := Compare("c", "W3", w0, w3, 3)
	if len(c.Findings) != 0 || len(c.Flaky) != 1 {
		t.Fatalf("want one flaky and no findings, got %+v", c)
	}
	if c.Verdict() != "flaky (1)" {
		t.Fatalf("verdict %q", c.Verdict())
	}
}

// A chart that cannot agree with itself without a mesh says nothing about the
// mesh, so a key unstable in W0 is never compared.
func TestKeyUnstableInW0IsNotCompared(t *testing.T) {
	w0 := runs("W0",
		map[string]string{"workload:StatefulSet/db:restarts": "0"},
		map[string]string{"workload:StatefulSet/db:restarts": ">0"},
		map[string]string{"workload:StatefulSet/db:restarts": "0"},
	)
	w1 := same("W1", 3, map[string]string{"workload:StatefulSet/db:restarts": ">0"})
	c, o := Compare("c", "W1", w0, w1, 3)
	if len(c.Findings)+len(c.Flaky) != 0 {
		t.Fatalf("an unstable W0 key produced a difference: %+v", c)
	}
	if len(o.Unstable) != 1 {
		t.Fatalf("want the key listed as unstable, got %+v", o.Unstable)
	}
}

// The hook Job case: W0 deletes it on success, so the key never appears there;
// in the mesh world it hangs and is recorded incomplete.
func TestKeyOnlyInMeshWorldComparesAgainstAbsent(t *testing.T) {
	w0 := same("W0", 3, map[string]string{"helm:install": "ok"})
	w1 := same("W1", 3, map[string]string{"helm:install": "failed", "job:migrate:complete": "false"})
	c, _ := Compare("c", "W1", w0, w1, 3)
	if len(c.Findings) != 2 {
		t.Fatalf("want install and job findings, got %+v", c.Findings)
	}
	for _, d := range c.Findings {
		if d.Key == "job:migrate:complete" && d.Oracle != absent {
			t.Fatalf("job oracle should be absent, got %q", d.Oracle)
		}
	}
}

// An HTTP request to a MySQL port under a sniffing sidecar comes back 502.
// That is the probe's artefact, so it must not become a finding.
func TestHTTPProbeIgnoredWhereW0WasNotHTTP(t *testing.T) {
	w0 := same("W0", 3, map[string]string{"port:db:3306:http": "none", "port:web:80:http": "200"})
	w1 := same("W1", 3, map[string]string{"port:db:3306:http": "502", "port:web:80:http": "503"})
	c, _ := Compare("c", "W1", w0, w1, 3)
	if len(c.Findings) != 1 || c.Findings[0].Key != "port:web:80:http" {
		t.Fatalf("want only the real HTTP port, got %+v", c.Findings)
	}
}

func TestTooFewRunsIsUndecidedNotCompatible(t *testing.T) {
	w0 := same("W0", 3, map[string]string{"helm:install": "ok"})
	w2 := same("W2", 2, map[string]string{"helm:install": "ok"})
	c, _ := Compare("c", "W2", w0, w2, 3)
	if c.Verdict() != "undecided" {
		t.Fatalf("two runs must not read as same, got %q", c.Verdict())
	}
	c, _ = Compare("c", "W2", nil, same("W2", 3, nil), 3)
	if c.Verdict() != "undecided" {
		t.Fatalf("a missing W0 must not read as same, got %q", c.Verdict())
	}
}

func TestRenderTable(t *testing.T) {
	var records []Record
	records = append(records, same("W0", 3, map[string]string{"port:db:3306:passive": "bytes"})...)
	records = append(records, same("W1", 3, map[string]string{"port:db:3306:passive": "silent"})...)
	records = append(records, same("W3", 3, map[string]string{"port:db:3306:passive": "bytes"})...)
	var b bytes.Buffer
	if err := render(&b, records, 3); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"| c |  | differs (1) | undecided | same |",
		"- `port:db:3306:passive`: W0 `bytes`, W1 `silent` (3/3)",
		"W2 has 0 of 3 runs.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
