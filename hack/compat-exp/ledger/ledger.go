// Package ledger reads and checks hack/compat-exp/ledger.json, the hand
// maintained data file of the analysis phase of the compat experiment
// (docs/compat-experiment.md, section 8 item 11). One row holds what a person
// or a classifying agent decided about one cell (chart, world, observation
// key) and the evidence for it. The package only validates; it never decides
// a judgment field.
package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Schema is the only ledger schema version.
const Schema = 1

// Row statuses.
const (
	StatusCounted    = "counted"
	StatusExcluded   = "excluded"
	StatusUnexplain  = "unexplained"
	StatusUnresolved = "unresolved"
)

// Exclusion reasons: the fixed list of rule 5.
const (
	ReasonPullFailure    = "pull-failure"
	ReasonCapacity       = "capacity"
	ReasonFlaky          = "flaky"
	ReasonUndecided      = "undecided"
	ReasonCOptout        = "c-optout"
	ReasonWorldUniversal = "world-universal"
)

// Reviewer verdicts on a row.
const (
	VerdictAgree        = "agree"
	VerdictDisagreeType = "disagree-type"
	VerdictDisagreeOth  = "disagree-other"
)

// Types of a cell's cause.
const (
	TypeDefect = "defect"
	TypeGap    = "gap"
)

// Recheck results.
const (
	RecheckReproduces    = "reproduces-3of3"
	RecheckNotReproduced = "not-reproduced"
)

// Fix forms.
const (
	FormValues = "values"
	FormPatch  = "patch"
)

// Reviewer verdicts on a new key (see hack/compat-exp/crossrun/INTERPRETATIONS.md).
const (
	KeyAdmit  = "admit"
	KeyMerge  = "merge"
	KeyReject = "reject"
)

// File is the whole ledger.
type File struct {
	Schema  int      `json:"schema"`
	Rows    []Row    `json:"rows"`
	NewKeys []NewKey `json:"newKeys"`
}

// Row is one cell that differed.
type Row struct {
	Chart     string     `json:"chart"`
	World     string     `json:"world"`
	Key       string     `json:"key"`
	Cause     string     `json:"cause,omitempty"`
	Type      string     `json:"type,omitempty"`
	TypeBasis *TypeBasis `json:"typeBasis,omitempty"`
	Fix       *Fix       `json:"fix,omitempty"`
	Combined  *Combined  `json:"combined,omitempty"`
	Parts     []Part     `json:"parts,omitempty"`
	Runs      *Runs      `json:"runs,omitempty"`
	Recheck   *Recheck   `json:"recheck,omitempty"`
	Upstream  *Upstream  `json:"upstream,omitempty"`
	Exclusion *Exclusion `json:"exclusion,omitempty"`
	Reviewer  *Reviewer  `json:"reviewer,omitempty"`
	Status    string     `json:"status"`
	Publisher string     `json:"publisher,omitempty"`
	Note      string     `json:"note,omitempty"`
}

// TypeBasis is the values key or documentation reference that decides the type.
type TypeBasis struct {
	ValuesKey string `json:"valuesKey,omitempty"`
	Doc       string `json:"doc,omitempty"`
}

// Fix is the fix entry of the credited cause.
type Fix struct {
	Entry       string `json:"entry"`
	Form        string `json:"form"`
	PatchSHA256 string `json:"patchSha256,omitempty"`
	HelmLint    bool   `json:"helmLint"`
}

// Combined is the certificate of the combined fix, when a cell needs two
// keys' fixes (rule 3).
type Combined struct {
	Entry         string   `json:"entry"`
	Certification []string `json:"certification"`
}

// Part is one key's fix run alone (rule 3).
type Part struct {
	Cause string   `json:"cause"`
	Entry string   `json:"entry"`
	Runs  []string `json:"runs"`
}

// Runs are the workflow run ids behind rules 1 and 2.
type Runs struct {
	Run1          string   `json:"run1"`
	Run2          string   `json:"run2"`
	Certification []string `json:"certification"`
}

// Recheck is the result on the recheck Istio version (rule 4).
type Recheck struct {
	Version string `json:"version"`
	Result  string `json:"result"`
	Run     string `json:"run"`
}

// Upstream says whether a fix is merged on the chart's default branch.
type Upstream struct {
	Merged bool   `json:"merged"`
	Link   string `json:"link,omitempty"`
}

// Exclusion is a rule 5 exclusion with its evidence.
type Exclusion struct {
	Reason   string `json:"reason"`
	Evidence string `json:"evidence"`
}

// Reviewer is the fresh reviewer's verdict.
type Reviewer struct {
	Verdict string `json:"verdict"`
	Run     string `json:"run"`
}

// NewKey is the record of a cause key outside causes.json (section 3).
type NewKey struct {
	Key          string `json:"key"`
	Anchor       string `json:"anchor"`
	CertifiedFix string `json:"certifiedFix"`
	Reproduction string `json:"reproduction"`
	Reviewer     *struct {
		Verdict string `json:"verdict"`
		Run     string `json:"run"`
	} `json:"reviewer"`
}

// Cell names a (chart, world, observation key).
type Cell struct{ Chart, World, Key string }

func (c Cell) String() string { return c.Chart + "/" + c.World + "/" + c.Key }

// Cell returns the row's cell.
func (r Row) Cell() Cell { return Cell{r.Chart, r.World, r.Key} }

// Load reads a ledger strictly: unknown fields and trailing data are errors.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes ledger JSON strictly.
func Parse(data []byte) (*File, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("ledger: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("ledger: trailing data after the JSON document")
	}
	return &f, nil
}

// Causes is the frozen cause list, in rank order.
type Causes struct {
	Keys []string
	// Static maps a cause key to the id of its static check. A key whose
	// causes.json entry has staticCheck null has no entry.
	Static map[string]string
	// Worlds maps a cause key to the mesh worlds it applies to.
	Worlds map[string][]string
}

// LoadCauses reads hack/compat-exp/causes.json.
func LoadCauses(path string) (*Causes, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseCauses(data)
}

// ParseCauses is LoadCauses on bytes. The one shape is the real file's: an
// object whose list is under `keys`, each entry with `key`, `worlds` and
// `staticCheck` (a check id, or null for a key without a static check). Other
// fields are ignored.
func ParseCauses(data []byte) (*Causes, error) {
	var doc struct {
		Keys []struct {
			Key         string   `json:"key"`
			Worlds      []string `json:"worlds"`
			StaticCheck *string  `json:"staticCheck"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("causes: %v (the file is an object with a list under keys)", err)
	}
	if len(doc.Keys) == 0 {
		return nil, fmt.Errorf("causes: no keys found")
	}
	c := &Causes{Static: map[string]string{}, Worlds: map[string][]string{}}
	seen := map[string]bool{}
	for i, it := range doc.Keys {
		if it.Key == "" {
			return nil, fmt.Errorf("causes: entry %d has no key", i+1)
		}
		if seen[it.Key] {
			return nil, fmt.Errorf("causes: key %q repeated", it.Key)
		}
		if len(it.Worlds) == 0 {
			return nil, fmt.Errorf("causes: key %q has no worlds", it.Key)
		}
		seen[it.Key] = true
		c.Keys = append(c.Keys, it.Key)
		c.Worlds[it.Key] = it.Worlds
		if it.StaticCheck != nil && *it.StaticCheck != "" {
			c.Static[it.Key] = *it.StaticCheck
		}
	}
	return c, nil
}

// Has reports whether key is in the frozen list.
func (c *Causes) Has(key string) bool {
	for _, k := range c.Keys {
		if k == key {
			return true
		}
	}
	return false
}

// Order returns the rank of key: its index in causes.json, or len(Keys) plus
// its index in the ledger's newKeys for a new key (new keys rank last, in the
// order the ledger lists them). Unknown keys rank after all of those.
func (c *Causes) Order(f *File, key string) int {
	for i, k := range c.Keys {
		if k == key {
			return i
		}
	}
	for i, nk := range f.NewKeys {
		if nk.Key == key {
			return len(c.Keys) + i
		}
	}
	return len(c.Keys) + len(f.NewKeys)
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Problem is one failed check, naming its row.
type Problem struct {
	Where string
	Msg   string
}

func (p Problem) Error() string { return p.Where + ": " + p.Msg }

func isOneOf(v string, set ...string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

func rowName(i int, r Row) string {
	return fmt.Sprintf("row %d (%s/%s/%s)", i+1, r.Chart, r.World, r.Key)
}

// Check validates the ledger against the cause list. It returns every problem
// found, sorted by position, nil when the ledger is valid.
func Check(f *File, causes *Causes) []Problem {
	var ps []Problem
	add := func(where, format string, a ...any) {
		ps = append(ps, Problem{where, fmt.Sprintf(format, a...)})
	}
	if f.Schema != Schema {
		add("ledger", "schema is %d, want %d", f.Schema, Schema)
	}

	newKeys := map[string]bool{}
	for i, nk := range f.NewKeys {
		w := fmt.Sprintf("newKeys[%d] (%s)", i, nk.Key)
		if nk.Key == "" {
			add(w, "key is empty")
			continue
		}
		if causes.Has(nk.Key) {
			add(w, "key is already in causes.json")
		}
		if newKeys[nk.Key] {
			add(w, "key listed twice")
		}
		newKeys[nk.Key] = true
		if strings.TrimSpace(nk.Anchor) == "" {
			add(w, "anchor (Istio documentation anchor, or issue or code reference) is missing")
		}
		if strings.TrimSpace(nk.CertifiedFix) == "" {
			add(w, "certifiedFix is missing")
		}
		if strings.TrimSpace(nk.Reproduction) == "" {
			add(w, "reproduction script path is missing")
		}
		if nk.Reviewer == nil || nk.Reviewer.Verdict == "" || nk.Reviewer.Run == "" {
			add(w, "reviewer verdict and run are missing")
		} else if nk.Reviewer.Verdict != KeyAdmit {
			add(w, "reviewer verdict is %q, a key is admitted only on %q", nk.Reviewer.Verdict, KeyAdmit)
		}
	}

	seen := map[Cell]int{}
	for i, r := range f.Rows {
		w := rowName(i, r)
		if r.Chart == "" || r.World == "" || r.Key == "" {
			add(w, "chart, world and key are all required")
		}
		if j, dup := seen[r.Cell()]; dup {
			add(w, "same cell as row %d", j+1)
		} else {
			seen[r.Cell()] = i
		}
		if !isOneOf(r.Status, StatusCounted, StatusExcluded, StatusUnexplain, StatusUnresolved) {
			add(w, "status %q is not one of counted, excluded, unexplained, unresolved", r.Status)
			continue
		}
		if r.Cause != "" && !causes.Has(r.Cause) && !newKeys[r.Cause] {
			add(w, "cause %q is not in causes.json and has no complete new-key record", r.Cause)
		}
		if r.Type != "" && !isOneOf(r.Type, TypeDefect, TypeGap) {
			add(w, "type %q is not defect or gap", r.Type)
		}
		if r.Fix != nil {
			if r.Fix.Entry == "" {
				add(w, "fix.entry is empty")
			}
			if !isOneOf(r.Fix.Form, FormValues, FormPatch) {
				add(w, "fix.form %q is not values or patch", r.Fix.Form)
			}
			if r.Fix.Form == FormPatch && !sha256Re.MatchString(r.Fix.PatchSHA256) {
				add(w, "fix.form is patch, so fix.patchSha256 must be 64 lowercase hex digits")
			}
			if r.Fix.Form == FormValues && r.Fix.PatchSHA256 != "" {
				add(w, "fix.form is values but fix.patchSha256 is set")
			}
		}
		if r.Recheck != nil && !isOneOf(r.Recheck.Result, RecheckReproduces, RecheckNotReproduced) {
			add(w, "recheck.result %q is not %s or %s", r.Recheck.Result, RecheckReproduces, RecheckNotReproduced)
		}
		if r.Upstream != nil && r.Upstream.Merged && r.Upstream.Link == "" {
			add(w, "upstream.merged is true, so upstream.link is required")
		}
		if r.Exclusion != nil && !isOneOf(r.Exclusion.Reason, ReasonPullFailure, ReasonCapacity,
			ReasonFlaky, ReasonUndecided, ReasonCOptout, ReasonWorldUniversal) {
			add(w, "exclusion.reason %q is not one of the rule 5 reasons", r.Exclusion.Reason)
		}
		if r.Reviewer != nil && !isOneOf(r.Reviewer.Verdict, VerdictAgree, VerdictDisagreeType, VerdictDisagreeOth) {
			add(w, "reviewer.verdict %q is not agree, disagree-type or disagree-other", r.Reviewer.Verdict)
		}
		partCauses := map[string]bool{}
		for k, p := range r.Parts {
			if p.Cause == "" || p.Entry == "" || len(p.Runs) == 0 {
				add(w, "parts[%d] needs cause, entry and runs", k)
			}
			if p.Cause != "" && !causes.Has(p.Cause) && !newKeys[p.Cause] {
				add(w, "parts[%d].cause %q is not a known key", k, p.Cause)
			}
			if partCauses[p.Cause] {
				add(w, "parts[%d].cause %q repeated: a cell is credited to exactly one key", k, p.Cause)
			}
			partCauses[p.Cause] = true
		}
		if len(r.Parts) > 0 {
			if len(r.Parts) < 2 {
				add(w, "parts needs at least two keys")
			}
			if r.Cause != "" && !partCauses[r.Cause] {
				add(w, "cause %q is not one of the parts", r.Cause)
			}
			if r.Combined == nil || r.Combined.Entry == "" || len(r.Combined.Certification) < 2 {
				add(w, "parts need combined.entry and two combined.certification runs")
			}
			if r.Fix != nil {
				for _, p := range r.Parts {
					if p.Cause == r.Cause && p.Entry != r.Fix.Entry {
						add(w, "fix.entry %q is not the part entry %q of cause %q", r.Fix.Entry, p.Entry, p.Cause)
					}
				}
			}
		} else if r.Combined != nil {
			add(w, "combined is set without parts")
		}

		switch r.Status {
		case StatusCounted:
			checkCounted(w, r, add)
		case StatusExcluded:
			if r.Exclusion == nil || r.Exclusion.Reason == "" {
				add(w, "excluded row needs exclusion.reason")
			} else if strings.TrimSpace(r.Exclusion.Evidence) == "" {
				add(w, "excluded row needs exclusion.evidence")
			}
		default:
			if r.Exclusion != nil {
				add(w, "status %s but exclusion is set", r.Status)
			}
		}
		if r.Status == StatusCounted && r.Exclusion != nil {
			add(w, "counted row has an exclusion")
		}
	}

	sort.SliceStable(ps, func(a, b int) bool { return ps[a].Where < ps[b].Where })
	return ps
}

func checkCounted(w string, r Row, add func(string, string, ...any)) {
	if r.Cause == "" {
		add(w, "counted row needs cause")
	}
	if r.Type == "" {
		add(w, "counted row needs type (defect or gap)")
	}
	if r.TypeBasis == nil || (strings.TrimSpace(r.TypeBasis.ValuesKey) == "" && strings.TrimSpace(r.TypeBasis.Doc) == "") {
		add(w, "counted row needs typeBasis.valuesKey or typeBasis.doc (what decides the type)")
	}
	if r.Fix == nil {
		add(w, "counted row needs fix (rule 2)")
	} else if !r.Fix.HelmLint {
		add(w, "counted row needs fix.helmLint true (rule 2)")
	}
	if r.Runs == nil || r.Runs.Run1 == "" || r.Runs.Run2 == "" {
		add(w, "counted row needs runs.run1 and runs.run2 (rule 1)")
	} else if r.Runs.Run1 == r.Runs.Run2 {
		add(w, "runs.run1 and runs.run2 are the same workflow run (rule 1)")
	}
	if r.Runs == nil || len(distinct(r.Runs.Certification)) < 2 {
		add(w, "counted row needs two distinct runs.certification ids (rule 2)")
	}
	if r.Recheck == nil || r.Recheck.Version == "" || r.Recheck.Run == "" {
		add(w, "counted row needs recheck.version, recheck.result and recheck.run (rule 4)")
	} else if r.Recheck.Result != RecheckReproduces {
		add(w, "counted row has recheck.result %q, rule 4 needs %q", r.Recheck.Result, RecheckReproduces)
	}
	if r.Upstream == nil {
		add(w, "counted row needs upstream (merged: false asserts no merged fix, rule 4)")
	} else if r.Upstream.Merged {
		add(w, "counted row has a fix merged upstream (rule 4)")
	}
	if r.Reviewer == nil || r.Reviewer.Verdict == "" || r.Reviewer.Run == "" {
		add(w, "counted row needs reviewer.verdict and reviewer.run")
	} else if r.Reviewer.Verdict == VerdictDisagreeOth {
		add(w, "reviewer said disagree-other: the cell stays uncounted (section 2)")
	}
	if strings.TrimSpace(r.Publisher) == "" {
		add(w, "counted row needs publisher")
	}
}

func distinct(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Distinct returns the non-empty, unique ids in order of first appearance.
func Distinct(ids []string) []string { return distinct(ids) }
