package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const store = "testdata/store"

// The fixtures are real Artifact Hub responses trimmed to the fields frame
// reads, plus clearly marked synthetic packages (repository example-repo) for
// the cases the live top 300 does not contain today (deprecated, ties).

func copyStore(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	ents, err := os.ReadDir(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(store, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func buildStore(t *testing.T, dir string) (*Output, []byte) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "candidates.json")
	if err := buildFile(dir, out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var o Output
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatal(err)
	}
	return &o, raw
}

func cand(o *Output, pkg, repo string) *Candidate {
	for i := range o.Candidates {
		if o.Candidates[i].Package == pkg && o.Candidates[i].Repository == repo {
			return &o.Candidates[i]
		}
	}
	return nil
}

func skipped(o *Output, pkg, repo string) *Skipped {
	for i := range o.Skipped {
		if o.Skipped[i].Package == pkg && o.Skipped[i].Repository == repo {
			return &o.Skipped[i]
		}
	}
	return nil
}

func TestGolden(t *testing.T) {
	_, raw := buildStore(t, store)
	golden := filepath.Join("testdata", "candidates.golden.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("candidates.json differs from %s (UPDATE_GOLDEN=1 rewrites it after review)", golden)
	}
	// Same input, byte-identical output, and a copy of the input elsewhere
	// gives the same bytes: nothing depends on the path or the clock.
	_, again := buildStore(t, copyStore(t))
	if !bytes.Equal(raw, again) {
		t.Fatal("output differs between two builds of the same stored input")
	}
}

func TestInputHashIsTheSumsFile(t *testing.T) {
	o, _ := buildStore(t, store)
	raw, _ := os.ReadFile(filepath.Join(store, sumsName))
	if o.InputSHA256 != sha(raw) {
		t.Fatalf("input_sha256 %s is not the sha256 of SHA256SUMS", o.InputSHA256)
	}
	if o.ExclusionsSHA256 != sha(embeddedExclusions) {
		t.Fatal("exclusions_sha256 is not the sha256 of exclusions.json")
	}
}

func TestPublisherCap(t *testing.T) {
	o, _ := buildStore(t, store)
	if cand(o, "redis", "bitnami") == nil || cand(o, "postgresql", "bitnami") == nil {
		t.Fatal("the first two bitnami charts should be candidates")
	}
	s := skipped(o, "keycloak", "bitnami")
	if s == nil || s.Class != "publisher-cap" || !strings.Contains(s.Reason, "bitnami") {
		t.Fatalf("third bitnami chart should be skipped for the cap, got %+v", s)
	}
	// Excluded and deprecated charts do not use up a publisher's places:
	// prometheus-community has an excluded -crds chart and one candidate.
	if cand(o, "kube-prometheus-stack", "prometheus-community") == nil {
		t.Fatal("kube-prometheus-stack should be a candidate")
	}
}

func TestExclusionClasses(t *testing.T) {
	o, _ := buildStore(t, store)
	cases := []struct{ pkg, repo, class string }{
		{"cilium", "cilium", "cni"},
		{"tigera-operator", "projectcalico", "cni"},
		{"cni-keyword", "example-repo", "cni"}, // keyword match is case-insensitive
		{"istiod", "istio-official", "mesh"},
		{"linkerd-viz", "linkerd2", "mesh"}, // keyword service-mesh
		{"consul", "hashicorp", "mesh"},
		{"widgets-crds", "example-repo", "crd-only"},
		{"prometheus-operator-crds", "prometheus-community", "crd-only"},
		{"common", "bitnami", "library"}, // data.type == library
		{"example-deprecated", "example-repo", "deprecated"},
	}
	for _, c := range cases {
		s := skipped(o, c.pkg, c.repo)
		if s == nil || s.Class != c.class || s.Reason == "" {
			t.Errorf("%s/%s: want skipped as %s with a reason, got %+v", c.repo, c.pkg, c.class, s)
		}
		if cand(o, c.pkg, c.repo) != nil {
			t.Errorf("%s/%s is both skipped and a candidate", c.repo, c.pkg)
		}
	}
	if s := skipped(o, "istiod", "istio-official"); s != nil && !strings.Contains(s.Reason, "exclusions.json entry") {
		t.Errorf("an exclusion reason should name the entry: %q", s.Reason)
	}
}

func TestKeywordCRDEntry(t *testing.T) {
	ex, err := loadExclusions(embeddedExclusions)
	if err != nil {
		t.Fatal(err)
	}
	var crdKeyword *exclusionEntry
	for i := range ex.Entries {
		if ex.Entries[i].Class == "crd-only" && ex.Entries[i].Keyword != "" {
			crdKeyword = &ex.Entries[i]
		}
	}
	if crdKeyword == nil {
		t.Fatal("no crd-only keyword entry")
	}
	if !crdKeyword.matches("r", "anything", []string{"Foo", "CRDs"}) || crdKeyword.matches("r", "anything", []string{"foo"}) {
		t.Fatal("keyword entry must match on a case-insensitive keyword and nothing else")
	}
}

func TestEntryNeedsAllFields(t *testing.T) {
	e := exclusionEntry{Class: "mesh", Repository: "hashicorp", Name: "consul", Reason: "x"}
	if !e.matches("hashicorp", "consul", nil) || e.matches("bitnami", "consul", nil) || e.matches("hashicorp", "vault", nil) {
		t.Fatal("repository and name must both match")
	}
}

func TestPublisherDerivation(t *testing.T) {
	o, _ := buildStore(t, store)
	cases := []struct{ pkg, repo, pub, source string }{
		{"kube-prometheus-stack", "prometheus-community", "prometheus-community", "github-source-link"},
		{"mixed-case-owner", "example-repo", "mixedcase-org", "github-source-link"},     // lower-cased; "Application Sources" ignored
		{"gitlab", "gitlab", "gitlab", "repository-name: no source link on github.com"}, // source link on gitlab.com
		{"no-links", "example-repo", "example-repo", "repository-name: no source link"},
		{"redis", "cloudpirates-redis", "cloudpirates-redis", "repository-name: no source link"}, // only a "Helm Chart" link
	}
	for _, c := range cases {
		got := cand(o, c.pkg, c.repo)
		if got == nil {
			t.Errorf("%s/%s should be a candidate", c.repo, c.pkg)
			continue
		}
		if got.Publisher != c.pub || got.PublisherSource != c.source {
			t.Errorf("%s/%s: publisher %q (%s), want %q (%s)", c.repo, c.pkg, got.Publisher, got.PublisherSource, c.pub, c.source)
		}
	}
	if c := cand(o, "mixed-case-owner", "example-repo"); c != nil && !strings.HasPrefix(c.PublisherLink, "https://github.com/MixedCase-Org") {
		t.Errorf("the link the owner came from should be recorded: %q", c.PublisherLink)
	}
}

func TestGithubOwner(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/Foo/bar":     "foo",
		"https://www.github.com/foo":     "foo",
		"http://GITHUB.com//foo/bar":     "foo",
		"https://gitlab.com/foo/bar":     "",
		"https://github.com/":            "",
		"https://github.com.evil.test/x": "",
		"not a url \x7f":                 "",
		"https://hub.docker.com/u/foo":   "",
	} {
		if got := githubOwner(in); got != want {
			t.Errorf("githubOwner(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTieBreakIsReturnedOrder(t *testing.T) {
	o, _ := buildStore(t, store)
	z, a := cand(o, "zeta-tie", "example-repo"), cand(o, "alpha-tie", "example-repo")
	if z == nil || a == nil || z.Stars != a.Stars {
		t.Fatal("fixture should hold a star tie")
	}
	// zeta-tie is returned before alpha-tie; the tie keeps the returned order
	// even though the names sort the other way.
	if z.Rank >= a.Rank {
		t.Fatalf("returned order must win over name: zeta rank %d, alpha rank %d", z.Rank, a.Rank)
	}
	for i := 1; i < len(o.Candidates); i++ {
		if o.Candidates[i].Rank <= o.Candidates[i-1].Rank {
			t.Fatal("candidates are not in rank order")
		}
	}
}

func TestUnsortedRankingIsRefused(t *testing.T) {
	dir := copyStore(t)
	p := filepath.Join(dir, "search-000.json")
	var pg searchPage
	b, _ := os.ReadFile(p)
	if err := json.Unmarshal(b, &pg); err != nil {
		t.Fatal(err)
	}
	pg.Packages[0].Stars = 1
	nb, _ := json.Marshal(pg)
	if err := os.WriteFile(p, nb, 0o644); err != nil {
		t.Fatal(err)
	}
	rewriteSums(t, dir)
	if err := buildFile(dir, filepath.Join(t.TempDir(), "o.json")); err == nil || !strings.Contains(err.Error(), "not sorted by stars") {
		t.Fatalf("want a not-sorted error, got %v", err)
	}
}

func TestChartRef(t *testing.T) {
	cases := []struct{ url, name, repo, chart string }{
		{"https://prometheus-community.github.io/helm-charts", "kube-prometheus-stack", "https://prometheus-community.github.io/helm-charts", "kube-prometheus-stack"},
		{"https://nats-io.github.io/k8s/helm/charts/", "nats", "https://nats-io.github.io/k8s/helm/charts", "nats"},
		{"oci://ghcr.io/argoproj/argo-helm/argo-cd", "argo-cd", "", "oci://ghcr.io/argoproj/argo-helm/argo-cd"},
		{"oci://registry-1.docker.io/bitnamicharts", "redis", "", "oci://registry-1.docker.io/bitnamicharts/redis"},
	}
	for _, c := range cases {
		r, ch := chartRef(c.url, c.name)
		if r != c.repo || ch != c.chart {
			t.Errorf("chartRef(%q,%q) = %q, %q", c.url, c.name, r, ch)
		}
	}
}

func TestHashMismatch(t *testing.T) {
	dir := copyStore(t)
	p := filepath.Join(dir, "pkg-cilium__cilium.json")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, bytes.Replace(b, []byte("cilium"), []byte("cilium2"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	err := buildFile(dir, filepath.Join(t.TempDir(), "o.json"))
	if err == nil || !strings.Contains(err.Error(), "hash mismatch for pkg-cilium__cilium.json") {
		t.Fatalf("a tampered file must fail verification, got %v", err)
	}
}

func TestExtraAndMissingFiles(t *testing.T) {
	dir := copyStore(t)
	if err := os.WriteFile(filepath.Join(dir, "pkg-extra__x.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := buildFile(dir, filepath.Join(t.TempDir(), "o.json")); err == nil || !strings.Contains(err.Error(), "not in SHA256SUMS") {
		t.Fatalf("an unlisted file must be refused, got %v", err)
	}
	dir = copyStore(t)
	os.Remove(filepath.Join(dir, "pkg-cilium__cilium.json"))
	if err := buildFile(dir, filepath.Join(t.TempDir(), "o.json")); err == nil || !strings.Contains(err.Error(), "listed file missing") {
		t.Fatalf("a missing file must be refused, got %v", err)
	}
	dir = copyStore(t)
	os.Remove(filepath.Join(dir, sumsName))
	if err := buildFile(dir, filepath.Join(t.TempDir(), "o.json")); err == nil {
		t.Fatal("a directory without SHA256SUMS must be refused")
	}
}

type failTransport struct{ calls int }

func (f *failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls++
	return nil, errors.New("network is off limits in build")
}

func TestBuildNeverTouchesTheNetwork(t *testing.T) {
	ft := &failTransport{}
	oldT, oldC := http.DefaultTransport, http.DefaultClient
	http.DefaultTransport = ft
	http.DefaultClient = &http.Client{Transport: ft}
	defer func() { http.DefaultTransport, http.DefaultClient = oldT, oldC }()
	buildStore(t, store)
	out := filepath.Join(t.TempDir(), "c.json")
	if err := run([]string{"build", "-in", store, "-out", out}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"charts", "-in", out, "-out", filepath.Join(t.TempDir(), "ch.json")}); err != nil {
		t.Fatal(err)
	}
	if ft.calls != 0 {
		t.Fatalf("build and charts made %d network calls", ft.calls)
	}
}

// fixtureDoer serves the committed fixtures as if they were Artifact Hub.
type fixtureDoer struct {
	reqs    []*http.Request
	status  int // when non-zero, every response has this status
	failURL string
}

func (f *fixtureDoer) Do(r *http.Request) (*http.Response, error) {
	f.reqs = append(f.reqs, r)
	reply := func(code int, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(body))}, nil
	}
	if f.status != 0 || (f.failURL != "" && strings.Contains(r.URL.String(), f.failURL)) {
		code := f.status
		if code == 0 {
			code = 500
		}
		return reply(code, []byte("nope"))
	}
	var name string
	switch {
	case strings.HasSuffix(r.URL.Path, "/packages/search"):
		q := r.URL.Query()
		if q.Get("kind") != "0" || q.Get("sort") != "stars" || q.Get("limit") != "60" {
			return reply(400, nil)
		}
		off, _ := strconv.Atoi(q.Get("offset"))
		name = searchFile(off)
	case strings.Contains(r.URL.Path, "/packages/helm/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/packages/helm/"), "/")
		name = "pkg-" + parts[0] + "__" + parts[1] + ".json"
	}
	body, err := os.ReadFile(filepath.Join(store, name))
	if err != nil {
		return reply(404, nil)
	}
	return reply(200, body)
}

func TestFetchStoresRawBodiesAndSums(t *testing.T) {
	d := &fixtureDoer{}
	dir := filepath.Join(t.TempDir(), "raw")
	if err := fetchAll(d, dir, 0, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{"offset=0", "offset=60", "offset=120", "offset=180", "offset=240"}
	for i, w := range want {
		if !strings.Contains(d.reqs[i].URL.RawQuery, w) || !strings.Contains(d.reqs[i].URL.RawQuery, "kind=0&sort=stars&limit=60") {
			t.Errorf("request %d is %s", i, d.reqs[i].URL)
		}
	}
	for _, r := range d.reqs {
		if r.Method != http.MethodGet || r.Header.Get("User-Agent") != "meshmedic-compat-exp" || r.URL.Host != "artifacthub.io" {
			t.Errorf("unexpected request %s %s ua=%q", r.Method, r.URL, r.Header.Get("User-Agent"))
		}
		for k := range r.Header {
			if k != "User-Agent" && k != "Accept" {
				t.Errorf("unexpected header %s", k)
			}
		}
	}
	// 25 fixture packages, one deprecated: 5 searches + 24 details.
	if len(d.reqs) != 5+24 {
		t.Errorf("made %d requests, want 29", len(d.reqs))
	}
	// Stored bytes are the served bytes, and the sums verify.
	got, _ := os.ReadFile(filepath.Join(dir, "pkg-cilium__cilium.json"))
	orig, _ := os.ReadFile(filepath.Join(store, "pkg-cilium__cilium.json"))
	if !bytes.Equal(got, orig) {
		t.Error("stored body differs from the response")
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg-example-repo__example-deprecated.json")); err == nil {
		t.Error("a deprecated package should not be fetched in detail")
	}
	_, _, err := verifyDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// What was fetched builds to the same candidates as the committed store.
	a, _ := buildStore(t, dir)
	b, _ := buildStore(t, store)
	if len(a.Candidates) != len(b.Candidates) || len(a.Skipped) != len(b.Skipped) {
		t.Error("fetched set builds to a different result")
	}
	// A second fetch into the same directory is refused.
	if err := fetchAll(d, dir, 0, io.Discard); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("want a not-empty error, got %v", err)
	}
}

func TestFetchNon200(t *testing.T) {
	err := fetchAll(&fixtureDoer{status: 503}, filepath.Join(t.TempDir(), "raw"), 0, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "search?kind=0") {
		t.Fatalf("want a clear HTTP 503 error naming the URL, got %v", err)
	}
	err = fetchAll(&fixtureDoer{failURL: "/packages/helm/cilium/"}, filepath.Join(t.TempDir(), "raw"), 0, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "cilium") {
		t.Fatalf("want a clear detail error, got %v", err)
	}
}

func TestFetchPausesBetweenRequests(t *testing.T) {
	start := time.Now()
	d := &fixtureDoer{}
	// Two searches' worth is enough to see the pause; run the whole thing at 2 ms.
	if err := fetchAll(d, filepath.Join(t.TempDir(), "raw"), 2*time.Millisecond, io.Discard); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 28*2*time.Millisecond {
		t.Error("requests were not spaced by the pause")
	}
}

func TestChartsShape(t *testing.T) {
	o, _ := buildStore(t, store)
	entries, err := toCharts(o.Candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(o.Candidates) {
		t.Fatal("one entry per candidate")
	}
	data, _ := marshal(entries)
	var generic []map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"id": true, "repo": true, "chart": true, "version": true, "why": true}
	ids := map[string]bool{}
	for _, e := range generic {
		for k := range e {
			if !allowed[k] {
				t.Errorf("key %q is not part of the defaults-only shape", k)
			}
		}
		for _, k := range []string{"id", "chart", "version", "why"} {
			if s, _ := e[k].(string); s == "" {
				t.Errorf("entry %v lacks %s", e["id"], k)
			}
		}
		if ids[e["id"].(string)] {
			t.Errorf("duplicate id %v", e["id"])
		}
		ids[e["id"].(string)] = true
	}
	// A classic repository gives repo + chart; an OCI one gives the full ref.
	for _, e := range entries {
		if e.ID == "kube-prometheus-stack" && (e.Repo == "" || e.Chart != "kube-prometheus-stack") {
			t.Errorf("classic entry: %+v", e)
		}
		if e.ID == "argo-cd" && (e.Repo != "" || !strings.HasPrefix(e.Chart, "oci://")) {
			t.Errorf("oci entry: %+v", e)
		}
	}
	// "redis" appears once among the candidates here (bitnami), and the other
	// redis is cloudpirates-redis: both are candidates, so ids are qualified.
	if !ids["bitnami-redis"] || !ids["cloudpirates-redis-redis"] {
		t.Errorf("colliding names should use repository-name ids: %v", ids)
	}
	// charts file round trip through the CLI.
	dir := t.TempDir()
	c, ch := filepath.Join(dir, "c.json"), filepath.Join(dir, "ch.json")
	if err := buildFile(store, c); err != nil {
		t.Fatal(err)
	}
	if err := chartsFile(c, ch); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ch)
	if !bytes.Equal(b, data) {
		t.Error("charts file differs from the in-memory conversion")
	}
}

func TestExclusionFileValidation(t *testing.T) {
	for _, bad := range []string{
		`{"entries":[{"class":"cni","reason":"r"}]}`,                  // no match field
		`{"entries":[{"class":"cni","name":"x"}]}`,                    // no reason
		`{"entries":[{"class":"cni","name":"x","reason":"r","x":1}]}`, // unknown key
	} {
		if _, err := loadExclusions([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := loadExclusions(embeddedExclusions); err != nil {
		t.Fatal(err)
	}
}

func TestUsage(t *testing.T) {
	if err := run(nil); err == nil {
		t.Error("no arguments should be a usage error")
	}
	if err := run([]string{"build"}); err == nil {
		t.Error("build without flags should fail")
	}
}

func sha(b []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func rewriteSums(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	sums := map[string]string{}
	for _, e := range ents {
		if e.Name() == sumsName {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		sums[e.Name()] = sha(b)
	}
	if err := writeSums(filepath.Join(dir, sumsName), sums); err != nil {
		t.Fatal(err)
	}
}
