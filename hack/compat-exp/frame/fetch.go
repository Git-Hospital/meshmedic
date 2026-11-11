package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	apiBase   = "https://artifacthub.io/api/v1"
	userAgent = "meshmedic-compat-exp"
	pageSize  = 60
	pages     = 5 // offsets 0, 60, 120, 180, 240: the top 300
	sumsName  = "SHA256SUMS"
	// maxBody guards against a runaway response; real ones are well under it.
	maxBody = 32 << 20
)

// doer is the one network seam: fetch takes it, build never sees it.
type doer interface {
	Do(*http.Request) (*http.Response, error)
}

func newHTTPDoer(timeout time.Duration) doer {
	return &http.Client{Timeout: timeout}
}

func searchFile(offset int) string { return fmt.Sprintf("search-%03d.json", offset) }

var safeName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// detailFile names the stored package response. Two repositories can carry
// the same chart name, so the repository is part of the name.
func detailFile(repo, name string) string { return "pkg-" + repo + "__" + name + ".json" }

// searchPackage is the part of a search result the tool reads.
type searchPackage struct {
	PackageID      string `json:"package_id"`
	Name           string `json:"name"`
	NormalizedName string `json:"normalized_name"`
	Stars          int    `json:"stars"`
	Deprecated     bool   `json:"deprecated"`
	Version        string `json:"version"`
	Repository     struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Kind int    `json:"kind"`
	} `json:"repository"`
}

func (p searchPackage) urlName() string {
	if p.NormalizedName != "" {
		return p.NormalizedName
	}
	return p.Name
}

type searchPage struct {
	Packages []searchPackage `json:"packages"`
}

func get(d doer, u string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := d.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("GET %s: reading body: %w", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d (expected 200)", u, resp.StatusCode)
	}
	return body, nil
}

// fetchAll stores the raw bodies byte for byte, then SHA256SUMS. Requests are
// sequential with a pause. The directory must not hold earlier data, so a
// stored set always comes from one pass.
func fetchAll(d doer, dir string, pause time.Duration, log io.Writer) error {
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		return fmt.Errorf("fetch: %s is not empty; use a new directory", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sums := map[string]string{}
	store := func(name string, body []byte) error {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			return err
		}
		h := sha256.Sum256(body)
		sums[name] = hex.EncodeToString(h[:])
		return nil
	}
	first := true
	wait := func() {
		if !first {
			time.Sleep(pause)
		}
		first = false
	}

	var ranked []searchPackage
	for i := 0; i < pages; i++ {
		off := i * pageSize
		wait()
		u := fmt.Sprintf("%s/packages/search?kind=0&sort=stars&limit=%d&offset=%d", apiBase, pageSize, off)
		body, err := get(d, u)
		if err != nil {
			return err
		}
		var pg searchPage
		if err := json.Unmarshal(body, &pg); err != nil {
			return fmt.Errorf("search offset %d: not the expected JSON: %w", off, err)
		}
		if len(pg.Packages) == 0 {
			return fmt.Errorf("search offset %d: no packages returned", off)
		}
		if err := store(searchFile(off), body); err != nil {
			return err
		}
		ranked = append(ranked, pg.Packages...)
		fmt.Fprintf(log, "search offset %d: %d packages\n", off, len(pg.Packages))
	}

	// Detail only for packages build can still select; deprecated ones are
	// skipped on the search flag alone.
	seen := map[string]bool{}
	n := 0
	for _, p := range ranked {
		if p.Deprecated || seen[p.PackageID] {
			continue
		}
		seen[p.PackageID] = true
		repo, name := p.Repository.Name, p.urlName()
		if !safeName.MatchString(repo) || !safeName.MatchString(name) {
			return fmt.Errorf("package %q in repository %q: name is not a plain slug", name, repo)
		}
		wait()
		u := fmt.Sprintf("%s/packages/helm/%s/%s", apiBase, url.PathEscape(repo), url.PathEscape(name))
		body, err := get(d, u)
		if err != nil {
			return err
		}
		if err := store(detailFile(repo, name), body); err != nil {
			return err
		}
		n++
	}
	fmt.Fprintf(log, "details: %d packages\n", n)
	return writeSums(filepath.Join(dir, sumsName), sums)
}

func writeSums(path string, sums map[string]string) error {
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[n], n)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
