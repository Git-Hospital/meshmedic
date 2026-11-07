// Package prom is a minimal client for the Prometheus HTTP API, covering
// exactly what the detector needs: instant queries, reduced to one number
// for signals and to labeled samples for evidence.
package prom

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrNoData reports a query that returned an empty vector. Callers decide
// whether absence of data means healthy or broken; the client does not guess.
var ErrNoData = errors.New("query returned no samples")

// Sample is one labeled sample from an instant query's result vector.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Client talks to one Prometheus server.
type Client struct {
	base string
	http *http.Client
	auth Auth
}

// Auth is how a client proves itself to a Prometheus behind authentication,
// and which extra CA it trusts. The zero value sends no credentials and
// trusts the system roots. BearerToken and Username/Password are
// alternatives; the caller resolves them from the environment, so they never
// sit in a config file.
type Auth struct {
	BearerToken string
	Username    string
	Password    string
	// CAFile is a PEM file of CA certificates trusted in addition to the
	// system roots, for a Prometheus with a certificate from a private CA.
	CAFile string
}

// NewClient builds a client for the given base URL, e.g. http://localhost:9090.
func NewClient(baseURL string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// NewClientWithAuth builds a client that sends the given credentials with
// every request and trusts auth.CAFile as well as the system roots.
func NewClientWithAuth(baseURL string, auth Auth) (*Client, error) {
	if auth.BearerToken != "" && (auth.Username != "" || auth.Password != "") {
		return nil, errors.New("prometheus: a bearer token and basic auth are alternatives; set one")
	}
	c := NewClient(baseURL)
	c.auth = auth
	if auth.BearerToken != "" || auth.Username != "" || auth.Password != "" {
		// net/http keeps the Authorization header on a redirect to the same host, whatever the
		// scheme, so an https server redirecting to http would get the credential in plain text.
		c.http.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing a redirect from https to %s, which would send the credentials in plain text", req.URL.Scheme)
			}
			return nil
		}
	}
	if auth.CAFile != "" {
		pem, err := os.ReadFile(auth.CAFile)
		if err != nil {
			return nil, fmt.Errorf("prometheus: CA file: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("prometheus: CA file %s holds no PEM certificate", auth.CAFile)
		}
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		c.http.Transport = t
	}
	return c, nil
}

// get sends a GET for path (with its query string) to the server, with the
// client's credentials. Every request goes through here, so none can be sent
// without them. On a redirect, net/http keeps the Authorization header only
// for the same domain or a subdomain of it, and NewClientWithAuth refuses a
// redirect from https to http.
func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	switch {
	case c.auth.BearerToken != "":
		req.Header.Set("Authorization", "Bearer "+c.auth.BearerToken)
	case c.auth.Username != "" || c.auth.Password != "":
		req.SetBasicAuth(c.auth.Username, c.auth.Password)
	}
	return c.http.Do(req)
}

// Query runs an instant query and returns the single sample's value.
// A result with more than one sample is an error: catalog signals must
// aggregate, otherwise the threshold comparison is meaningless.
func (c *Client) Query(ctx context.Context, promql string) (float64, error) {
	samples, err := c.instant(ctx, promql)
	if err != nil {
		return 0, err
	}
	if len(samples) > 1 {
		return 0, fmt.Errorf("prometheus: %d samples, catalog signals must aggregate to one", len(samples))
	}
	return samples[0].Value, nil
}

// QuerySeries runs an instant query and returns every sample with its
// labels intact. Evidence queries use it: a per-workload breakdown is only
// evidence if the workload names survive into the report.
func (c *Client) QuerySeries(ctx context.Context, promql string) ([]Sample, error) {
	return c.instant(ctx, promql)
}

// instant runs the query and decodes the full result vector. An empty
// vector is ErrNoData.
func (c *Client) instant(ctx context.Context, promql string) ([]Sample, error) {
	resp, err := c.get(ctx, "/api/v1/query?query="+url.QueryEscape(promql))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus: %s", resp.Status)
	}

	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("prometheus: decoding response: %w", err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus: response status %q", body.Status)
	}
	if body.Data.ResultType != "vector" {
		return nil, fmt.Errorf("prometheus: unexpected result type %q", body.Data.ResultType)
	}
	if len(body.Data.Result) == 0 {
		return nil, ErrNoData
	}
	samples := make([]Sample, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		s, ok := r.Value[1].(string)
		if !ok {
			return nil, fmt.Errorf("prometheus: malformed sample value")
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("prometheus: malformed sample value: %w", err)
		}
		samples = append(samples, Sample{Labels: r.Metric, Value: v})
	}
	return samples, nil
}

// MetricNames lists every metric name the server currently knows about.
// It is what makes a catalog entry checkable before an incident: a signal
// naming a metric that is not here can never fire, and the coverage is lost
// silently rather than loudly.
func (c *Client) MetricNames(ctx context.Context) (map[string]bool, error) {
	return c.labelValues(ctx, "/api/v1/label/__name__/values")
}

// LabelNames lists every label key the server currently knows about. Istio
// renames labels between versions, and a matcher on a label that no longer
// exists matches nothing at all rather than erroring.
func (c *Client) LabelNames(ctx context.Context) (map[string]bool, error) {
	return c.labelValues(ctx, "/api/v1/labels")
}

// labelValues fetches one of Prometheus's string-list endpoints.
func (c *Client) labelValues(ctx context.Context, path string) (map[string]bool, error) {
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus: %s: %s", path, resp.Status)
	}
	var body struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("prometheus: decoding %s: %w", path, err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s: response status %q", path, body.Status)
	}
	out := make(map[string]bool, len(body.Data))
	for _, v := range body.Data {
		out[v] = true
	}
	return out, nil
}
