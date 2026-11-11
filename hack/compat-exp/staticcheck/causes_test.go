package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The schema of hack/compat-exp/causes.json. Unknown fields are refused, so
// a misspelt field fails here instead of being read as absent.

type anchor struct {
	URL           string `json:"url"`
	SourcePath    string `json:"sourcePath"`
	Heading       string `json:"heading"`
	Quote         string `json:"quote"`
	IstioIoCommit string `json:"istioIoCommit"`
	CheckedOn     string `json:"checkedOn"`
}

type breadth struct {
	Guess              bool   `json:"guess"`
	UnderBound         string `json:"underBound"`
	FalsePositiveClass string `json:"falsePositiveClass"`
	Note               string `json:"note"`
}

type causeKey struct {
	Key                 string   `json:"key"`
	Mechanism           string   `json:"mechanism"`
	Anchor              *anchor  `json:"anchor"`
	NoAnchorReason      string   `json:"noAnchorReason"`
	NearestStatement    *anchor  `json:"nearestStatement"`
	NeedsNewKeyRule     bool     `json:"needsNewKeyRule"`
	AdditionalAnchors   []anchor `json:"additionalAnchors"`
	Worlds              []string `json:"worlds"`
	StaticCheck         *string  `json:"staticCheck"`
	StaticCheckRule     string   `json:"staticCheckRule"`
	StaticCheckWorlds   []string `json:"staticCheckWorlds"`
	NoStaticCheckReason string   `json:"noStaticCheckReason"`
	ExpectedBreadth     *breadth `json:"expectedBreadth"`
	RequirementKeyHint  string   `json:"requirementKeyHint"`
	Notes               string   `json:"notes"`
}

type causesFile struct {
	SchemaVersion   int             `json:"schemaVersion"`
	PreRegistration string          `json:"preRegistration"`
	Tool            string          `json:"tool"`
	CheckedOn       string          `json:"checkedOn"`
	IstioIo         json.RawMessage `json:"istioIo"`
	QuoteForm       string          `json:"quoteForm"`
	Keys            []causeKey      `json:"keys"`
}

const causesPath = "../causes.json"

func loadCauses(t *testing.T) (causesFile, []byte) {
	t.Helper()
	data, err := os.ReadFile(causesPath)
	if err != nil {
		t.Fatal(err)
	}
	var cf causesFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cf); err != nil {
		t.Fatalf("causes.json: %v", err)
	}
	return cf, data
}

// sectionThreeKeys reads the key column of the table in section 3 of the
// pre-registration, so the list here cannot drift from the signed one.
func sectionThreeKeys(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("../../../docs/compat-experiment.md")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var keys []string
	in := false
	row := regexp.MustCompile(`^\| ([a-z0-9-]+) \|`)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			in = strings.HasPrefix(line, "## 3. ")
			continue
		}
		if m := row.FindStringSubmatch(line); in && m != nil && m[1] != "key" {
			keys = append(keys, m[1])
		}
	}
	if len(keys) == 0 {
		t.Fatal("no key table in section 3 of docs/compat-experiment.md")
	}
	return keys
}

var (
	sha1Re  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	dateRe  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	plainRe = regexp.MustCompile("[–—‘’“”←-⇿]")
)

func checkAnchor(t *testing.T, key, what string, a *anchor) {
	t.Helper()
	if !strings.HasPrefix(a.URL, "https://istio.io/") {
		t.Errorf("%s %s: url %q is not on istio.io", key, what, a.URL)
	}
	if !strings.HasPrefix(a.SourcePath, "content/en/") {
		t.Errorf("%s %s: sourcePath %q is not an istio.io content path", key, what, a.SourcePath)
	}
	if a.Heading == "" || a.Quote == "" {
		t.Errorf("%s %s: heading and quote are required", key, what)
	}
	if !sha1Re.MatchString(a.IstioIoCommit) {
		t.Errorf("%s %s: istioIoCommit %q is not a commit SHA", key, what, a.IstioIoCommit)
	}
	if !dateRe.MatchString(a.CheckedOn) {
		t.Errorf("%s %s: checkedOn %q is not a date", key, what, a.CheckedOn)
	}
}

func TestCausesSchema(t *testing.T) {
	cf, data := loadCauses(t)
	if loc := plainRe.FindIndex(data); loc != nil {
		t.Errorf("causes.json has non-plain punctuation at byte %d", loc[0])
	}
	if cf.SchemaVersion != 1 || cf.Tool != "hack/compat-exp/staticcheck" {
		t.Errorf("schemaVersion %d, tool %q", cf.SchemaVersion, cf.Tool)
	}

	want := sectionThreeKeys(t)
	var got []string
	seen := map[string]bool{}
	for _, k := range cf.Keys {
		if seen[k.Key] {
			t.Errorf("duplicate key %s", k.Key)
		}
		seen[k.Key] = true
		got = append(got, k.Key)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys, in order:\n got %v\nwant %v (section 3)", got, want)
	}
	if len(got) != 9 {
		t.Errorf("%d keys, want nine", len(got))
	}

	registry := map[string]check{}
	for _, c := range checks {
		registry[c.ID] = c
	}
	used := map[string]string{}
	for _, k := range cf.Keys {
		if k.Mechanism == "" || k.RequirementKeyHint == "" {
			t.Errorf("%s: mechanism and requirementKeyHint are required", k.Key)
		}
		if len(k.Worlds) == 0 {
			t.Errorf("%s: no worlds", k.Key)
		}
		for _, w := range k.Worlds {
			if w != "W1" && w != "W3" {
				t.Errorf("%s: world %q is not W1 or W3", k.Key, w)
			}
		}

		// The anchor, or the explicit null form.
		if k.Anchor != nil {
			checkAnchor(t, k.Key, "anchor", k.Anchor)
			if k.NoAnchorReason != "" || k.NeedsNewKeyRule {
				t.Errorf("%s: has an anchor and the null form", k.Key)
			}
		} else if k.NoAnchorReason == "" || !k.NeedsNewKeyRule {
			t.Errorf("%s: null anchor needs noAnchorReason and needsNewKeyRule: true", k.Key)
		}
		for i := range k.AdditionalAnchors {
			checkAnchor(t, k.Key, "additionalAnchors", &k.AdditionalAnchors[i])
		}
		if k.NearestStatement != nil {
			checkAnchor(t, k.Key, "nearestStatement", k.NearestStatement)
		}

		// The static check, or the reason there is none.
		if k.StaticCheck == nil {
			if k.NoStaticCheckReason == "" || k.StaticCheckRule != "" || k.ExpectedBreadth != nil {
				t.Errorf("%s: null staticCheck needs noStaticCheckReason and no rule or breadth", k.Key)
			}
			continue
		}
		id := *k.StaticCheck
		c, ok := registry[id]
		if !ok {
			t.Errorf("%s: staticCheck %q is not implemented in the tool", k.Key, id)
			continue
		}
		if prev, dup := used[id]; dup {
			t.Errorf("%s: staticCheck %q already used by %s", k.Key, id, prev)
		}
		used[id] = k.Key
		if c.Key != k.Key {
			t.Errorf("%s: check %s belongs to key %s in the tool", k.Key, id, c.Key)
		}
		if k.StaticCheckRule != c.Rule {
			t.Errorf("%s: staticCheckRule differs from the tool's rule for %s", k.Key, id)
		}
		if !reflect.DeepEqual(k.StaticCheckWorlds, c.Worlds) {
			t.Errorf("%s: staticCheckWorlds %v, the tool runs %s in %v", k.Key, k.StaticCheckWorlds, id, c.Worlds)
		}
		for _, w := range c.Worlds {
			if !contains(k.Worlds, w) {
				t.Errorf("%s: check %s runs in %s, which is not one of the key's worlds", k.Key, id, w)
			}
		}
		if k.NoStaticCheckReason != "" {
			t.Errorf("%s: has a check and a noStaticCheckReason", k.Key)
		}
		if k.ExpectedBreadth == nil || !k.ExpectedBreadth.Guess || k.ExpectedBreadth.UnderBound == "" || k.ExpectedBreadth.FalsePositiveClass == "" {
			t.Errorf("%s: expectedBreadth must be a labelled guess with underBound and falsePositiveClass", k.Key)
		}
	}
	for _, c := range checks {
		if _, ok := used[c.ID]; !ok {
			t.Errorf("check %s is implemented but named by no key", c.ID)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
