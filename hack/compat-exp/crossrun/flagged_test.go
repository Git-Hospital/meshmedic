package main

import (
	"strings"
	"testing"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

// Point 1 of "Open before the freeze": a key carrying needsNewKeyRule in
// causes.json (apiserver-caller) enters only by the new-key rule, like a key
// that is not in the file at all. These tests name the key only because
// mustCauses sets the flag on it, as the real file does; the code reads the flag.

const flaggedKey = "apiserver-caller"

func admittedKey(key string) ledger.NewKey {
	nk := ledger.NewKey{Key: key, Anchor: "istio/istio#00000", CertifiedFix: "x-fix", Reproduction: "hack/compat-exp/repro/x.sh"}
	nk.Reviewer = &struct {
		Verdict string `json:"verdict"`
		Run     string `json:"run"`
	}{ledger.KeyAdmit, "rv-key"}
	return nk
}

// flaggedFixture: two charts of two publishers, both with a held cell in W3
// credited to the flagged key, plus ten same charts for the static test.
func flaggedFixture(withRecord bool) *fixture {
	r := []ChartReport{chart("hit", wr("W3", bad(wk))), chart("hit2", wr("W3", bad(wk)))}
	var st []StaticResult
	st = append(st, stat("hit", "W3", "webhook-backend-in-mesh", true), stat("hit2", "W3", "webhook-backend-in-mesh", true))
	for _, n := range names(10, "s") {
		r = append(r, chart(n, wr("W3", ok(wk))))
		st = append(st, stat(n, "W3", "webhook-backend-in-mesh", false))
	}
	f := newFixture(r, r, nil)
	// the fixture's static check is named after the key; the real id is the check's name
	f.in.Causes.Static[flaggedKey] = "webhook-backend-in-mesh"
	if withRecord {
		f.in.Ledger.NewKeys = []ledger.NewKey{admittedKey(flaggedKey)}
	}
	f.withCerts(row("hit", "W3", wk, flaggedKey, "gap"), row("hit2", "W3", wk, flaggedKey, "gap"))
	f.in.Static, f.in.StaticGiven = st, true
	return f
}

func TestFlaggedKeyWithoutRecordIsAnErrorNamingTheRow(t *testing.T) {
	msg := flaggedFixture(false).analyzeErr(t)
	for _, want := range []string{"ledger row 1 (hit/W3/" + wk + ")", "ledger row 2 (hit2/W3/", "needs the new-key rule"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("want %q in:\n%s", want, msg)
		}
	}
}

func TestFlaggedKeyWithIncompleteRecordIsAnError(t *testing.T) {
	f := flaggedFixture(true)
	f.in.Ledger.NewKeys[0].Reproduction = ""
	if msg := f.analyzeErr(t); !strings.Contains(msg, "needs the new-key rule") {
		t.Fatal(msg)
	}
	f = flaggedFixture(true)
	f.in.Ledger.NewKeys[0].Reviewer.Verdict = ledger.KeyMerge
	if msg := f.analyzeErr(t); !strings.Contains(msg, "needs the new-key rule") {
		t.Fatal(msg)
	}
}

func TestFlaggedKeyWithAdmittedRecordCountsAndNeverEarnsS(t *testing.T) {
	res := flaggedFixture(true).analyze(t)
	if m := res.Metrics; m.N != 1 || m.P != 2 || m.S != 0 {
		t.Fatalf("a flagged key with an admitted record counts in N_new and P_new, never in S: %+v", m)
	}
	var sk *StaticKey
	for i := range res.Static {
		if res.Static[i].Key == flaggedKey {
			sk = &res.Static[i]
		}
	}
	if sk == nil {
		t.Fatalf("the static check is still run and reported: %+v", res.Static)
	}
	if sk.Qualifies || !sk.FlagsAll || sk.NotCountedReason != NotCountedNewKeyRule {
		t.Fatalf("flags every finding, labelled as not counting toward S: %+v", sk)
	}
	if !strings.Contains(string(RenderMarkdown(res)), "does not count toward S: admitted by the new-key rule") {
		t.Fatal(string(RenderMarkdown(res)))
	}
}

// the same fixture with a frozen key still earns S: the flag, not the check, decides
func TestFrozenKeyStillEarnsSInTheSameFixture(t *testing.T) {
	f := flaggedFixture(true)
	f.in.Causes.NewKeyRule = map[string]bool{}
	f.in.Ledger.NewKeys = nil
	if m := f.analyze(t).Metrics; m.S != 1 {
		t.Fatalf("%+v", m)
	}
}

func TestFlaggedKeyRanksAfterTheFrozenList(t *testing.T) {
	// a cell needing two keys' fixes, neither part clearing it alone: the first
	// key in rule 3's order. apiserver-caller sits before ambient-l7 in
	// causes.json, but as a new key it ranks after it.
	f := creditFixture(t)
	f.in.Ledger.NewKeys = []ledger.NewKey{admittedKey(flaggedKey)}
	r := creditRow(ck3, "ambient-l7", false, false)
	r.Parts[0].Cause, r.Parts[1].Cause = flaggedKey, "ambient-l7"
	f.in.Ledger.Rows = []ledger.Row{r}
	if c := cellOf(t, f.analyze(t), "a", "W3", ck3); c.Credit != "ambient-l7" || c.Status != StCounted {
		t.Fatalf("%+v", c)
	}
	// crediting it to the flagged key is the wrong credit
	f = creditFixture(t)
	f.in.Ledger.NewKeys = []ledger.NewKey{admittedKey(flaggedKey)}
	r = creditRow(ck3, flaggedKey, false, false)
	r.Parts[0].Cause, r.Parts[1].Cause = flaggedKey, "ambient-l7"
	f.in.Ledger.Rows = []ledger.Row{r}
	if msg := f.analyzeErr(t); !strings.Contains(msg, `rule 3 credits the cell to "ambient-l7"`) {
		t.Fatal(msg)
	}
	// among new keys, in admission order
	for _, order := range [][]string{{flaggedKey, "zz-new"}, {"zz-new", flaggedKey}} {
		f = creditFixture(t)
		f.in.Ledger.NewKeys = nil
		for _, k := range order {
			f.in.Ledger.NewKeys = append(f.in.Ledger.NewKeys, admittedKey(k))
		}
		r = creditRow(ck3, order[0], false, false)
		r.Parts[0].Cause, r.Parts[1].Cause = flaggedKey, "zz-new"
		f.in.Ledger.Rows = []ledger.Row{r}
		if c := cellOf(t, f.analyze(t), "a", "W3", ck3); c.Credit != order[0] {
			t.Fatalf("order %v: %+v", order, c)
		}
	}
}

func TestFlaggedKeyIsListedOnceInTheWorldUniversalRows(t *testing.T) {
	res := flaggedFixture(true).analyze(t)
	n := 0
	for _, u := range res.Universal {
		if u.Kind == "cause" && u.Name == flaggedKey {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d cause rows for the flagged key, want 1: %+v", n, res.Universal)
	}
}
