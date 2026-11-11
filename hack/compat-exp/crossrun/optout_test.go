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

// optFixture: chart "prom" in W1 with a finding of `key`, records with the given pods in both runs.
func optFixture(key string, callers []string, pods ...PodMesh) *fixture {
	r := []ChartReport{chart("prom", wr("W1", bad(key)))}
	f := newFixture(r, r, nil)
	f.pad(9)
	recs := []Record{}
	for run := 1; run <= 3; run++ {
		recs = append(recs, Record{World: "W1", Chart: "prom", Run: run, PodMesh: pods})
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
	f := optFixture(portKey, []string{"prom-admission-create"}, job, PodMesh{Pod: "prom-0", Owner: "StatefulSet/prom"}, PodMesh{Pod: "mmc-client", Client: true})
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

func TestCOptoutCallersAreChecked(t *testing.T) {
	const k = "helm:test"
	test := PodMesh{Pod: "prom-test-x1", Owner: "Pod/prom-test-x1", MeshMetadata: optedOut("W1")}
	main := PodMesh{Pod: "prom-0", Owner: "StatefulSet/prom"}
	job := PodMesh{Pod: "prom-admission-create-abcde", Owner: "Job/prom-admission-create", MeshMetadata: optedOut("W1")}
	client := PodMesh{Pod: "mmc-client", Client: true, MeshMetadata: optedOut("W1")}
	cases := []struct {
		name    string
		callers []string
		pods    []PodMesh
		want    string
	}{
		{"named caller opted out", []string{"prom-test"}, []PodMesh{test, main, client}, ""},
		{"unrelated opted-out Job, caller not in podMesh", []string{"prom-test"}, []PodMesh{job, main, client}, `caller "prom-test" is not in podMesh`},
		{"caller exists but is not opted out", []string{"prom"}, []PodMesh{test, main, client}, "does not carry the opt-out"},
		{"caller is the instrument's own client", []string{"mmc-client"}, []PodMesh{test, main, client}, "instrument's own client"},
		{"opt-out for the other world does not apply", []string{"prom-test"},
			[]PodMesh{{Pod: "prom-test-x1", MeshMetadata: optedOut("W3")}, client}, "does not carry the opt-out"},
		{"every named caller must exist", []string{"prom-test", "prom-other-test"}, []PodMesh{test, client}, `caller "prom-other-test" is not in podMesh`},
		{"a prefix needs the dash boundary", []string{"prom-te"}, []PodMesh{test, client}, `caller "prom-te" is not in podMesh`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := optFixture(k, tc.callers, tc.pods...)
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
		f := optFixture(k, []string{"prom-test"}, test, client)
		f.in.Runs[1].Records = []Record{{World: "W1", Chart: "prom", Run: 1, PodMesh: []PodMesh{main, client}}}
		if msg := f.analyzeErr(t); !strings.Contains(msg, "r2") || !strings.Contains(msg, `caller "prom-test"`) {
			t.Fatal(msg)
		}
	})
	t.Run("a run without records for the chart and world", func(t *testing.T) {
		f := optFixture(k, []string{"prom-test"}, test, client)
		f.in.Runs[1].Records = nil
		if msg := f.analyzeErr(t); !strings.Contains(msg, "no record") {
			t.Fatal(msg)
		}
	})
}

// The callers of helm:test are the pods helm test creates, which observe.sh records
// in testPodMesh (a second listing after helm test), not in podMesh.
func TestCOptoutCallersInTestPodMesh(t *testing.T) {
	const k = "helm:test"
	main := PodMesh{Pod: "prom-0", Owner: "StatefulSet/prom"}
	client := PodMesh{Pod: "mmc-client", Client: true, MeshMetadata: optedOut("W1")}
	testPod := PodMesh{Pod: "prom-test-x1", Owner: "Pod/prom-test-x1", MeshMetadata: optedOut("W1")}
	plain := PodMesh{Pod: "prom-test-x1", Owner: "Pod/prom-test-x1"}
	mk := func(pm, tpm []PodMesh, status string) *fixture {
		f := optFixture(k, []string{"prom-test"}, pm...)
		for i := range f.in.Runs {
			for j := range f.in.Runs[i].Records {
				f.in.Runs[i].Records[j].TestPodMesh = tpm
				f.in.Runs[i].Records[j].TestPodMeshStatus = status
			}
		}
		return f
	}
	t.Run("caller only in testPodMesh, opted out: accepted", func(t *testing.T) {
		res := mk([]PodMesh{main, client}, []PodMesh{testPod}, "listed").analyze(t)
		if c := cellOf(t, res, "prom", "W1", k); c.Status != StExcluded || c.Reasons[0] != ledger.ReasonCOptout {
			t.Fatalf("%+v", c)
		}
	})
	t.Run("caller in testPodMesh without the opt-out: refused", func(t *testing.T) {
		if msg := mk([]PodMesh{main, client}, []PodMesh{plain}, "listed").analyzeErr(t); !strings.Contains(msg, "does not carry the opt-out") {
			t.Fatal(msg)
		}
	})
	t.Run("caller in neither list: refused, the status is in the message", func(t *testing.T) {
		msg := mk([]PodMesh{main, client}, nil, "no-new-pods").analyzeErr(t)
		if !strings.Contains(msg, `caller "prom-test" is not in podMesh or testPodMesh`) || !strings.Contains(msg, "no-new-pods") {
			t.Fatal(msg)
		}
	})
	t.Run("a matching pod in either list must be opted out", func(t *testing.T) {
		optedMain := PodMesh{Pod: "prom-test-x1", Owner: "Pod/prom-test-x1", MeshMetadata: optedOut("W1")}
		if msg := mk([]PodMesh{optedMain, client}, []PodMesh{plain}, "listed").analyzeErr(t); !strings.Contains(msg, "does not carry the opt-out") {
			t.Fatal(msg)
		}
	})
}
