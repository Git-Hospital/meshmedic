package gitops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeGitHub records the API calls Open makes and serves canned responses.
type fakeGitHub struct {
	t          *testing.T
	calls      []string
	fileExists bool
	// branchExists and pullExists play a retry of an episode whose earlier attempt made the
	// branch, or opened the pull request but lost the response.
	branchExists bool
	pullExists   bool
	putBody      map[string]string
	pullBody     map[string]string
}

func (f *fakeGitHub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			f.t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		key := r.Method + " " + r.URL.Path
		f.calls = append(f.calls, key)
		switch {
		case key == "GET /repos/o/r":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case key == "GET /repos/o/r/git/ref/heads/main":
			fmt.Fprint(w, `{"object":{"sha":"abc123"}}`)
		case key == "POST /repos/o/r/git/refs":
			if f.branchExists {
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprint(w, `{"message":"Reference already exists"}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case key == "GET /repos/o/r/git/ref/heads/meshmedic/test-1":
			if f.branchExists {
				fmt.Fprint(w, `{"object":{"sha":"def789"}}`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/o/r/contents/"):
			if f.fileExists {
				fmt.Fprint(w, `{"sha":"blob456"}`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/repos/o/r/contents/"):
			json.NewDecoder(r.Body).Decode(&f.putBody)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case key == "POST /repos/o/r/pulls" && f.pullExists:
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"A pull request already exists for o:meshmedic/test-1."}`)
		case key == "GET /repos/o/r/pulls":
			if got := r.URL.Query().Get("head"); got != "o:meshmedic/test-1" {
				f.t.Errorf("pull lookup head = %q", got)
			}
			fmt.Fprint(w, `[{"html_url":"https://github.com/o/r/pull/7"}]`)
		case key == "POST /repos/o/r/pulls":
			json.NewDecoder(r.Body).Decode(&f.pullBody)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"html_url":"https://github.com/o/r/pull/1"}`)
		default:
			f.t.Errorf("unexpected call %s", key)
			w.WriteHeader(http.StatusTeapot)
		}
	})
}

func testPR() PullRequest {
	return PullRequest{
		Branch:        "meshmedic/test-1",
		Title:         "[meshmedic] test",
		Body:          "## Incident\nreport body",
		Path:          "istio/demo/payments.yaml",
		Content:       []byte("kind: VirtualService\n"),
		CommitMessage: "meshmedic: shift traffic",
	}
}

func TestOpenCreatesBranchCommitAndPR(t *testing.T) {
	f := &fakeGitHub{t: t}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	c, err := NewClient(srv.URL, "o/r", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	url, err := c.Open(context.Background(), testPR())
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/o/r/pull/1" {
		t.Fatalf("got url %q", url)
	}

	want := []string{
		"GET /repos/o/r",
		"GET /repos/o/r/git/ref/heads/main",
		"POST /repos/o/r/git/refs",
		"GET /repos/o/r/contents/istio/demo/payments.yaml",
		"PUT /repos/o/r/contents/istio/demo/payments.yaml",
		"POST /repos/o/r/pulls",
	}
	if len(f.calls) != len(want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, f.calls[i], want[i])
		}
	}

	decoded, err := base64.StdEncoding.DecodeString(f.putBody["content"])
	if err != nil || string(decoded) != "kind: VirtualService\n" {
		t.Fatalf("committed content %q (decode err %v)", decoded, err)
	}
	if _, hasSHA := f.putBody["sha"]; hasSHA {
		t.Fatal("sha sent for a new file")
	}
	if f.pullBody["body"] != "## Incident\nreport body" {
		t.Fatalf("PR body %q, report must pass through unchanged", f.pullBody["body"])
	}
	if f.pullBody["base"] != "main" {
		t.Fatalf("PR base %q, want resolved default branch", f.pullBody["base"])
	}
}

func TestOpenUpdatesExistingFileWithSHA(t *testing.T) {
	f := &fakeGitHub{t: t, fileExists: true}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	c, _ := NewClient(srv.URL, "o/r", "test-token")
	if _, err := c.Open(context.Background(), testPR()); err != nil {
		t.Fatal(err)
	}
	if f.putBody["sha"] != "blob456" {
		t.Fatalf("sha %q, want existing blob sha for an update", f.putBody["sha"])
	}
}

func TestOpenRetriesTransient5xx(t *testing.T) {
	f := &fakeGitHub{t: t}
	inner := f.handler()
	failures := 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failures > 0 {
			failures--
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "brownout")
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "o/r", "test-token")
	url, err := c.Open(context.Background(), testPR())
	if err != nil {
		t.Fatalf("a single 503 must be retried, got %v", err)
	}
	if url != "https://github.com/o/r/pull/1" {
		t.Fatalf("got url %q", url)
	}
}

func TestOpenDoesNotRetry4xx(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"message":"Validation Failed"}`)
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "o/r", "test-token")
	if _, err := c.Open(context.Background(), testPR()); err == nil {
		t.Fatal("want error")
	}
	if calls != 1 {
		t.Fatalf("4xx retried %d times, client errors must fail fast", calls)
	}
}

func TestNewClientRejectsBareRepoName(t *testing.T) {
	if _, err := NewClient("", "just-a-name", "t"); err == nil {
		t.Fatal("want error for repo without owner/")
	}
}

func TestPathFor(t *testing.T) {
	got, err := PathFor("istio/{{.namespace}}/{{.scenario}}.yaml",
		map[string]string{"namespace": "demo"}, "canary-latency-rollback")
	if err != nil {
		t.Fatal(err)
	}
	if got != "istio/demo/canary-latency-rollback.yaml" {
		t.Fatalf("got %q", got)
	}
	if _, err := PathFor("{{.missing}}", map[string]string{}, "x"); err == nil {
		t.Fatal("want error for missing path parameter")
	}
}

func TestBranchForIsUniquePerEpisode(t *testing.T) {
	payments := map[string]string{"service": "payments", "namespace": "demo"}
	a := BranchFor("x", payments, time.Unix(100, 0))
	if b := BranchFor("x", payments, time.Unix(101, 0)); a == b {
		t.Fatal("branches for different episodes must differ")
	}
	if b := BranchFor("x", map[string]string{"service": "orders", "namespace": "demo"}, time.Unix(100, 0)); a == b {
		t.Fatal("two targets breaching in the same second must get different branches")
	}
	// The same episode retried gets the same branch, whatever the map order.
	if b := BranchFor("x", map[string]string{"namespace": "demo", "service": "payments"}, time.Unix(100, 0)); a != b {
		t.Fatalf("a retry of one episode got %q, then %q", a, b)
	}
}

// rateLimitedOnce answers the first request with status and headers, then hands every request
// to the fake GitHub.
func rateLimitedOnce(t *testing.T, status int, headers map[string]string) (*httptest.Server, *int) {
	t.Helper()
	inner := (&fakeGitHub{t: t}).handler()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			fmt.Fprint(w, `{"message":"You have exceeded a secondary rate limit."}`)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// recordSleeps replaces the client's wait so a test sees each wait without taking it.
func recordSleeps(c *Client) *[]time.Duration {
	var waits []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	return &waits
}

func TestOpenWaitsOutRateLimits(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		want    time.Duration
	}{
		{"429 with Retry-After", http.StatusTooManyRequests, map[string]string{"Retry-After": "7"}, 7 * time.Second},
		{"429 without a wait", http.StatusTooManyRequests, nil, 2 * time.Second},
		{"403 with Retry-After", http.StatusForbidden, map[string]string{"Retry-After": "30"}, 30 * time.Second},
		{"403 with the limit used up", http.StatusForbidden, map[string]string{
			"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(now.Add(45 * time.Second).Unix()),
		}, 45 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := rateLimitedOnce(t, tc.status, tc.headers)
			c, _ := NewClient(srv.URL, "o/r", "test-token")
			c.now = func() time.Time { return now }
			waits := recordSleeps(c)
			if _, err := c.Open(context.Background(), testPR()); err != nil {
				t.Fatalf("a rate limit must be waited out, got %v", err)
			}
			if len(*waits) != 1 || (*waits)[0] != tc.want {
				t.Fatalf("waits = %v, want [%s]", *waits, tc.want)
			}
		})
	}
}

func TestOpenDoesNotRetryAPlain403(t *testing.T) {
	srv, calls := rateLimitedOnce(t, http.StatusForbidden, nil)
	c, _ := NewClient(srv.URL, "o/r", "test-token")
	waits := recordSleeps(c)
	if _, err := c.Open(context.Background(), testPR()); err == nil {
		t.Fatal("a 403 without a rate-limit header is a permission error and must fail")
	}
	if *calls != 1 || len(*waits) != 0 {
		t.Fatalf("plain 403 retried: %d calls, waits %v", *calls, *waits)
	}
}

func TestOpenGivesUpOnALongRateLimit(t *testing.T) {
	srv, calls := rateLimitedOnce(t, http.StatusTooManyRequests, map[string]string{"Retry-After": "3600"})
	c, _ := NewClient(srv.URL, "o/r", "test-token")
	waits := recordSleeps(c)
	_, err := c.Open(context.Background(), testPR())
	if err == nil || !strings.Contains(err.Error(), "rate limited for 1h0m0s") {
		t.Fatalf("want an error naming the hour-long limit, got %v", err)
	}
	if *calls != 1 || len(*waits) != 0 {
		t.Fatalf("waited for an hour-long limit: %d calls, waits %v", *calls, *waits)
	}
}

func TestOpenStopsWaitingWhenTheContextEnds(t *testing.T) {
	srv, calls := rateLimitedOnce(t, http.StatusTooManyRequests, map[string]string{"Retry-After": "60"})
	c, _ := NewClient(srv.URL, "o/r", "test-token")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	_, err := c.Open(ctx, testPR())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > 5*time.Second || *calls != 1 {
		t.Fatalf("the wait did not end with the context: %s, %d calls", time.Since(start), *calls)
	}
}

// A retry of an episode reuses the branch the failed attempt made, and returns the pull
// request an earlier attempt opened, instead of leaving a branch with no pull request (#22).
func TestOpenReusesTheEpisodeBranchAndPullRequest(t *testing.T) {
	f := &fakeGitHub{t: t, branchExists: true, pullExists: true}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c, _ := NewClient(srv.URL, "o/r", "test-token")
	url, err := c.Open(context.Background(), testPR())
	if err != nil {
		t.Fatalf("a retry must go on with the existing branch, got %v", err)
	}
	if url != "https://github.com/o/r/pull/7" {
		t.Fatalf("want the existing pull request, got %q", url)
	}
	if f.putBody["branch"] != "meshmedic/test-1" {
		t.Fatalf("the patch was not committed to the reused branch: %v", f.putBody)
	}
}
