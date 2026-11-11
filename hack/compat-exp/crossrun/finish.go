package main

import (
	"fmt"
	"sort"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

// effectiveType is the row's type, or gap when the reviewer disagreed about it
// (section 2: a disagreement about type makes the cell a gap).
func effectiveType(r *ledger.Row) string {
	if r.Reviewer != nil && r.Reviewer.Verdict == ledger.VerdictDisagreeType {
		return ledger.TypeGap
	}
	return r.Type
}

type counted struct {
	cell      ledger.Cell
	key       string
	typ       string
	publisher string
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// metricsOf computes N_new, P_new, D and P_D over the given cells (ambient-np
// is left out) and S through flagged, which says whether a key's static check
// qualifies it.
func metricsOf(cells []counted, sOf func(key string, cs []counted) bool) (Metrics, []string) {
	keys := map[string]bool{}
	pubs := map[string]bool{}
	defKeys := map[string]bool{}
	defPubs := map[string]bool{}
	byKey := map[string][]counted{}
	for _, c := range cells {
		if c.key == AmbientNP {
			continue
		}
		keys[c.key] = true
		pubs[c.publisher] = true
		byKey[c.key] = append(byKey[c.key], c)
		if c.typ == ledger.TypeDefect {
			defKeys[c.key] = true
			defPubs[c.publisher] = true
		}
	}
	m := Metrics{N: len(keys), P: len(pubs), D: len(defKeys), PD: len(defPubs)}
	var sKeys []string
	for _, k := range sortedKeys(keys) {
		if sOf != nil && sOf(k, byKey[k]) {
			m.S++
			sKeys = append(sKeys, k)
		}
	}
	return m, sKeys
}

func (a *analysis) finish(res *Result, uworlds []UndecidedWorld) {
	in := a.in
	var cs []counted
	var amb []string
	ambCells := 0
	for _, cr := range a.cells {
		row := a.rowOf[cr.cell()]
		if row != nil && row.Cause != "" {
			cr.Type = effectiveType(row)
		}
		if row != nil && row.Cause != "" && cr.Credit == "" {
			cr.Credit = row.Cause
		}
		if cr.Status != StCounted {
			continue
		}
		cs = append(cs, counted{cr.cell(), cr.Credit, cr.Type, cr.Publisher})
		if cr.Credit == AmbientNP {
			ambCells++
			amb = append(amb, cr.Chart)
		}
	}
	ambSet := map[string]bool{}
	for _, c := range amb {
		ambSet[c] = true
	}
	res.Prevalence = Prevalence{Cells: ambCells, Charts: sortedKeys(ambSet)}

	// S
	res.SComputed = in.StaticGiven
	var sFn func(string, []counted) bool
	if in.StaticGiven {
		sFn = func(key string, cells []counted) bool {
			sk := a.staticKey(key, cells)
			res.Static = append(res.Static, sk)
			return sk.Qualifies
		}
	} else {
		res.Warnings = append(res.Warnings, "no static check results given: S is 0 and regions B and E are decided without it (S is a lower bound)")
	}
	res.Metrics, _ = metricsOf(cs, sFn)
	sort.Slice(res.Static, func(i, j int) bool { return res.Static[i].Key < res.Static[j].Key })
	res.Region = Region(res.Metrics)
	res.Trace = Trace(res.Metrics)

	// open cells
	pendingPubs := map[string]bool{}
	pending := 0
	undecided := 0
	var open []*CellResult
	for _, cr := range a.cells {
		switch cr.Status {
		case StUnresolved:
			res.Unresolved = append(res.Unresolved, cr.cell())
		case StUnexplained:
			res.Unexplained = append(res.Unexplained, cr.cell())
		}
		isPending := cr.Status == StUnresolved || cr.Status == StUnexplained || cr.Status == StAwaitRun2 ||
			(cr.Status == StExcluded && len(cr.Reasons) > 0 && cr.Reasons[0] == ledger.ReasonUndecided)
		if cr.Status == StExcluded && len(cr.Reasons) > 0 && cr.Reasons[0] == ledger.ReasonUndecided {
			undecided++
		}
		if isPending {
			pending++
			pendingPubs[cr.Publisher] = true
		}
		if cr.Status == StUnresolved || cr.Status == StUnexplained || cr.Status == StAwaitRun2 {
			open = append(open, cr)
		}
	}

	// futility
	pubsNow := map[string]bool{}
	defPubsNow := map[string]bool{}
	for _, c := range cs {
		if c.key != AmbientNP {
			pubsNow[c.publisher] = true
			if c.typ == ledger.TypeDefect {
				defPubsNow[c.publisher] = true
			}
		}
	}
	newPubs, newDefPubs := 0, 0
	for p := range pendingPubs {
		if !pubsNow[p] {
			newPubs++
		}
		if !defPubsNow[p] {
			newDefPubs++
		}
	}
	f := Futility{NMax: res.Metrics.N + pending, PMax: res.Metrics.P + newPubs, PendingCells: pending, PendingPublishers: newPubs}
	b := Bounds{Cur: res.Metrics, NMax: f.NMax, PMax: f.PMax, DMax: res.Metrics.D + pending, PDMax: res.Metrics.PD + newDefPubs, SMax: f.NMax}
	f.Reachable = Reachable(b)
	f.OnlyE = len(f.Reachable) == 1 && f.Reachable[0] == "E"
	f.StopDrop = f.NMax <= 1 || f.PMax <= 1
	f.PrevalenceCertificatesOff = !contains(f.Reachable, "A")
	res.Futility = f

	// undecided share
	res.Undecided = UndecidedShare{Undecided: undecided, Cells: len(a.cells),
		Over:        undecided*100 > UndecidedPercent*len(a.cells),
		NoteAfterRR: "the stop applies after one rerun; crossrun reports the share of this input only"}

	// if all open cells counted
	hyp := append([]counted{}, cs...)
	for _, cr := range open {
		row := a.rowOf[cr.cell()]
		key, typ := "unclassified:"+cr.cell().String(), ledger.TypeGap
		if row != nil && row.Cause != "" {
			key = row.Cause
			if cr.Credit != "" {
				key = cr.Credit
			}
			if t := effectiveType(row); t != "" {
				typ = t
			}
		}
		hyp = append(hyp, counted{cr.cell(), key, typ, cr.Publisher})
	}
	var hsFn func(string, []counted) bool
	if in.StaticGiven {
		hsFn = func(key string, cells []counted) bool {
			if len(key) > 13 && key[:13] == "unclassified:" {
				return false
			}
			return a.staticKey(key, cells).Qualifies
		}
	}
	hm, _ := metricsOf(hyp, hsFn)
	res.IfAllCounted = Hypothetical{Cells: len(open), Metrics: hm, Region: Region(hm)}

	// results
	for _, cr := range a.cells {
		res.Cells = append(res.Cells, *cr)
	}
	res.UndecidedWorld = uworlds
	sort.SliceStable(res.UndecidedWorld, func(i, j int) bool {
		x, y := res.UndecidedWorld[i], res.UndecidedWorld[j]
		if x.Run != y.Run {
			return x.Run < y.Run
		}
		if x.Chart != y.Chart {
			return x.Chart < y.Chart
		}
		return x.World < y.World
	})
	// opt-out evidence for asserted rows and for open held cells with opt-out pods
	seen := map[chartWorld]bool{}
	for i := range in.Ledger.Rows {
		r := &in.Ledger.Rows[i]
		if r.Status == ledger.StatusExcluded && r.Exclusion != nil && r.Exclusion.Reason == ledger.ReasonCOptout {
			cw := chartWorld{r.Chart, r.World}
			if !seen[cw] {
				seen[cw] = true
				ev, _ := a.optOutEvidence(r.Chart, r.World, false)
				ev.Asserted = true
				res.OptOut = append(res.OptOut, *ev)
			}
		}
	}
	for _, cr := range a.cells {
		cw := chartWorld{cr.Chart, cr.World}
		if seen[cw] || !(cr.Status == StUnresolved || cr.Status == StUnexplained || cr.Status == StCounted) {
			continue
		}
		ev, _ := a.optOutEvidence(cr.Chart, cr.World, false)
		if ev.Effective > 0 {
			seen[cw] = true
			res.OptOut = append(res.OptOut, *ev)
		}
	}
	sort.SliceStable(res.OptOut, func(i, j int) bool {
		if res.OptOut[i].Chart != res.OptOut[j].Chart {
			return res.OptOut[i].Chart < res.OptOut[j].Chart
		}
		return res.OptOut[i].World < res.OptOut[j].World
	})
	if in.Recheck == nil {
		n := 0
		for _, cr := range a.cells {
			if cr.Status == StCounted {
				n++
			}
		}
		if n > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("no recheck report given: the recheck claims of %d counted row(s) are not checked against a report", n))
		}
	}
	if res.Run2 == "" {
		res.Warnings = append(res.Warnings, "only run 1 given: no cell can hold under rule 1 yet; cells are awaiting run 2")
	}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
}

// staticKey runs the S test for one key over its counted cells.
func (a *analysis) staticKey(key string, cells []counted) StaticKey {
	check := a.checkID(key)
	sk := StaticKey{Key: key, Check: check, FlagsAll: true, Qualifies: true, Worlds: []StaticWorld{}, CountedFor: []ledger.Cell{}}
	worlds := map[string]bool{}
	for _, c := range cells {
		sk.CountedFor = append(sk.CountedFor, c.cell)
		worlds[c.cell.World] = true
		f, found := a.flagged(c.cell.Chart, c.cell.World, check)
		if !found {
			a.errf("static results lack check %q for %s/%s, which has a counted finding of key %s", check, c.cell.Chart, c.cell.World, key)
			continue
		}
		if !f {
			sk.FlagsAll = false
			sk.Unflagged = append(sk.Unflagged, c.cell)
		}
	}
	sort.Slice(sk.CountedFor, func(i, j int) bool { return cellLess(sk.CountedFor[i], sk.CountedFor[j]) })
	for _, w := range sortedKeys(worlds) {
		sw := StaticWorld{World: w}
		for _, ch := range a.qualifiedCharts() {
			same := true
			for _, v := range a.views {
				if v.verdict[chartWorld{ch, w}] != "same" {
					same = false
				}
			}
			if !same {
				continue
			}
			sw.SameCharts++
			f, found := a.flagged(ch, w, check)
			if !found {
				a.errf("static results lack check %q for %s/%s, a chart that was same in %s", check, ch, w, w)
				continue
			}
			if f {
				sw.FlaggedSame++
			}
		}
		sw.Ok = sw.FlaggedSame*100 <= StaticPercent*sw.SameCharts
		sk.Worlds = append(sk.Worlds, sw)
		if !sw.Ok {
			sk.Qualifies = false
		}
	}
	if !sk.FlagsAll {
		sk.Qualifies = false
	}
	return sk
}
