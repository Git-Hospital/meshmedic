// Package gitops opens remediation pull requests. MeshMedic never writes to
// the cluster; this package is its only side effect, and the result is a
// reviewable pull request in the config repository.
package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// Client opens pull requests against one GitHub config repository.
type Client struct {
	api   string
	owner string
	repo  string
	token string
	http  *http.Client
	sleep func(context.Context, time.Duration) error // waits between attempts; tests replace it
	now   func() time.Time
}

// maxRateLimitWait bounds how long one request waits for a rate limit to lift. A primary rate
// limit can reset an hour away; the incident is handed back to the detector instead.
const maxRateLimitWait = 2 * time.Minute

// NewClient builds a client for owner/repo. An empty apiBase means the
// public GitHub API; tests point it at a stub server.
func NewClient(apiBase, ownerRepo, token string) (*Client, error) {
	owner, repo, ok := strings.Cut(ownerRepo, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("gitops repo must be owner/repo, got %q", ownerRepo)
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &Client{
		api:   strings.TrimRight(apiBase, "/"),
		owner: owner,
		repo:  repo,
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
		sleep: sleepCtx,
		now:   time.Now,
	}, nil
}

// PullRequest is one remediation change to propose.
type PullRequest struct {
	Branch        string
	Base          string // empty means the repository's default branch
	Title         string
	Body          string
	Path          string // file path inside the config repository
	Content       []byte // the rendered patch
	CommitMessage string
}

// Open creates the branch from base, commits the patch file, and opens the
// pull request. It returns the PR's HTML URL.
func (c *Client) Open(ctx context.Context, pr PullRequest) (string, error) {
	base := pr.Base
	if base == "" {
		var repoInfo struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s", c.owner, c.repo), nil, &repoInfo); err != nil {
			return "", fmt.Errorf("resolving default branch: %w", err)
		}
		base = repoInfo.DefaultBranch
	}

	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", c.owner, c.repo, base), nil, &ref); err != nil {
		return "", fmt.Errorf("resolving base %s: %w", base, err)
	}

	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/refs", c.owner, c.repo), map[string]string{
		"ref": "refs/heads/" + pr.Branch,
		"sha": ref.Object.SHA,
	}, nil); err != nil {
		// BranchFor names one branch per breach episode, so a retry after a failed commit or
		// pull request step finds the branch it made last time and goes on with it (#22).
		if !isStatus(err, http.StatusUnprocessableEntity) ||
			c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", c.owner, c.repo, pr.Branch), nil, nil) != nil {
			return "", fmt.Errorf("creating branch %s: %w", pr.Branch, err)
		}
	}

	// If the patch file already exists (a previous remediation merged),
	// the contents API needs its blob sha to update instead of create.
	sha := ""
	var existing struct {
		SHA string `json:"sha"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", c.owner, c.repo, pr.Path, pr.Branch), nil, &existing)
	switch {
	case err == nil:
		sha = existing.SHA
	case !isNotFound(err):
		return "", fmt.Errorf("checking %s: %w", pr.Path, err)
	}

	payload := map[string]string{
		"message": pr.CommitMessage,
		"content": base64.StdEncoding.EncodeToString(pr.Content),
		"branch":  pr.Branch,
	}
	if sha != "" {
		payload["sha"] = sha
	}
	if err := c.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/%s/contents/%s", c.owner, c.repo, pr.Path), payload, nil); err != nil {
		return "", fmt.Errorf("committing %s: %w", pr.Path, err)
	}

	var opened struct {
		HTMLURL string `json:"html_url"`
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", c.owner, c.repo), map[string]string{
		"title": pr.Title,
		"head":  pr.Branch,
		"base":  base,
		"body":  pr.Body,
	}, &opened); err != nil {
		// The pull request can exist already when an earlier attempt opened it but its response
		// was lost; that one is the result.
		if isStatus(err, http.StatusUnprocessableEntity) {
			if url, ok := c.openPullFor(ctx, pr.Branch); ok {
				return url, nil
			}
		}
		return "", fmt.Errorf("opening pull request: %w", err)
	}
	return opened.HTMLURL, nil
}

// openPullFor returns the URL of the open pull request whose head is branch, if there is one.
func (c *Client) openPullFor(ctx context.Context, branch string) (string, bool) {
	var pulls []struct {
		HTMLURL string `json:"html_url"`
	}
	q := url.Values{"head": {c.owner + ":" + branch}, "state": {"open"}}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls?%s", c.owner, c.repo, q.Encode()), nil, &pulls); err != nil || len(pulls) == 0 {
		return "", false
	}
	return pulls[0].HTMLURL, true
}

// BranchFor names the remediation branch for one breach episode: the scenario, a short hash of
// the target's parameters and the time the breach began. Retries of the same episode get the
// same name, so a failed attempt's branch is reused instead of left behind (#22); a later
// episode, or another target breaching in the same second, gets a different one.
func BranchFor(scenarioID string, params map[string]string, since time.Time) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, params[k])
	}
	return fmt.Sprintf("meshmedic/%s-%x-%d", scenarioID, h.Sum(nil)[:4], since.Unix())
}

// PathFor renders the configured path template with the incident parameters
// plus a "scenario" key holding the scenario ID.
func PathFor(tmpl string, params map[string]string, scenarioID string) (string, error) {
	values := map[string]string{"scenario": scenarioID}
	for k, v := range params {
		values[k] = v
	}
	t, err := template.New("path").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("path template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, values); err != nil {
		return "", fmt.Errorf("path template: %w", err)
	}
	return buf.String(), nil
}

type apiError struct {
	status      int
	body        string
	rateLimited bool          // 429, or 403 with a rate-limit header
	retryAfter  time.Duration // how long GitHub asked us to wait; 0 when it did not say
}

func (e *apiError) Error() string {
	return fmt.Sprintf("github: HTTP %d: %s", e.status, e.body)
}

func isNotFound(err error) bool {
	return isStatus(err, http.StatusNotFound)
}

func isStatus(err error, status int) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == status
}

// rateLimit reads GitHub's rate-limit signals: 429, or 403 with Retry-After or with
// X-RateLimit-Remaining at 0. The wait comes from Retry-After (seconds), else from
// X-RateLimit-Reset (a Unix time). A 403 with neither header is a permission error.
func rateLimit(resp *http.Response, now time.Time) (bool, time.Duration) {
	retryAfter := resp.Header.Get("Retry-After")
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
	case resp.StatusCode == http.StatusForbidden && (retryAfter != "" || resp.Header.Get("X-RateLimit-Remaining") == "0"):
	default:
		return false, 0
	}
	if n, err := strconv.Atoi(retryAfter); err == nil && n >= 0 {
		return true, time.Duration(n) * time.Second
	}
	if n, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		if d := time.Unix(n, 0).Sub(now); d > 0 {
			return true, d
		}
	}
	return true, 0
}

// do sends one API request, retrying transient failures. GitHub answers
// with 5xx during brownouts; losing an incident's pull request to one of
// those is not acceptable, so network errors and 5xx responses get three
// attempts with backoff before giving up. Rate-limit responses are retried
// too, after the wait GitHub asked for when that is longer than the backoff,
// up to maxRateLimitWait.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			wait := time.Duration(attempt) * 2 * time.Second
			var ae *apiError
			if errors.As(lastErr, &ae) && ae.retryAfter > wait {
				wait = ae.retryAfter
			}
			if err := c.sleep(ctx, wait); err != nil {
				return err
			}
		}
		lastErr = c.doOnce(ctx, method, path, payload, out)
		if lastErr == nil || !isTransient(lastErr) {
			return lastErr
		}
		var ae *apiError
		if errors.As(lastErr, &ae) && ae.retryAfter > maxRateLimitWait {
			return fmt.Errorf("%w (rate limited for %s, longer than the %s this client waits)",
				lastErr, ae.retryAfter.Round(time.Second), maxRateLimitWait)
		}
	}
	return lastErr
}

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func isTransient(err error) bool {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status >= 500 || ae.rateLimited
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var je *json.SyntaxError
	if errors.As(err, &je) {
		return false
	}
	// Anything else never produced an HTTP status: a network problem.
	return true
}

func (c *Client) doOnce(ctx context.Context, method, path string, payload []byte, out any) error {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("github: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 1024 {
			msg = msg[:1024] + "..."
		}
		ae := &apiError{status: resp.StatusCode, body: msg}
		ae.rateLimited, ae.retryAfter = rateLimit(resp, c.now())
		return ae
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
