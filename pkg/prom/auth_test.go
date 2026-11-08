package prom

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// tlsPrometheus answers both kinds of request the client makes, an instant query and a label
// list, over TLS with a certificate from httptest's own CA, and records each Authorization header.
func tlsPrometheus(t *testing.T) (srv *httptest.Server, caFile string, seen func() []string) {
	t.Helper()
	var mu sync.Mutex
	var headers []string
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/query":
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"1"]}]}}`)
		case "/api/v1/label/__name__/values":
			fmt.Fprint(w, `{"status":"success","data":["up"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	caFile = filepath.Join(t.TempDir(), "ca.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, block, 0o600); err != nil {
		t.Fatal(err)
	}
	return srv, caFile, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), headers...)
	}
}

// both makes one request of each kind.
func both(t *testing.T, c *Client) {
	t.Helper()
	if _, err := c.Query(context.Background(), "up"); err != nil {
		t.Fatalf("query: %v", err)
	}
	if _, err := c.MetricNames(context.Background()); err != nil {
		t.Fatalf("metric names: %v", err)
	}
}

func TestAuthHeaderOnEveryRequest(t *testing.T) {
	for name, tc := range map[string]struct {
		auth Auth
		want string
	}{
		"bearer token":   {Auth{BearerToken: "t0k3n"}, "Bearer t0k3n"},
		"basic auth":     {Auth{Username: "meshmedic", Password: "pw"}, "Basic bWVzaG1lZGljOnB3"},
		"not configured": {Auth{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			srv, caFile, seen := tlsPrometheus(t)
			tc.auth.CAFile = caFile
			c, err := NewClientWithAuth(srv.URL, tc.auth)
			if err != nil {
				t.Fatal(err)
			}
			both(t, c)
			got := seen()
			if len(got) != 2 {
				t.Fatalf("requests %q, want two", got)
			}
			for _, h := range got {
				if _, auth, _ := strings.Cut(h, " "); auth != tc.want {
					t.Errorf("request %q, want Authorization %q", h, tc.want)
				}
			}
		})
	}
}

func TestCAFile(t *testing.T) {
	srv, caFile, _ := tlsPrometheus(t)
	c, err := NewClientWithAuth(srv.URL, Auth{BearerToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.MetricNames(context.Background()); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("a server from an untrusted CA must be refused, got %v", err)
	}
	c, err = NewClientWithAuth(srv.URL, Auth{BearerToken: "t", CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	both(t, c)

	empty := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClientWithAuth(srv.URL, Auth{CAFile: empty}); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Errorf("a CA file without a certificate must be an error, got %v", err)
	}
	if _, err := NewClientWithAuth(srv.URL, Auth{CAFile: filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Error("a missing CA file must be an error")
	}
}

func TestBearerAndBasicAreAlternatives(t *testing.T) {
	if _, err := NewClientWithAuth("https://p", Auth{BearerToken: "t", Username: "u", Password: "p"}); err == nil {
		t.Fatal("both kinds of credentials must be refused")
	}
}

// An https server that redirects to http on the same host must not get the credential sent in
// plain text: net/http would keep the header, since only the host is compared.
func TestNoCredentialsOnADowngradeRedirect(t *testing.T) {
	var leaked []string
	var mu sync.Mutex
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		leaked = append(leaked, r.Header.Get("Authorization"))
		mu.Unlock()
		fmt.Fprint(w, `{"status":"success","data":["up"]}`)
	}))
	defer plain.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := NewClientWithAuth(srv.URL, Auth{BearerToken: "t0k3n", CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.MetricNames(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing a redirect from https") {
		t.Errorf("want the downgrade refused, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) != 0 {
		t.Errorf("the http server was reached, with Authorization %q", leaked)
	}
}
