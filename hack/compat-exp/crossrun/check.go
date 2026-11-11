package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

// checkRows compares every ledger row with the reports. It never changes a
// judgment field; a disagreement is an error naming the row.
func (a *analysis) checkRows() {
	in := a.in
	for i := range in.Ledger.Rows {
		r := &in.Ledger.Rows[i]
		w := rowLabel(i, *r)
		cr := a.byCell[r.Cell()]
		if cr == nil {
			a.errf("%s: the cell is not a finding in any run, and no run left it undecided with every used value differing", w)
			continue
		}
		if pub := in.Publisher[r.Chart]; r.Publisher != "" && r.Publisher != pub {
			a.errf("%s: publisher %q, the frame says %q", w, r.Publisher, pub)
		}
		if r.Cause != "" && cr.Credit != "" && cr.Credit != r.Cause {
			a.errf("%s: cause is %q but rule 3 credits the cell to %q from the part certificates", w, r.Cause, cr.Credit)
		}
		switch r.Status {
		case ledger.StatusCounted:
			a.checkCounted(w, r, cr)
		case ledger.StatusExcluded:
			a.checkExcluded(w, r, cr)
		}
	}
}

func (a *analysis) checkCounted(w string, r *ledger.Row, cr *CellResult) {
	in := a.in
	switch cr.Status {
	case StCounted:
	case StNotReproduce:
		a.errf("%s: counted, but the cell does not differ in both runs under rule 1 (%s)", w, cr.Detail)
		return
	case StAwaitRun2:
		a.errf("%s: counted, but only run 1 is given, rule 1 needs run 2", w)
		return
	case StExcluded:
		a.errf("%s: counted, but the cell is excluded by %s", w, strings.Join(cr.Reasons, ", "))
		return
	default:
		a.errf("%s: counted, but the cell resolved to %s", w, cr.Status)
		return
	}
	if r.Runs != nil {
		if r.Runs.Run1 != in.Runs[0].ID {
			a.errf("%s: runs.run1 is %s, the manifest's run 1 is %s", w, r.Runs.Run1, in.Runs[0].ID)
		}
		if len(in.Runs) == 2 && r.Runs.Run2 != in.Runs[1].ID {
			a.errf("%s: runs.run2 is %s, the manifest's run 2 is %s", w, r.Runs.Run2, in.Runs[1].ID)
		}
	}
	// rule 2
	if r.Fix != nil && r.Runs != nil {
		for _, id := range ledger.Distinct(r.Runs.Certification) {
			a.checkCertificate(w, id, r.Fix.Entry, r)
		}
	}
	if r.Combined != nil {
		for _, id := range ledger.Distinct(r.Combined.Certification) {
			a.checkCertificate(w, id, r.Combined.Entry, r)
		}
	}
	// rule 4
	if r.Recheck != nil && in.Recheck != nil {
		if r.Recheck.Run != in.Recheck.ID {
			a.errf("%s: recheck.run is %s, the manifest's recheck run is %s", w, r.Recheck.Run, in.Recheck.ID)
		}
		if in.Recheck.Istio != "" && in.Recheck.Istio != r.Recheck.Version {
			a.errf("%s: recheck.version is %s, the manifest's recheck ran Istio %s", w, r.Recheck.Version, in.Recheck.Istio)
		}
		ch := findChart(in.Recheck.Report, r.Chart)
		var kr *KeyReport
		if ch != nil {
			kr = findKey(ch, r.World, r.Key)
		}
		if kr == nil || kr.Verdict != "differs" {
			a.errf("%s: recheck.result says it reproduces, the recheck report does not show the cell differing", w)
		}
	}
}

func (a *analysis) checkCertificate(w, runID, entry string, r *ledger.Row) {
	ci := a.certRun(runID)
	if ci == nil {
		a.errf("%s: certification run %s is not in the manifest", w, runID)
		return
	}
	ch := findChart(ci.Report, entry)
	if ch == nil {
		a.errf("%s: fix entry %q is not in certification run %s", w, entry, runID)
		return
	}
	if ch.FixOf != r.Chart {
		a.errf("%s: fix entry %q has fixOf %q in run %s, the cell's chart is %q", w, entry, ch.FixOf, runID, r.Chart)
		return
	}
	c := ch.Certificate
	if c == nil {
		a.errf("%s: run %s has no certificate for %q", w, runID, entry)
		return
	}
	if c.Base != r.Chart {
		a.errf("%s: the certificate of %q in run %s repairs %q, not %q", w, entry, runID, c.Base, r.Chart)
	}
	if !c.Certified {
		a.errf("%s: the certificate of %q in run %s is not earned: %s", w, entry, runID, strings.Join(c.Problems, "; "))
		return
	}
	if !contains(c.CellWorlds, r.World) {
		a.errf("%s: the certificate of %q in run %s covers worlds %v, not %s", w, entry, runID, c.CellWorlds, r.World)
	}
	if !contains(c.Cleared, r.World) {
		a.errf("%s: the certificate of %q in run %s does not list %s as cleared", w, entry, runID, r.World)
	}
	for _, ig := range c.Ignored {
		if ig.World == r.World && ig.Key == r.Key && (ig.Chart == r.Chart || ig.Chart == entry) {
			a.errf("%s: the certificate of %q in run %s ignored this very cell (%s)", w, entry, runID, ig.Reason)
		}
	}
	for _, ig := range c.WorldsNotCompared {
		if ig.World == r.World {
			a.errf("%s: the certificate of %q in run %s passed world %s without comparing it", w, entry, runID, r.World)
		}
	}
}

func (a *analysis) checkExcluded(w string, r *ledger.Row, cr *CellResult) {
	reason := r.Exclusion.Reason
	switch reason {
	case ledger.ReasonCOptout:
		if err := a.checkCallers(r); err != nil {
			a.errf("%s: c-optout asserted but %v", w, err)
		}
	default:
		if !contains(cr.Reasons, reason) {
			a.errf("%s: excluded as %s, the reports do not show it (cell reasons from the reports: %s)", w, reason, listOrNone(cr.Reasons))
		}
	}
}

func statusOrNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func listOrNone(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return strings.Join(l, ", ")
}

// isOptOut reports whether the metadata is the chart's own mesh opt-out that
// is effective in the world: sidecar.istio.io/inject=false in a sidecar world,
// istio.io/dataplane-mode=none in an ambient one.
func isOptOut(world, key, value string) bool {
	if ambientWorlds[world] {
		return key == "istio.io/dataplane-mode" && value == "none"
	}
	return key == "sidecar.istio.io/inject" && value == "false"
}

// COptoutKey is the one observation key a c-optout exclusion may be accepted
// for: the helm test, whose callers are pods of the chart. The callers of the
// other keys are the instrument's own client (ports), nobody (workloads, jobs,
// late) or kube-apiserver (apiserver); none of them is "the chart's own pod
// with the chart's own opt-out" (section 2, rule 5).
const COptoutKey = "helm:test"

// checkCallers verifies a c-optout row against the records of both sample
// runs: the key is helm:test, every run has records for the chart and world,
// and in every record each caller the row names matches at least one pod of
// the chart, every matching pod is not the instrument's client and carries
// the opt-out metadata that applies to the world. The callers are looked for in
// podMesh and in testPodMesh (the pods helm test created). A pod that merely carries
// opt-out metadata but is not a named caller proves nothing.
func (a *analysis) checkCallers(r *ledger.Row) error {
	if r.Key != COptoutKey {
		return fmt.Errorf("c-optout is accepted for the observation key %s only (its callers are pods of the chart); the callers of %s are not", COptoutKey, r.Key)
	}
	callers := r.Exclusion.Callers
	if len(callers) == 0 {
		return fmt.Errorf("the row names no callers")
	}
	for _, run := range a.in.Runs {
		if !run.HasRecords {
			return fmt.Errorf("no records directory is given for run %s, so podMesh and testPodMesh cannot be read", run.ID)
		}
		n := 0
		for _, rec := range run.Records {
			if rec.Chart != r.Chart || rec.World != r.World {
				continue
			}
			n++
			for _, c := range callers {
				matched := 0
				for _, p := range rec.callerPods() {
					if !ledger.CallerMatches(c, p.Pod) {
						continue
					}
					matched++
					if p.Client {
						return fmt.Errorf("caller %q matches %s, the instrument's own client pod, in run %s record run%d", c, p.Pod, run.ID, rec.Run)
					}
					opted := false
					for _, m := range p.MeshMetadata {
						if isOptOut(r.World, m.Key, m.Value) {
							opted = true
						}
					}
					if !opted {
						return fmt.Errorf("caller %q matches %s, which does not carry the opt-out that applies in %s, in run %s record run%d", c, p.Pod, r.World, run.ID, rec.Run)
					}
				}
				if matched == 0 {
					return fmt.Errorf("caller %q is not in podMesh or testPodMesh of %s %s in run %s record run%d (testPodMeshStatus %q: a test pod removed by a hook delete policy cannot be recorded)",
						c, r.Chart, r.World, run.ID, rec.Run, statusOrNone(rec.TestPodMeshStatus))
				}
			}
		}
		if n == 0 {
			return fmt.Errorf("run %s has no record for %s in %s, so podMesh and testPodMesh cannot be read", run.ID, r.Chart, r.World)
		}
	}
	return nil
}

// optOutEvidence reads podMesh and testPodMesh from the sample runs' records for a chart and
// world. With require, it errors unless a non-client pod carries an effective
// opt-out.
func (a *analysis) optOutEvidence(chart, world string, require bool) (*OptOutEvidence, error) {
	ev := &OptOutEvidence{Chart: chart, World: world, Pods: []OptOutPod{}}
	have := false
	for _, run := range a.in.Runs {
		if !run.HasRecords {
			continue
		}
		have = true
		for _, rec := range run.Records {
			if rec.Chart != chart || rec.World != world {
				continue
			}
			for i, p := range rec.callerPods() {
				op := OptOutPod{Test: i >= len(rec.PodMesh), Run: fmt.Sprintf("%s run%d", run.ID, rec.Run), Pod: p.Pod, Owner: p.Owner, Client: p.Client,
					Proxy: p.IstioProxy.Container || p.IstioProxy.InitContainer, Redirect: p.AmbientRedirection, Metadata: []string{}}
				for _, m := range p.MeshMetadata {
					op.Metadata = append(op.Metadata, fmt.Sprintf("%s %s=%s", m.Kind, m.Key, m.Value))
					if isOptOut(world, m.Key, m.Value) {
						op.Effective = true
					}
				}
				if op.Effective && !p.Client {
					ev.Effective++
				}
				ev.Pods = append(ev.Pods, op)
			}
		}
	}
	sort.SliceStable(ev.Pods, func(i, j int) bool {
		if ev.Pods[i].Run != ev.Pods[j].Run {
			return ev.Pods[i].Run < ev.Pods[j].Run
		}
		return ev.Pods[i].Pod < ev.Pods[j].Pod
	})
	if require {
		if !have {
			return nil, fmt.Errorf("no records directory is given for any sample run, so podMesh and testPodMesh cannot be read")
		}
		if len(ev.Pods) == 0 {
			return nil, fmt.Errorf("the records hold no podMesh for %s in %s", chart, world)
		}
		if ev.Effective == 0 {
			return nil, fmt.Errorf("no non-client pod of %s in %s carries the mesh opt-out for that world", chart, world)
		}
	}
	return ev, nil
}
