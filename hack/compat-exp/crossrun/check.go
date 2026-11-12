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
		a.checkNewKeyRule(i, r)
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

// checkNewKeyRule: a row may credit a key that carries needsNewKeyRule in
// causes.json only if the ledger also holds a complete, admitted new-key record
// for it, exactly as for a key that is not in causes.json (section 3). The
// ledger check says the same on load; this keeps the analysis itself from
// treating a flagged key as frozen when it is given a ledger directly.
func (a *analysis) checkNewKeyRule(i int, r *ledger.Row) {
	causes := []string{r.Cause}
	for _, p := range r.Parts {
		causes = append(causes, p.Cause)
	}
	seen := map[string]bool{}
	for _, k := range causes {
		if k == "" || seen[k] || !a.in.Causes.NeedsNewKeyRule(k) {
			continue
		}
		seen[k] = true
		if !ledger.AdmittedNewKey(a.in.Ledger, k) {
			a.errf("%s: cause %q needs the new-key rule (causes.json: needsNewKeyRule, no documentation anchor): it is credited only with a complete, admitted new-key record in the ledger's newKeys", rowLabel(i, *r), k)
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
	// rule 2. A cell that needs two keys' fixes (rule 3) is certified by the
	// combined entry: a part that leaves the cell differing cannot earn a
	// certificate for it, so the credited key's own part is not asked for one.
	if len(r.Parts) > 0 {
		switch {
		case r.Combined == nil:
			a.errf("%s: the row has parts but no combined entry", w)
		case r.Fix != nil && r.Fix.Entry != r.Combined.Entry:
			a.errf("%s: fix.entry %q is not the combined entry %q: with parts, rule 2's certificate is the combined fix's", w, r.Fix.Entry, r.Combined.Entry)
		default:
			for _, id := range ledger.Distinct(r.Combined.Certification) {
				a.checkCertificate(w, id, r.Combined.Entry, r)
			}
			if r.Runs != nil && !sameIDs(r.Runs.Certification, r.Combined.Certification) {
				a.errf("%s: runs.certification %v is not combined.certification %v", w, ledger.Distinct(r.Runs.Certification), ledger.Distinct(r.Combined.Certification))
			}
		}
	} else if r.Fix != nil && r.Runs != nil {
		for _, id := range ledger.Distinct(r.Runs.Certification) {
			a.checkCertificate(w, id, r.Fix.Entry, r)
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

func sameIDs(a, b []string) bool {
	x, y := ledger.Distinct(a), ledger.Distinct(b)
	if len(x) != len(y) {
		return false
	}
	for _, id := range x {
		if !contains(y, id) {
			return false
		}
	}
	return true
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
// and in every record (a) testPodMeshStatus is "listed", so the pods helm test
// created were captured, (b) the callers are ALL the pods of testPodMesh that
// are helm test hook pods (testHook true), not a list the analyst chooses; there
// is at least one, and each carries the opt-out metadata that applies to the
// world, and (c) the row's `callers` equals that set: every test hook pod is
// named by an entry (a pod name, or a prefix before a dash) and every entry
// names one. Pods of testPodMesh that are not test hook pods are not callers;
// podMesh, the snapshot before helm test, holds no caller.
func (a *analysis) checkCallers(r *ledger.Row) error {
	if r.Key != COptoutKey {
		return fmt.Errorf("c-optout is accepted for the observation key %s only (its callers are pods of the chart); the callers of %s are not", COptoutKey, r.Key)
	}
	named := r.Exclusion.Callers
	if len(named) == 0 {
		return fmt.Errorf("the row names no callers")
	}
	for _, run := range a.in.Runs {
		if !run.HasRecords {
			return fmt.Errorf("no records directory is given for run %s, so testPodMesh cannot be read", run.ID)
		}
		n := 0
		for _, rec := range run.Records {
			if rec.Chart != r.Chart || rec.World != r.World {
				continue
			}
			n++
			where := fmt.Sprintf("run %s record run%d", run.ID, rec.Run)
			if rec.TestPodMeshStatus != "listed" {
				return fmt.Errorf("testPodMeshStatus is %q in %s, not \"listed\": the pods helm test created were not captured (a test pod removed by a hook delete policy cannot be recorded), so the callers are not known", statusOrNone(rec.TestPodMeshStatus), where)
			}
			callers := rec.testHookPods()
			if len(callers) == 0 {
				return fmt.Errorf("testPodMesh of %s %s holds no helm test hook pod in %s, so there is no failing caller to be opted out", r.Chart, r.World, where)
			}
			for _, p := range callers {
				if p.Client {
					return fmt.Errorf("test hook pod %s is the instrument's own client pod, in %s", p.Pod, where)
				}
				opted := false
				for _, m := range p.MeshMetadata {
					if isOptOut(r.World, m.Key, m.Value) {
						opted = true
					}
				}
				if !opted {
					return fmt.Errorf("test hook pod %s does not carry the opt-out that applies in %s, in %s: every helm test pod is a caller, whether the row names it or not", p.Pod, r.World, where)
				}
				matched := false
				for _, c := range named {
					if ledger.CallerMatches(c, p.Pod) {
						matched = true
					}
				}
				if !matched {
					return fmt.Errorf("test hook pod %s is a caller the row does not name (callers %s), in %s: the callers are all the test hook pods of the chart, not a list the analyst chooses", p.Pod, strings.Join(named, ", "), where)
				}
			}
			for _, c := range named {
				matched := false
				for _, p := range callers {
					if ledger.CallerMatches(c, p.Pod) {
						matched = true
					}
				}
				if !matched {
					return fmt.Errorf("caller %q is not a helm test hook pod of testPodMesh of %s %s in %s (pods that are not test hook pods are not callers)", c, r.Chart, r.World, where)
				}
			}
		}
		if n == 0 {
			return fmt.Errorf("run %s has no record for %s in %s, so testPodMesh cannot be read", run.ID, r.Chart, r.World)
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
