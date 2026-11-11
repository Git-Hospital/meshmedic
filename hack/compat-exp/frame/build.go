package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed exclusions.json
var embeddedExclusions []byte

const (
	publisherCap = 2
	tieBreakRule = "stars descending, then the order Artifact Hub returned the packages in (page by page), then repository name, then package name"
)

// --- exclusion data -----------------------------------------------------------

type exclusionEntry struct {
	Class      string `json:"class"`
	Repository string `json:"repository,omitempty"`
	Name       string `json:"name,omitempty"`
	NameSuffix string `json:"name_suffix,omitempty"`
	Keyword    string `json:"keyword,omitempty"`
	Reason     string `json:"reason"`
}

type exclusionFile struct {
	Comment string           `json:"comment"`
	Entries []exclusionEntry `json:"entries"`
}

func loadExclusions(data []byte) (*exclusionFile, error) {
	var ex exclusionFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ex); err != nil {
		return nil, fmt.Errorf("exclusions: %w", err)
	}
	for i, e := range ex.Entries {
		if e.Class == "" || e.Reason == "" {
			return nil, fmt.Errorf("exclusions: entry %d needs a class and a reason", i+1)
		}
		if e.Repository == "" && e.Name == "" && e.NameSuffix == "" && e.Keyword == "" {
			return nil, fmt.Errorf("exclusions: entry %d has no match field", i+1)
		}
	}
	return &ex, nil
}

func (e exclusionEntry) matches(repo, name string, keywords []string) bool {
	if e.Repository != "" && e.Repository != repo {
		return false
	}
	if e.Name != "" && e.Name != name {
		return false
	}
	if e.NameSuffix != "" && !strings.HasSuffix(name, e.NameSuffix) {
		return false
	}
	if e.Keyword != "" {
		found := false
		for _, k := range keywords {
			if strings.EqualFold(k, e.Keyword) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// --- stored input ---------------------------------------------------------------

type detail struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Available []struct {
		Version string `json:"version"`
	} `json:"available_versions"`
	Deprecated bool     `json:"deprecated"`
	Keywords   []string `json:"keywords"`
	Links      []struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	} `json:"links"`
	Data struct {
		Type string `json:"type"`
	} `json:"data"`
	Repository struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"repository"`
}

// verifyDir checks every stored file against SHA256SUMS, refuses files the
// sums do not list, and returns the sums file's own sha256 (the input set's
// identity) and the verified contents by name.
func verifyDir(dir string) (string, map[string][]byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, sumsName))
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", sumsName, err)
	}
	listed := map[string]string{}
	for i, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		hash, name, ok := strings.Cut(line, "  ")
		if !ok || len(hash) != 64 || name == "" || name != path.Base(name) {
			return "", nil, fmt.Errorf("%s line %d is malformed", sumsName, i+1)
		}
		if _, dup := listed[name]; dup {
			return "", nil, fmt.Errorf("%s lists %s twice", sumsName, name)
		}
		listed[name] = hash
	}
	files := map[string][]byte{}
	for name, want := range listed {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", nil, fmt.Errorf("listed file missing: %w", err)
		}
		h := sha256.Sum256(body)
		if got := hex.EncodeToString(h[:]); got != want {
			return "", nil, fmt.Errorf("hash mismatch for %s: SHA256SUMS says %s, the file is %s", name, want, got)
		}
		files[name] = body
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	for _, e := range ents {
		if e.Name() == sumsName {
			continue
		}
		if _, ok := listed[e.Name()]; !ok {
			return "", nil, fmt.Errorf("%s is in the directory but not in %s", e.Name(), sumsName)
		}
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), files, nil
}

// --- output ---------------------------------------------------------------------

type Candidate struct {
	Rank              int    `json:"rank"`
	Stars             int    `json:"stars"`
	Package           string `json:"package"`
	Repository        string `json:"repository"`
	RepositoryURL     string `json:"repository_url"`
	Publisher         string `json:"publisher"`
	PublisherSource   string `json:"publisher_source"`
	PublisherLink     string `json:"publisher_link,omitempty"`
	Repo              string `json:"repo,omitempty"`
	Chart             string `json:"chart"`
	Version           string `json:"version"`
	SkippedPrerelease string `json:"skippedPrerelease,omitempty"`
}

type Skipped struct {
	Rank       int    `json:"rank"`
	Stars      int    `json:"stars"`
	Package    string `json:"package"`
	Repository string `json:"repository"`
	Reason     string `json:"reason"`
	Class      string `json:"class,omitempty"`
}

type Rules struct {
	Source       string `json:"source"`
	PublisherCap int    `json:"publisher_cap"`
	TieBreak     string `json:"tie_break"`
	Order        string `json:"order"`
}

type Counts struct {
	Ranked     int `json:"ranked"`
	Candidates int `json:"candidates"`
	Skipped    int `json:"skipped"`
	Publishers int `json:"publishers"`
}

type Output struct {
	InputSHA256      string      `json:"input_sha256"`
	ExclusionsSHA256 string      `json:"exclusions_sha256"`
	Rules            Rules       `json:"rules"`
	Counts           Counts      `json:"counts"`
	Candidates       []Candidate `json:"candidates"`
	Skipped          []Skipped   `json:"skipped"`
}

var sourceLinkName = regexp.MustCompile(`^(helm )?(chart )?sources?$`)

// publisher is the GitHub owner in the first source link that points at
// github.com, else the Artifact Hub repository name (section 2).
func publisher(d *detail, repo string) (pub, how, link string) {
	sawSource := false
	for _, l := range d.Links {
		if !sourceLinkName.MatchString(strings.ToLower(strings.TrimSpace(l.Name))) {
			continue
		}
		sawSource = true
		if owner := githubOwner(l.URL); owner != "" {
			return owner, "github-source-link", l.URL
		}
	}
	if sawSource {
		return repo, "repository-name: no source link on github.com", ""
	}
	return repo, "repository-name: no source link", ""
}

func githubOwner(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if h != "github.com" && h != "www.github.com" {
		return ""
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg != "" {
			return strings.ToLower(seg)
		}
	}
	return ""
}

// chartRef maps an Artifact Hub repository to the observe.sh entry shape: a
// classic repository gives repo + chart name; an OCI repository gives the full
// oci:// reference (Artifact Hub's OCI repository URL already ends with the
// chart name for single-chart repositories; otherwise the name is appended).
func chartRef(repoURL, name string) (repo, chart string) {
	u := strings.TrimRight(repoURL, "/")
	if strings.HasPrefix(u, "oci://") {
		if path.Base(u) == name {
			return "", u
		}
		return "", u + "/" + name
	}
	return u, name
}

type rankedPkg struct {
	searchPackage
	pos int
}

func build(files map[string][]byte, inputSHA string, ex *exclusionFile, exSHA string) (*Output, error) {
	var ranked []rankedPkg
	for i := 0; i < pages; i++ {
		name := searchFile(i * pageSize)
		body, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("stored input lacks %s", name)
		}
		var pg searchPage
		if err := json.Unmarshal(body, &pg); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, p := range pg.Packages {
			ranked = append(ranked, rankedPkg{p, len(ranked)})
		}
	}
	// The API must have returned stars in descending order; if not, the
	// ranking is not what the pre-registration says and nothing is guessed.
	for i := 1; i < len(ranked); i++ {
		if ranked[i].Stars > ranked[i-1].Stars {
			return nil, fmt.Errorf("search results are not sorted by stars at position %d (%s has %d, previous %d)",
				i+1, ranked[i].Name, ranked[i].Stars, ranked[i-1].Stars)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.Stars != b.Stars {
			return a.Stars > b.Stars
		}
		if a.pos != b.pos {
			return a.pos < b.pos
		}
		if a.Repository.Name != b.Repository.Name {
			return a.Repository.Name < b.Repository.Name
		}
		return a.Name < b.Name
	})

	out := &Output{
		InputSHA256:      inputSHA,
		ExclusionsSHA256: exSHA,
		Rules: Rules{
			Source:       "Artifact Hub /api/v1/packages/search?kind=0&sort=stars, offsets 0 to 240 at limit 60 (top 300 Helm packages)",
			PublisherCap: publisherCap,
			TieBreak:     tieBreakRule,
			Order:        "candidates and skipped are both in rank order",
		},
		Candidates: []Candidate{},
		Skipped:    []Skipped{},
	}
	perPublisher := map[string]int{}
	seen := map[string]bool{}
	for i, p := range ranked {
		rank := i + 1
		repo, name := p.Repository.Name, p.Name
		skip := func(class, reason string) {
			out.Skipped = append(out.Skipped, Skipped{Rank: rank, Stars: p.Stars, Package: name, Repository: repo, Reason: reason, Class: class})
		}
		if seen[p.PackageID] {
			skip("", "duplicate: the same package id appeared earlier in the ranking")
			continue
		}
		seen[p.PackageID] = true
		if p.Deprecated {
			skip("deprecated", "deprecated: the search result says deprecated")
			continue
		}
		body, ok := files[detailFile(repo, p.urlName())]
		if !ok {
			return nil, fmt.Errorf("stored input lacks the package detail for %s/%s (rank %d)", repo, name, rank)
		}
		var d detail
		if err := json.Unmarshal(body, &d); err != nil {
			return nil, fmt.Errorf("package detail %s/%s: %w", repo, name, err)
		}
		if d.Deprecated {
			skip("deprecated", "deprecated: the package detail says deprecated")
			continue
		}
		if d.Data.Type == "library" {
			skip("library", "library: the chart type (data.type) is library")
			continue
		}
		excluded := false
		for i, e := range ex.Entries {
			if e.matches(repo, name, d.Keywords) {
				skip(e.Class, fmt.Sprintf("%s: %s (exclusions.json entry %d)", e.Class, e.Reason, i+1))
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		pub, how, link := publisher(&d, repo)
		if perPublisher[pub] >= publisherCap {
			skip("publisher-cap", fmt.Sprintf("publisher cap: %q already has %d charts in the list", pub, publisherCap))
			continue
		}
		var listed []string
		for _, v := range d.Available {
			listed = append(listed, v.Version)
		}
		if len(listed) == 0 && d.Version != "" {
			listed = []string{d.Version}
		}
		pinned, passed, found := pinVersion(listed, d.Version)
		if !found {
			skip("no-stable-version", "no-stable-version: none of the listed versions is free of a pre-release part")
			continue
		}
		perPublisher[pub]++
		rurl := p.Repository.URL
		if rurl == "" {
			rurl = d.Repository.URL
		}
		crepo, chart := chartRef(rurl, p.urlName())
		out.Candidates = append(out.Candidates, Candidate{
			Rank: rank, Stars: p.Stars, Package: name, Repository: repo, RepositoryURL: rurl,
			Publisher: pub, PublisherSource: how, PublisherLink: link,
			Repo: crepo, Chart: chart, Version: pinned, SkippedPrerelease: passed,
		})
	}
	out.Counts = Counts{Ranked: len(ranked), Candidates: len(out.Candidates), Skipped: len(out.Skipped), Publishers: len(perPublisher)}
	return out, nil
}

func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// buildFile works only from the stored responses; it has no network code path.
func buildFile(in, out string) error {
	inputSHA, files, err := verifyDir(in)
	if err != nil {
		return err
	}
	ex, err := loadExclusions(embeddedExclusions)
	if err != nil {
		return err
	}
	h := sha256.Sum256(embeddedExclusions)
	res, err := build(files, inputSHA, ex, hex.EncodeToString(h[:]))
	if err != nil {
		return err
	}
	data, err := marshal(res)
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o644)
}

// --- charts shape ---------------------------------------------------------------

// ChartEntry is the shape hack/compat-pilot/observe.sh reads from charts.json,
// with defaults only: no values, no deadline.
type ChartEntry struct {
	ID      string `json:"id"`
	Repo    string `json:"repo,omitempty"`
	Chart   string `json:"chart"`
	Version string `json:"version"`
	Why     string `json:"why"`
}

// toCharts turns candidates into chart entries. The id is the package name;
// when two candidates share a name, both use <repository>-<name>.
func toCharts(cands []Candidate) ([]ChartEntry, error) {
	count := map[string]int{}
	for _, c := range cands {
		count[c.Package]++
	}
	var out []ChartEntry
	ids := map[string]bool{}
	for _, c := range cands {
		id := c.Package
		if count[c.Package] > 1 {
			id = c.Repository + "-" + c.Package
		}
		if len(id) > 53 {
			return nil, fmt.Errorf("chart id %q is longer than 53 characters (Helm release name limit)", id)
		}
		if ids[id] {
			return nil, fmt.Errorf("chart id %q is not unique", id)
		}
		ids[id] = true
		out = append(out, ChartEntry{
			ID: id, Repo: c.Repo, Chart: c.Chart, Version: c.Version,
			Why: fmt.Sprintf("Artifact Hub top 300 by stars, rank %d, %d stars, publisher %s", c.Rank, c.Stars, c.Publisher),
		})
	}
	if out == nil {
		out = []ChartEntry{}
	}
	return out, nil
}

func chartsFile(in, out string) error {
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var o Output
	if err := json.Unmarshal(raw, &o); err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	entries, err := toCharts(o.Candidates)
	if err != nil {
		return err
	}
	data, err := marshal(entries)
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o644)
}

// parseVersion reads MAJOR.MINOR.PATCH (more numeric parts allowed), with an
// optional leading v. pre reports a pre-release part; build metadata after +
// is dropped and never affects precedence or stability.
func parseVersion(v string) (nums []int, pre bool, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		pre = true
		v = v[:i]
	}
	if v == "" {
		return nil, false, false
	}
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false, false
		}
		nums = append(nums, n)
	}
	return nums, pre, true
}

func cmpNums(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// pinVersion returns the highest listed version with no pre-release part, by
// semantic-version precedence, ignoring Artifact Hub's prerelease flag. When
// the newest listed version (newest) is a pre-release that was passed over, it
// is returned as passed. found is false when no stable version is listed.
func pinVersion(listed []string, newest string) (pinned, passed string, found bool) {
	var best []int
	for _, v := range listed {
		nums, pre, ok := parseVersion(v)
		if !ok || pre {
			continue
		}
		if !found || cmpNums(nums, best) > 0 {
			best, pinned, found = nums, v, true
		}
	}
	if found {
		if _, pre, ok := parseVersion(newest); ok && pre {
			passed = newest
		}
	}
	return
}
