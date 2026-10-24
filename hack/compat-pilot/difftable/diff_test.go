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

func withPort(rs []Record, svc string, port int, name string) []Record {
	for i := range rs {
		rs[i].Ports = append(rs[i].Ports, struct {
			Svc         string `json:"svc"`
			Port        int    `json:"port"`
			Name        string `json:"name"`
			AppProtocol string `json:"appProtocol"`
		}{Svc: svc, Port: port, Name: name})
	}
	return rs
}

// The first pilot run: podinfo's gRPC port greets in W0 (an HTTP/2 SETTINGS
// frame) and is silent behind a sidecar. Real gRPC clients speak first, so
// this must not become a finding, while a server-first TCP port still must.
func TestPassiveProbeIgnoredOnHTTPFamilyPorts(t *testing.T) {
	obs := func(v string) map[string]string {
		return map[string]string{"port:podinfo:9999:passive": v, "port:nats:4222:passive": v}
	}
	w0 := withPort(withPort(same("W0", 3, obs("bytes")), "podinfo", 9999, "grpc"), "nats", 4222, "tcp-client")
	w1 := withPort(withPort(same("W1", 3, obs("silent")), "podinfo", 9999, "grpc"), "nats", 4222, "tcp-client")
	c, _ := Compare("c", "W1", w0, w1, 3)
	if len(c.Findings) != 1 || c.Findings[0].Key != "port:nats:4222:passive" {
		t.Fatalf("want only the TCP port as a finding, got %+v", c.Findings)
	}
}

func comparisons(chart string, w0 []Record, worlds map[string][]Record) map[string]Comparison {
	m := map[string]Comparison{}
	for _, w := range meshWorlds {
		m[w], _ = Compare(chart, w, w0, worlds[w], 3)
	}
	return m
}

func TestCertifyFixThatClearsTheFinding(t *testing.T) {
	ok := map[string]string{"workload:StatefulSet/redis-replicas:ready": "true"}
	bad := map[string]string{"workload:StatefulSet/redis-replicas:ready": "false"}
	base := comparisons("redis", same("W0", 3, ok), map[string][]Record{
		"W1": same("W1", 3, ok), "W2": same("W2", 3, ok), "W3": same("W3", 3, bad)})
	fix := comparisons("redis-fix", same("W0", 3, ok), map[string][]Record{
		"W1": same("W1", 3, ok), "W2": same("W2", 3, ok), "W3": same("W3", 3, ok)})
	c := Certify("redis-fix", "redis", fix, base, same("W0", 3, ok), same("W0", 3, ok), 3)
	if !c.Certified() || len(c.Cleared) != 1 || c.Cleared[0] != "W3" {
		t.Fatalf("want certified clearing W3, got %+v", c)
	}
}

func TestCertifyRefusesFixThatBreaksAnotherWorldOrW0(t *testing.T) {
	ok := map[string]string{"workload:StatefulSet/redis-replicas:ready": "true", "port:redis:6379:passive": "silent"}
	bad := map[string]string{"workload:StatefulSet/redis-replicas:ready": "false", "port:redis:6379:passive": "silent"}
	base := comparisons("redis", same("W0", 3, ok), map[string][]Record{
		"W1": same("W1", 3, ok), "W2": same("W2", 3, ok), "W3": same("W3", 3, bad)})

	// Clears W3 but breaks W1.
	fix := comparisons("redis-fix", same("W0", 3, ok), map[string][]Record{
		"W1": same("W1", 3, bad), "W2": same("W2", 3, ok), "W3": same("W3", 3, ok)})
	if c := Certify("redis-fix", "redis", fix, base, same("W0", 3, ok), same("W0", 3, ok), 3); c.Certified() {
		t.Fatalf("a fix that breaks W1 was certified: %+v", c)
	}

	// Clears every mesh world by changing what the chart does without one.
	changed := map[string]string{"workload:StatefulSet/redis-replicas:ready": "true", "port:redis:6379:passive": "bytes"}
	fix = comparisons("redis-fix", same("W0", 3, changed), map[string][]Record{
		"W1": same("W1", 3, changed), "W2": same("W2", 3, changed), "W3": same("W3", 3, changed)})
	if c := Certify("redis-fix", "redis", fix, base, same("W0", 3, changed), same("W0", 3, ok), 3); c.Certified() {
		t.Fatalf("a fix that changes W0 was certified: %+v", c)
	}
}

func TestCertifyRefusesFixForChartWithoutFinding(t *testing.T) {
	ok := map[string]string{"helm:install": "ok"}
	all := map[string][]Record{"W1": same("W1", 3, ok), "W2": same("W2", 3, ok), "W3": same("W3", 3, ok)}
	base := comparisons("p", same("W0", 3, ok), all)
	fix := comparisons("p-fix", same("W0", 3, ok), all)
	if c := Certify("p-fix", "p", fix, base, same("W0", 3, ok), same("W0", 3, ok), 3); c.Certified() {
		t.Fatalf("certified a fix with nothing to fix: %+v", c)
	}
}

func pullFailed(rs []Record, image string) []Record {
	for i := range rs {
		rs[i].Workloads = append(rs[i].Workloads, struct {
			Workload   string   `json:"workload"`
			Reasons    []string `json:"reasons"`
			Containers []struct {
				Image  string `json:"image"`
				Reason string `json:"reason"`
			} `json:"containers"`
		}{Workload: "Deployment/redis", Containers: []struct {
			Image  string `json:"image"`
			Reason string `json:"reason"`
		}{{Image: image, Reason: "ImagePullBackOff"}}})
	}
	return rs
}

// The second pilot run: argo-cd's redis image failed to pull in W0 and W3 and
// pulled in W1 and W2, 3 of 3 each, because a world's runs share a cluster.
// The first run had it the other way round. A registry outage is not a
// finding in either direction.
func TestImagePullFailureLeavesTheRunOut(t *testing.T) {
	ok := map[string]string{"helm:install": "ok"}
	failed := map[string]string{"helm:install": "failed"}
	w0 := pullFailed(same("W0", 3, failed), "ecr-public.aws.com/docker/library/redis:8.6.4-alpine")
	c, _ := Compare("c", "W1", w0, same("W1", 3, ok), 3)
	if c.Verdict() != "undecided" || len(c.Dropped) != 3 {
		t.Fatalf("want undecided with three runs left out, got %+v", c)
	}
	if !strings.Contains(c.Dropped[0], "ecr-public.aws.com") {
		t.Fatalf("the reason should name the image, got %q", c.Dropped[0])
	}

	// One bad run out of four still leaves three to decide on.
	w1 := append(same("W1", 3, ok), pullFailed(runs("W1", failed), "x")...)
	w1[3].Run = 4
	c, _ = Compare("c", "W1", same("W0", 3, ok), w1, 3)
	if c.Verdict() != "same" || len(c.Dropped) != 1 {
		t.Fatalf("want same with one run left out, got %+v", c)
	}
}

// A hook Job's pod is not a workload, so its pull failure arrives only in
// pullFailures; it must leave the run out all the same.
func TestJobPodPullFailureLeavesTheRunOut(t *testing.T) {
	w1 := same("W1", 3, map[string]string{"job:secret-init:complete": "false"})
	for i := range w1 {
		w1[i].PullFailures = append(w1[i].PullFailures, struct {
			Owner string `json:"owner"`
			Image string `json:"image"`
		}{Owner: "Job/secret-init", Image: "redis:8"})
	}
	c, _ := Compare("c", "W1", same("W0", 3, map[string]string{"job:secret-init:complete": "true"}), w1, 3)
	if c.Verdict() != "undecided" || len(c.Dropped) != 3 {
		t.Fatalf("want undecided with three runs left out, got %+v", c)
	}
}

// ztunnel does not parse HTTP, so in W3 a gRPC port that stops greeting is a
// difference like any other.
func TestPassiveProbeComparedOnHTTPPortsInAmbient(t *testing.T) {
	obs := func(v string) map[string]string { return map[string]string{"port:podinfo:9999:passive": v} }
	w0 := withPort(same("W0", 3, obs("bytes")), "podinfo", 9999, "grpc")
	w3 := withPort(same("W3", 3, obs("silent")), "podinfo", 9999, "grpc")
	c, _ := Compare("c", "W3", w0, w3, 3)
	if len(c.Findings) != 1 {
		t.Fatalf("want the W3 difference kept, got %+v", c)
	}
}

func TestCertifyRefusesFixFlakyWithoutMesh(t *testing.T) {
	ok := map[string]string{"workload:StatefulSet/redis-replicas:ready": "true"}
	bad := map[string]string{"workload:StatefulSet/redis-replicas:ready": "false"}
	base := comparisons("redis", same("W0", 3, ok), map[string][]Record{
		"W1": same("W1", 3, ok), "W2": same("W2", 3, ok), "W3": same("W3", 3, bad)})
	fixW0 := runs("W0", ok, ok, bad)
	fix := comparisons("redis-fix", fixW0, map[string][]Record{
		"W1": same("W1", 3, ok), "W2": same("W2", 3, ok), "W3": same("W3", 3, ok)})
	if c := Certify("redis-fix", "redis", fix, base, fixW0, same("W0", 3, ok), 3); c.Certified() {
		t.Fatalf("a fix flaky without a mesh was certified: %+v", c)
	}
}

func TestRenderRefusesMixedVersions(t *testing.T) {
	records := append(same("W0", 3, nil), same("W1", 3, nil)...)
	records[4].ChartVersion = "2.0.0"
	if err := render(&bytes.Buffer{}, records, 3); err == nil {
		t.Fatal("mixed versions were tabled")
	}
}

// The second pilot holds the ambient worlds against W1, because a canary
// weight does nothing without a mesh: a W0 to compare with does not exist.
func TestRenderAgainstAnotherOracle(t *testing.T) {
	defer func(o string, ws []string) { oracleWorld, meshWorlds = o, ws }(oracleWorld, meshWorlds)
	oracleWorld, meshWorlds = "W1", []string{"W3", "W4"}

	atWeight := map[string]string{"traffic:canary": "at-weight"}
	records := append(same("W1", 3, atWeight), same("W3", 3, map[string]string{"traffic:canary": "zero"})...)
	records = append(records, same("W4", 3, atWeight)...)
	var b bytes.Buffer
	if err := render(&b, records, 3); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"| chart | version | W3 ambient | W4 ambient+waypoint |",
		"| c |  | differs (1) | same |",
		"- `traffic:canary`: W1 `at-weight`, W3 `zero` (3/3)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "W0") {
		t.Errorf("table mentions W0 although the oracle is W1:\n%s", out)
	}
}

func TestValidWorlds(t *testing.T) {
	for _, tc := range []struct {
		oracle string
		worlds []string
		ok     bool
	}{
		{"W0", []string{"W1", "W2", "W3"}, true},
		{"W1", []string{"W3", "W4"}, true},
		{"W1", []string{"W1", "W3"}, false},
		{"W1", []string{"W5"}, false},
		{"W9", []string{"W3"}, false},
		{"W1", []string{""}, false},
	} {
		if got := validWorlds(tc.oracle, tc.worlds); got != tc.ok {
			t.Errorf("validWorlds(%s, %v) = %v, want %v", tc.oracle, tc.worlds, got, tc.ok)
		}
	}
}
