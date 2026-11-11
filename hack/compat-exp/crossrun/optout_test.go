package main

import (
	"strings"
	"testing"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

type meta = struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

func optedOut(world string) []meta {
	if ambientWorlds[world] {
		return []meta{{"label", "istio.io/dataplane-mode", "none"}}
	}
	return []meta{{"annotation", "sidecar.istio.io/inject", "false"}}
}

// testPod is a pod helm test created: a hook pod (testHook true), opted out
// or not.
func testPod(name string, optOut bool) PodMesh {
	p := PodMesh{Pod: name, Owner: "Pod/" + name, TestHook: true}
	if optOut {
		p.MeshMetadata = optedOut("W1")
	}
	return p
}

// optFixture: chart "prom" in W1 with a finding of `key`; the records of both
// runs hold podMesh `pods` (the snapshot before helm test) and testPodMesh
// `testPods` with the given status.
func optFixture(key string, callers []string, status string, pods []PodMesh, testPods ...PodMesh) *fixture {
	r := []ChartReport{chart("prom", wr("W1", bad(key)))}
	f := newFixture(r, r, nil)
	f.pad(9)
	recs := []Record{}
	for run := 1; run <= 3; run++ {
		recs = append(recs, Record{World: "W1", Chart: "prom", Run: run, PodMesh: pods, TestPodMesh: testPods, TestPodMeshStatus: status})
	}
	for i := range f.in.Runs {
		f.in.Runs[i].HasRecords, f.in.Runs[i].Records = true, recs
	}
	f.in.Ledger.Rows = []ledger.Row{{Chart: "prom", World: "W1", Key: key, Status: ledger.StatusExcluded,
		Exclusion: &ledger.Exclusion{Reason: ledger.ReasonCOptout, Evidence: "x", Callers: callers},
		Reviewer:  &ledger.Reviewer{Verdict: ledger.VerdictAgree, Run: "rv"}}}
	return f
}

// F5: the audit excluded a Prometheus port finding because an unrelated
// admission Job pod was opted out. That must not exclude it.
func TestCOptoutAuditScenarioPortFinding(t *testing.T) {
	const portKey = "port:prom:9090:http"
	job := PodMesh{Pod: "prom-admission-create-abcde", Owner: "Job/prom-admission-create", MeshMetadata: optedOut("W1")}
	f := optFixture(portKey, []string{"prom-admission-create"}, "listed", []PodMesh{job, {Pod: "prom-0", Owner: "StatefulSet/prom"}, {Pod: "mmc-client", Client: true}})
	msg := f.analyzeErr(t)
	if !strings.Contains(msg, "helm:test only") {
		t.Fatal(msg)
	}
	// without a ledger row the cell is not excluded either
	f.in.Ledger.Rows = nil
	res := f.analyze(t)
	if c := cellOf(t, res, "prom", "W1", portKey); contains(c.Reasons, ledger.ReasonCOptout) {
		t.Fatalf("%+v", c)
	}
}

// R5, the second audit's scenario: callers=[app-test-connection] (opted out)
// while app-test-grpc, a test pod in the mesh with a sidecar, also exists. The
// document says every failing caller carries the chart's own opt-out; the
// callers are all the helm test hook pods, not the ones the analyst names.
func TestCOptoutNamesOnlyOnePodOfTwo(t *testing.T) {
	const k = "helm:test"
	conn := testPod("app-test-connection", true)
	grpc := testPod("app-test-grpc", false)
	grpc.IstioProxy.Container = true
	main := PodMesh{Pod: "app-0", Owner: "StatefulSet/app"}
	t.Run("the unnamed in-mesh test pod refuses the exclusion", func(t *testing.T) {
		f := optFixture(k, []string{"app-test-connection"}, "listed", []PodMesh{main}, conn, grpc)
		msg := f.analyzeErr(t)
		if !strings.Contains(msg, "app-test-grpc") || !strings.Contains(msg, "does not carry the opt-out") {
			t.Fatal(msg)
		}
	})
	t.Run("both opted out but only one named: the row must equal the computed set", func(t *testing.T) {
		g2 := testPod("app-test-grpc", true)
		f := optFixture(k, []string{"app-test-connection"}, "listed", []PodMesh{main}, conn, g2)
		if msg := f.analyzeErr(t); !strings.Contains(msg, "a caller the row does not name") || !strings.Contains(msg, "app-test-grpc") {
			t.Fatal(msg)
		}
	})
	t.Run("all test pods opted out and all named: accepted", func(t *testing.T) {
		g2 := testPod("app-test-grpc", true)
		f := optFixture(k, []string{"app-test-connection", "app-test-grpc"}, "listed", []PodMesh{main}, conn, g2)
		res := f.analyze(t)
		if c := cellOf(t, res, "prom", "W1", k); c.Status != StExcluded || c.Reasons[0] != ledger.ReasonCOptout {
			t.Fatalf("%+v", c)
		}
	})
}

func TestCOptoutCallersAreChecked(t *testing.T) {
	const k = "helm:test"
	test := testPod("prom-test-x1", true)
	main := PodMesh{Pod: "prom-0", Owner: "StatefulSet/prom"}
	job := PodMesh{Pod: "prom-admission-create-abcde", Owner: "Job/prom-admission-create", MeshMetadata: optedOut("W1")}
	client := PodMesh{Pod: "mmc-client", Client: true, MeshMetadata: optedOut("W1")}
	notHook := PodMesh{Pod: "prom-helper-x1", Owner: "Pod/prom-helper-x1"}
	cases := []struct {
		name    string
		callers []string
		status  string
		pods    []PodMesh
		tests   []PodMesh
		want    string
	}{
		{"named caller opted out", []string{"prom-test"}, "listed", []PodMesh{main, client}, []PodMesh{test}, ""},
		{"a testPodMesh pod that is not a hook pod is not a caller", []string{"prom-test"}, "listed", []PodMesh{main, client}, []PodMesh{test, notHook}, ""},
		{"naming a pod that is not a hook pod is refused", []string{"prom-test", "prom-helper"}, "listed", []PodMesh{main, client}, []PodMesh{test, notHook},
			`caller "prom-helper" is not a helm test hook pod`},
		{"unrelated opted-out Job, no test hook pod at all", []string{"prom-test"}, "listed", []PodMesh{job, main, client}, nil, "holds no helm test hook pod"},
		{"caller exists but is not opted out", []string{"prom-test"}, "listed", []PodMesh{main, client}, []PodMesh{testPod("prom-test-x1", false)}, "does not carry the opt-out"},
		{"opt-out for the other world does not apply", []string{"prom-test"}, "listed", []PodMesh{client},
			[]PodMesh{{Pod: "prom-test-x1", TestHook: true, MeshMetadata: optedOut("W3")}}, "does not carry the opt-out"},
		{"every named caller must be a hook pod", []string{"prom-test", "prom-other-test"}, "listed", []PodMesh{client}, []PodMesh{test}, `caller "prom-other-test" is not a helm test hook pod`},
		{"a prefix needs the dash boundary", []string{"prom-te"}, "listed", []PodMesh{client}, []PodMesh{test}, "a caller the row does not name"},
		{"a test pod listed in podMesh only is not a caller", []string{"prom-test"}, "listed", []PodMesh{test, client}, nil, "holds no helm test hook pod"},
		{"status no-new-pods: refused", []string{"prom-test"}, "no-new-pods", []PodMesh{main, client}, nil, `testPodMeshStatus is "no-new-pods"`},
		{"status not-run: refused", []string{"prom-test"}, "not-run", []PodMesh{main, client}, nil, `testPodMeshStatus is "not-run"`},
		{"status missing: refused", []string{"prom-test"}, "", []PodMesh{main, client}, []PodMesh{test}, `testPodMeshStatus is "none"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := optFixture(k, tc.callers, tc.status, tc.pods, tc.tests...)
			if tc.want == "" {
				res := f.analyze(t)
				if c := cellOf(t, res, "prom", "W1", k); c.Status != StExcluded || c.Reasons[0] != ledger.ReasonCOptout {
					t.Fatalf("%+v", c)
				}
				return
			}
			if msg := f.analyzeErr(t); !strings.Contains(msg, tc.want) {
				t.Fatal(msg)
			}
		})
	}
	t.Run("both runs are checked", func(t *testing.T) {
		f := optFixture(k, []string{"prom-test"}, "listed", []PodMesh{main, client}, test)
		f.in.Runs[1].Records = []Record{{World: "W1", Chart: "prom", Run: 1, PodMesh: []PodMesh{main, client}, TestPodMeshStatus: "listed"}}
		if msg := f.analyzeErr(t); !strings.Contains(msg, "r2") || !strings.Contains(msg, "holds no helm test hook pod") {
			t.Fatal(msg)
		}
	})
	t.Run("every record is checked, not the first", func(t *testing.T) {
		f := optFixture(k, []string{"prom-test"}, "listed", []PodMesh{main, client}, test)
		f.in.Runs[0].Records = append([]Record{}, f.in.Runs[0].Records...)
		f.in.Runs[0].Records[2].TestPodMesh = []PodMesh{test, testPod("prom-test-grpc-x2", false)}
		if msg := f.analyzeErr(t); !strings.Contains(msg, "prom-test-grpc-x2") {
			t.Fatal(msg)
		}
	})
	t.Run("a run without records for the chart and world", func(t *testing.T) {
		f := optFixture(k, []string{"prom-test"}, "listed", []PodMesh{main, client}, test)
		f.in.Runs[1].Records = nil
		if msg := f.analyzeErr(t); !strings.Contains(msg, "no record") {
			t.Fatal(msg)
		}
	})
}
