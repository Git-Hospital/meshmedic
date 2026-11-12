package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testCauses(t *testing.T) *Causes {
	t.Helper()
	c, err := LoadCauses(filepath.Join("..", "causes.json"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func countedRow() Row {
	return Row{
		Chart: "redis", World: "W3", Key: "workload:StatefulSet/redis-replicas:ready",
		Cause: "ambient-np", Type: TypeDefect,
		TypeBasis: &TypeBasis{Doc: "extraIngress is a generic key"},
		Fix:       &Fix{Entry: "redis-np-15008", Form: FormValues, HelmLint: true},
		Runs:      &Runs{Run1: "100", Run2: "200", Certification: []string{"300", "400"}},
		Recheck:   &Recheck{Version: "1.32.0", Result: RecheckReproduces, Run: "500"},
		Upstream:  &Upstream{},
		Reviewer:  &Reviewer{Verdict: VerdictAgree, Run: "600"},
		Status:    StatusCounted, Publisher: "bitnami",
	}
}

// withParts makes the row a combined-fix row: rule 2's certificate is the
// combined entry's, and each part (named after its cause) was run alone.
func withParts(r *Row, c1, c2 string) *Row {
	no := false
	r.Cause = c1
	r.Fix.Entry = "redis-both"
	r.Combined = &Combined{Entry: "redis-both", Certification: []string{"300", "400"}}
	r.Parts = []Part{
		{Cause: c1, Entry: "redis-" + c1, Runs: []string{"310", "410"}, ClearsAlone: &no},
		{Cause: c2, Entry: "redis-" + c2, Runs: []string{"320", "420"}, ClearsAlone: &no},
	}
	return r
}

func newKey() NewKey {
	nk := NewKey{Key: "tls-origination", Anchor: "traffic-management/tls", CertifiedFix: "x-fix", Reproduction: "hack/compat-exp/repro/x.sh"}
	nk.Reviewer = &struct {
		Verdict string `json:"verdict"`
		Run     string `json:"run"`
	}{KeyAdmit, "700"}
	return nk
}

func TestCheck(t *testing.T) {
	causes := testCauses(t)
	cases := []struct {
		name string
		mod  func(f *File)
		want string // substring of the single expected problem; "" means valid
	}{
		{"valid counted", func(f *File) {}, ""},
		{"empty ledger", func(f *File) { f.Rows = nil }, ""},
		{"bad schema", func(f *File) { f.Schema = 2 }, "schema is 2"},
		{"unknown cause", func(f *File) { f.Rows[0].Cause = "made-up" }, `cause "made-up" is not in causes.json`},
		{"new key complete", func(f *File) {
			f.NewKeys = []NewKey{newKey()}
			f.Rows[0].Cause = "tls-origination"
		}, ""},
		{"new key without anchor", func(f *File) {
			nk := newKey()
			nk.Anchor = ""
			f.NewKeys = []NewKey{nk}
			f.Rows[0].Cause = "tls-origination"
		}, "anchor"},
		{"new key without fix", func(f *File) {
			nk := newKey()
			nk.CertifiedFix = ""
			f.NewKeys = []NewKey{nk}
		}, "certifiedFix"},
		{"new key without reproduction", func(f *File) {
			nk := newKey()
			nk.Reproduction = ""
			f.NewKeys = []NewKey{nk}
		}, "reproduction"},
		{"new key without reviewer", func(f *File) {
			nk := newKey()
			nk.Reviewer = nil
			f.NewKeys = []NewKey{nk}
		}, "reviewer verdict and run are missing"},
		{"new key merged by reviewer", func(f *File) {
			nk := newKey()
			nk.Reviewer.Verdict = KeyMerge
			f.NewKeys = []NewKey{nk}
		}, "admitted only on"},
		{"new key duplicates frozen key", func(f *File) {
			nk := newKey()
			nk.Key = "protocol"
			f.NewKeys = []NewKey{nk}
		}, "already in causes.json"},
		{"flagged key credited without a new-key record", func(f *File) { f.Rows[0].Cause = "apiserver-caller" }, `cause "apiserver-caller" needs the new-key rule`},
		{"flagged key as the cause of a part, without a record", func(f *File) {
			withParts(&f.Rows[0], "protocol", "apiserver-caller")
		}, `parts[1].cause "apiserver-caller" needs the new-key rule`},
		{"flagged key with a complete new-key record", func(f *File) {
			nk := newKey()
			nk.Key = "apiserver-caller"
			f.NewKeys = []NewKey{nk}
			f.Rows[0].Cause = "apiserver-caller"
		}, ""},
		{"flagged key with a record that lacks the reproduction", func(f *File) {
			nk := newKey()
			nk.Key = "apiserver-caller"
			nk.Reproduction = ""
			f.NewKeys = []NewKey{nk}
			f.Rows[0].Cause = "apiserver-caller"
		}, "reproduction"},
		{"flagged key with a record the reviewer merged", func(f *File) {
			nk := newKey()
			nk.Key = "apiserver-caller"
			nk.Reviewer.Verdict = KeyMerge
			f.NewKeys = []NewKey{nk}
			f.Rows[0].Cause = "apiserver-caller"
		}, "admitted only on"},
		{"duplicate cell", func(f *File) { f.Rows = append(f.Rows, f.Rows[0]) }, "same cell as row 1"},
		{"bad status", func(f *File) { f.Rows[0].Status = "done" }, "status"},
		{"bad type", func(f *File) { f.Rows[0].Type = "bug" }, "type"},
		{"counted without cause", func(f *File) { f.Rows[0].Cause = "" }, "needs cause"},
		{"counted without type", func(f *File) { f.Rows[0].Type = "" }, "needs type"},
		{"counted without type basis", func(f *File) { f.Rows[0].TypeBasis = nil }, "typeBasis"},
		{"counted without fix", func(f *File) { f.Rows[0].Fix = nil }, "needs fix"},
		{"counted without lint", func(f *File) { f.Rows[0].Fix.HelmLint = false }, "helmLint"},
		{"patch without sha", func(f *File) { f.Rows[0].Fix.Form = FormPatch }, "patchSha256"},
		{"patch with sha", func(f *File) {
			f.Rows[0].Fix.Form = FormPatch
			f.Rows[0].Fix.PatchSHA256 = strings.Repeat("a", 64)
		}, ""},
		{"values with sha", func(f *File) { f.Rows[0].Fix.PatchSHA256 = strings.Repeat("a", 64) }, "fix.form is values"},
		{"bad form", func(f *File) { f.Rows[0].Fix.Form = "yaml" }, "fix.form"},
		{"counted without run2", func(f *File) { f.Rows[0].Runs.Run2 = "" }, "run1 and runs.run2"},
		{"runs equal", func(f *File) { f.Rows[0].Runs.Run2 = "100" }, "same workflow run"},
		{"one certification run", func(f *File) { f.Rows[0].Runs.Certification = []string{"300"} }, "two distinct"},
		{"same certification run twice", func(f *File) { f.Rows[0].Runs.Certification = []string{"300", "300"} }, "two distinct"},
		{"no recheck", func(f *File) { f.Rows[0].Recheck = nil }, "recheck"},
		{"recheck not reproduced", func(f *File) { f.Rows[0].Recheck.Result = RecheckNotReproduced }, "rule 4 needs"},
		{"bad recheck result", func(f *File) { f.Rows[0].Recheck.Result = "maybe" }, "recheck.result"},
		{"upstream missing", func(f *File) { f.Rows[0].Upstream = nil }, "needs upstream"},
		{"upstream merged", func(f *File) { f.Rows[0].Upstream = &Upstream{Merged: true, Link: "https://x"} }, "merged upstream"},
		{"upstream merged without link", func(f *File) {
			f.Rows[0].Status = StatusUnexplain
			f.Rows[0].Upstream = &Upstream{Merged: true}
		}, "upstream.link"},
		{"no reviewer", func(f *File) { f.Rows[0].Reviewer = nil }, "reviewer"},
		{"reviewer disagree-other", func(f *File) { f.Rows[0].Reviewer.Verdict = VerdictDisagreeOth }, "disagree-other"},
		{"reviewer disagree-type is allowed", func(f *File) {
			f.Rows[0].Reviewer.Verdict = VerdictDisagreeType
			f.Rows[0].Type = TypeGap
		}, ""},
		{"bad reviewer verdict", func(f *File) { f.Rows[0].Reviewer.Verdict = "yes" }, "reviewer.verdict"},
		{"no publisher", func(f *File) { f.Rows[0].Publisher = "" }, "publisher"},
		{"counted with exclusion", func(f *File) { f.Rows[0].Exclusion = &Exclusion{Reason: ReasonFlaky, Evidence: "x"} }, "counted row has an exclusion"},
		{"excluded ok", func(f *File) {
			f.Rows[0] = Row{Chart: "podinfo", World: "W1", Key: "helm:test", Status: StatusExcluded,
				Exclusion: &Exclusion{Reason: ReasonCOptout, Evidence: "test pods carry sidecar.istio.io/inject=false", Callers: []string{"podinfo-grpc-test"}},
				Reviewer:  &Reviewer{Verdict: VerdictAgree, Run: "9"}}
		}, ""},
		{"c-optout without callers", func(f *File) {
			f.Rows[0] = Row{Chart: "podinfo", World: "W1", Key: "helm:test", Status: StatusExcluded,
				Exclusion: &Exclusion{Reason: ReasonCOptout, Evidence: "x"}, Reviewer: &Reviewer{Verdict: VerdictAgree, Run: "9"}}
		}, "exclusion.callers"},
		{"c-optout with a bad caller name", func(f *File) {
			f.Rows[0] = Row{Chart: "podinfo", World: "W1", Key: "helm:test", Status: StatusExcluded,
				Exclusion: &Exclusion{Reason: ReasonCOptout, Evidence: "x", Callers: []string{"Pod Name"}}, Reviewer: &Reviewer{Verdict: VerdictAgree, Run: "9"}}
		}, "not a pod name"},
		{"c-optout names a caller twice", func(f *File) {
			f.Rows[0] = Row{Chart: "podinfo", World: "W1", Key: "helm:test", Status: StatusExcluded,
				Exclusion: &Exclusion{Reason: ReasonCOptout, Evidence: "x", Callers: []string{"a-test", "a-test"}}, Reviewer: &Reviewer{Verdict: VerdictAgree, Run: "9"}}
		}, "twice"},
		{"callers on another reason", func(f *File) {
			f.Rows[0] = Row{Chart: "podinfo", World: "W1", Key: "helm:test", Status: StatusExcluded,
				Exclusion: &Exclusion{Reason: ReasonFlaky, Evidence: "x", Callers: []string{"a-test"}}}
		}, "only for c-optout"},
		{"c-optout without a reviewer", func(f *File) {
			f.Rows[0] = Row{Chart: "podinfo", World: "W1", Key: "helm:test", Status: StatusExcluded,
				Exclusion: &Exclusion{Reason: ReasonCOptout, Evidence: "x", Callers: []string{"a-test"}}}
		}, "reviewer verdict agree"},
		{"c-optout with reviewer disagree-other", func(f *File) {
			f.Rows[0] = Row{Chart: "podinfo", World: "W1", Key: "helm:test", Status: StatusExcluded,
				Exclusion: &Exclusion{Reason: ReasonCOptout, Evidence: "x", Callers: []string{"a-test"}}, Reviewer: &Reviewer{Verdict: VerdictDisagreeOth, Run: "9"}}
		}, "reviewer verdict agree"},
		{"world-universal without a reviewer", func(f *File) {
			f.Rows[0] = Row{Chart: "a", World: "W1", Key: "port:s:80:http", Status: StatusExcluded,
				Exclusion: &Exclusion{Reason: ReasonWorldUniversal, Evidence: "x"}}
		}, "reviewer verdict agree"},
		{"world-universal with reviewer agree", func(f *File) {
			f.Rows[0] = Row{Chart: "a", World: "W1", Key: "port:s:80:http", Status: StatusExcluded,
				Exclusion: &Exclusion{Reason: ReasonWorldUniversal, Evidence: "x"}, Reviewer: &Reviewer{Verdict: VerdictAgree, Run: "9"}}
		}, ""},
		{"a mechanical exclusion needs no reviewer", func(f *File) {
			f.Rows[0] = Row{Chart: "a", World: "W1", Key: "k", Status: StatusExcluded, Exclusion: &Exclusion{Reason: ReasonFlaky, Evidence: "x"}}
		}, ""},
		{"excluded without reason", func(f *File) {
			f.Rows[0] = Row{Chart: "podinfo", World: "W1", Key: "helm:test", Status: StatusExcluded}
		}, "needs exclusion.reason"},
		{"excluded without evidence", func(f *File) {
			f.Rows[0] = Row{Chart: "podinfo", World: "W1", Key: "helm:test", Status: StatusExcluded,
				Exclusion: &Exclusion{Reason: ReasonCOptout}}
		}, "needs exclusion.evidence"},
		{"excluded with invented reason", func(f *File) {
			f.Rows[0] = Row{Chart: "podinfo", World: "W1", Key: "helm:test", Status: StatusExcluded,
				Exclusion: &Exclusion{Reason: "too-hard", Evidence: "x"}}
		}, "rule 5 reasons"},
		{"unexplained with exclusion", func(f *File) {
			f.Rows[0] = Row{Chart: "a", World: "W1", Key: "k", Status: StatusUnexplain,
				Exclusion: &Exclusion{Reason: ReasonFlaky, Evidence: "x"}}
		}, "exclusion is set"},
		{"unexplained minimal", func(f *File) {
			f.Rows[0] = Row{Chart: "a", World: "W1", Key: "k", Status: StatusUnexplain}
		}, ""},
		{"unresolved minimal", func(f *File) {
			f.Rows[0] = Row{Chart: "a", World: "W1", Key: "k", Status: StatusUnresolved}
		}, ""},
		{"missing cell fields", func(f *File) { f.Rows[0].Key = "" }, "chart, world and key"},
		{"parts ok", func(f *File) { withParts(&f.Rows[0], "ambient-np", "protocol") }, ""},
		{"parts credit two keys", func(f *File) {
			r := withParts(&f.Rows[0], "protocol", "protocol")
			r.Parts[1].Entry = "q"
		}, "repeated"},
		{"cause outside parts", func(f *File) {
			r := withParts(&f.Rows[0], "protocol", "uid-1337")
			r.Cause = "ambient-np"
		}, "not one of the parts"},
		{"parts without combined", func(f *File) {
			r := withParts(&f.Rows[0], "ambient-np", "protocol")
			r.Combined = nil
		}, "combined"},
		{"fix is a part entry, not the combined entry", func(f *File) {
			r := withParts(&f.Rows[0], "ambient-np", "protocol")
			r.Fix.Entry = "redis-ambient-np"
		}, "not the combined entry"},
		{"runs.certification is not the combined runs", func(f *File) {
			r := withParts(&f.Rows[0], "ambient-np", "protocol")
			r.Runs.Certification = []string{"301", "401"}
		}, "must be combined.certification"},
		{"a part without clearsAlone", func(f *File) {
			r := withParts(&f.Rows[0], "ambient-np", "protocol")
			r.Parts[1].ClearsAlone = nil
		}, "clearsAlone is missing"},
		{"a part entry that is the combined entry", func(f *File) {
			r := withParts(&f.Rows[0], "ambient-np", "protocol")
			r.Parts[0].Entry = "redis-both"
		}, "is the combined entry"},
		{"the same part entry twice", func(f *File) {
			r := withParts(&f.Rows[0], "ambient-np", "protocol")
			r.Parts[1].Entry = r.Parts[0].Entry
		}, "repeated"},
		{"combined without parts", func(f *File) { f.Rows[0].Combined = &Combined{Entry: "x"} }, "without parts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &File{Schema: Schema, Rows: []Row{countedRow()}}
			tc.mod(f)
			ps := Check(f, causes)
			if tc.want == "" {
				if len(ps) != 0 {
					t.Fatalf("want valid, got %v", ps)
				}
				return
			}
			if len(ps) == 0 {
				t.Fatalf("want a problem containing %q, got none", tc.want)
			}
			for _, p := range ps {
				if strings.Contains(p.Error(), tc.want) {
					if len(f.Rows) > 0 && !strings.Contains(p.Where, "row 1 (") && !strings.HasPrefix(p.Where, "newKeys") && p.Where != "ledger" && !strings.Contains(p.Where, "row 2") {
						t.Fatalf("problem does not name its row: %v", p)
					}
					return
				}
			}
			t.Fatalf("no problem contains %q: %v", tc.want, ps)
		})
	}
}

func TestParseStrict(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"empty valid", `{"schema":1,"rows":[],"newKeys":[]}`, ""},
		{"unknown top field", `{"schema":1,"rows":[],"newKeys":[],"extra":1}`, "unknown field"},
		{"unknown row field", `{"schema":1,"rows":[{"chart":"a","world":"W1","key":"k","status":"unresolved","bogus":1}],"newKeys":[]}`, "unknown field"},
		{"unknown nested field", `{"schema":1,"rows":[{"chart":"a","world":"W1","key":"k","status":"counted","fix":{"entry":"x","form":"values","helmLint":true,"oops":1}}],"newKeys":[]}`, "unknown field"},
		{"trailing data", `{"schema":1,"rows":[],"newKeys":[]} {}`, "trailing"},
		{"not json", `rows`, "ledger:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseCauses(t *testing.T) {
	cases := []struct {
		name, doc string
		want      []string
		err       string
	}{
		{"keys", `{"keys":[{"key":"z","worlds":["W1"]},{"key":"a","worlds":["W3"],"staticCheck":"sa"}]}`, []string{"z", "a"}, ""},
		{"a bare array is not the file's shape", `[{"key":"a","worlds":["W1"]}]`, nil, "list under keys"},
		{"a causes wrapper is not the file's shape", `{"causes":[{"key":"a","worlds":["W1"]}]}`, nil, "no keys"},
		{"duplicate", `{"keys":[{"key":"a","worlds":["W1"]},{"key":"a","worlds":["W1"]}]}`, nil, "repeated"},
		{"empty key", `{"keys":[{"key":"","worlds":["W1"]}]}`, nil, "no key"},
		{"no worlds", `{"keys":[{"key":"a"}]}`, nil, "no worlds"},
		{"empty", `{"keys":[]}`, nil, "no keys"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseCauses([]byte(tc.doc))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(c.Keys, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v", c.Keys)
			}
		})
	}
	c, _ := ParseCauses([]byte(`{"keys":[{"key":"a","worlds":["W1"],"staticCheck":null},{"key":"b","worlds":["W1","W3"],"staticCheck":"sb"}]}`))
	if _, has := c.Static["a"]; has || c.Static["b"] != "sb" {
		t.Fatalf("static checks %v", c.Static)
	}
	if len(c.Worlds["b"]) != 2 {
		t.Fatalf("worlds %v", c.Worlds)
	}
}

// TestRealCauses reads the committed causes.json, the contract of every tool.
func TestRealCauses(t *testing.T) {
	c := testCauses(t)
	if len(c.Keys) != 9 || c.Keys[0] != "ambient-np" {
		t.Fatalf("keys %v", c.Keys)
	}
	if c.Static["ambient-np"] != "np-hbone-probe" || len(c.Worlds["ambient-np"]) != 1 || c.Worlds["ambient-np"][0] != "W3" {
		t.Fatalf("ambient-np: %q %v", c.Static["ambient-np"], c.Worlds["ambient-np"])
	}
	if _, has := c.Static["bind-address"]; has {
		t.Fatal("bind-address has staticCheck null, so no check")
	}
}

func TestFlaggedKeys(t *testing.T) {
	c := testCauses(t)
	if !c.NeedsNewKeyRule("apiserver-caller") || c.NeedsNewKeyRule("protocol") || c.NeedsNewKeyRule("made-up") {
		t.Fatal("only apiserver-caller carries needsNewKeyRule in causes.json")
	}
	if c.Frozen("apiserver-caller") || !c.Frozen("protocol") || c.Frozen("made-up") {
		t.Fatal("a flagged key is in causes.json and not frozen")
	}
	if !c.Has("apiserver-caller") {
		t.Fatal("Has still means: the key is in causes.json")
	}
	if got := c.FrozenKeys(); len(got) != 8 || got[7] != "ambient-l7" {
		t.Fatalf("frozen list %v", got)
	}
}

func TestParseCausesFlag(t *testing.T) {
	c, err := ParseCauses([]byte(`{"keys":[{"key":"a","worlds":["W1"]},{"key":"b","worlds":["W1"],"needsNewKeyRule":true},{"key":"c","worlds":["W1"],"needsNewKeyRule":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.NeedsNewKeyRule("a") || !c.NeedsNewKeyRule("b") || c.NeedsNewKeyRule("c") {
		t.Fatalf("%v", c.NewKeyRule)
	}
}

func TestOrderFlaggedKeyRanksAmongNewKeys(t *testing.T) {
	c := testCauses(t)
	// admitted second: after the frozen list, after the key admitted first
	f := &File{NewKeys: []NewKey{{Key: "n1"}, {Key: "apiserver-caller"}, {Key: "n2"}}}
	if !(c.Order(f, "ambient-l7") < c.Order(f, "n1") && c.Order(f, "n1") < c.Order(f, "apiserver-caller") && c.Order(f, "apiserver-caller") < c.Order(f, "n2")) {
		t.Fatal("a flagged key ranks among the new keys in the order they are admitted")
	}
	for _, k := range []string{"ambient-np", "bind-address", "job-lifecycle", "ambient-l7"} {
		if c.Order(f, "apiserver-caller") <= c.Order(f, k) {
			t.Fatalf("apiserver-caller ranks before frozen %s", k)
		}
	}
	// no record: after every frozen and every recorded key
	g := &File{NewKeys: []NewKey{{Key: "n1"}}}
	if !(c.Order(g, "apiserver-caller") > c.Order(g, "n1")) {
		t.Fatal("an unrecorded flagged key ranks after the recorded new keys")
	}
}

func TestAdmittedNewKey(t *testing.T) {
	f := &File{NewKeys: []NewKey{newKey()}}
	if !AdmittedNewKey(f, "tls-origination") || AdmittedNewKey(f, "protocol") {
		t.Fatal("complete record admitted, other keys not")
	}
	nk := newKey()
	nk.Reviewer.Verdict = KeyReject
	if AdmittedNewKey(&File{NewKeys: []NewKey{nk}}, "tls-origination") {
		t.Fatal("a rejected record is not admitted")
	}
	nk = newKey()
	nk.Anchor = "  "
	if AdmittedNewKey(&File{NewKeys: []NewKey{nk}}, "tls-origination") {
		t.Fatal("an incomplete record is not admitted")
	}
}

func TestOrder(t *testing.T) {
	c := testCauses(t)
	f := &File{NewKeys: []NewKey{{Key: "n1"}, {Key: "n2"}}}
	if c.Order(f, "ambient-np") != 0 || c.Order(f, "ambient-l7") != 7 {
		t.Fatal("frozen order")
	}
	if !(c.Order(f, "ambient-l7") < c.Order(f, "n1") && c.Order(f, "n1") < c.Order(f, "n2") && c.Order(f, "n2") < c.Order(f, "zzz")) {
		t.Fatal("new keys rank after the frozen list, in ledger order")
	}
}

// pilotLedger is pilot 1's real outcome as ledger rows: redis in W3 (three
// differing keys) and rabbitmq in W3 (five), cause ambient-np, fix
// <chart>-np-15008, and podinfo W1, excluded as c-optout. Pilot 1 had no
// recheck version, no reviewer and no upstream check in this form, so those
// fields are stand-ins and say so in the note.
func pilotLedger() *File {
	f := &File{Schema: Schema}
	counted := func(chart, fix string, keys ...string) {
		for _, k := range keys {
			f.Rows = append(f.Rows, Row{
				Chart: chart, World: "W3", Key: k,
				Cause: "ambient-np", Type: TypeDefect,
				TypeBasis: &TypeBasis{Doc: "networkPolicy.extraIngress is a generic key (section 2, Type)"},
				Fix:       &Fix{Entry: fix, Form: FormValues, HelmLint: true},
				Runs:      &Runs{Run1: "40109270484", Run2: "40119399052", Certification: []string{"40109270484", "40119399052"}},
				Recheck:   &Recheck{Version: "1.31.3", Result: RecheckReproduces, Run: "40119399052"},
				Upstream:  &Upstream{},
				Reviewer:  &Reviewer{Verdict: VerdictAgree, Run: "fixture"},
				Status:    StatusCounted, Publisher: "bitnami",
				Note: "test fixture: pilot 1 had no recheck version or reviewer; those fields are stand-ins",
			})
		}
	}
	counted("redis", "redis-np-15008",
		"helm:install", "workload:StatefulSet/redis-replicas:ready", "workload:StatefulSet/redis-replicas:restarts")
	counted("rabbitmq", "rabbitmq-np-15008",
		"helm:install", "port:rabbitmq-headless:15672:http", "port:rabbitmq:15672:http",
		"workload:StatefulSet/rabbitmq:ready", "workload:StatefulSet/rabbitmq:restarts")
	f.Rows = append(f.Rows, Row{
		Chart: "podinfo", World: "W1", Key: "helm:test", Status: StatusExcluded,
		Exclusion: &Exclusion{Reason: ReasonCOptout, Callers: []string{"podinfo-grpc-test", "podinfo-jwt-test"},
			Evidence: "podinfo's test pods carry sidecar.istio.io/inject=false and call podinfo in plaintext under STRICT (docs/compat-pilot.md, podinfo section)"},
		Reviewer: &Reviewer{Verdict: VerdictAgree, Run: "fixture"},
	})
	return f
}

func TestPilotRowsPass(t *testing.T) {
	f := pilotLedger()
	if ps := Check(f, testCauses(t)); len(ps) != 0 {
		t.Fatalf("pilot rows rejected: %v", ps)
	}
	// round trip through the strict parser
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	g, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if ps := Check(g, testCauses(t)); len(ps) != 0 {
		t.Fatalf("round trip rejected: %v", ps)
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "x.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedLedgerIsValid(t *testing.T) {
	f, err := Load(filepath.Join("..", "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Rows) != 0 || len(f.NewKeys) != 0 {
		t.Fatalf("the committed ledger starts empty")
	}
	if ps := Check(f, testCauses(t)); len(ps) != 0 {
		t.Fatal(ps)
	}
}
