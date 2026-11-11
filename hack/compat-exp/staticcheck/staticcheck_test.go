package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func runFile(t *testing.T, file, world string, native bool) report {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rep, err := run(f, options{World: world, NativeSidecars: native}, "pilot")
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func result(t *testing.T, rep report, id string) checkResult {
	t.Helper()
	for _, c := range rep.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s in report", id)
	return checkResult{}
}

func TestChecks(t *testing.T) {
	native := true
	classic := false
	tests := []struct {
		file   string
		world  string
		native bool
		check  string
		// want is the sorted list of flags as "Subject: reason"; nil means
		// not flagged.
		want []string
	}{
		// ambient-np
		{"np-redis-default.yaml", "W3", native, "np-hbone-probe", []string{"StatefulSet/redis-replicas: no ingress rule admits TCP 15008 (HBONE)"}},
		{"np-redis-default.yaml", "W1", native, "np-hbone-probe", nil},
		{"np-redis-fixed.yaml", "W3", native, "np-hbone-probe", nil},
		{"np-rule-no-ports.yaml", "W3", native, "np-hbone-probe", []string{"StatefulSet/redis-replicas: kubelet probe port 6379 is not admitted from 169.254.7.127/32"}},
		{"np-deny-all.yaml", "W3", native, "np-hbone-probe", []string{
			"StatefulSet/redis-replicas: kubelet probe port 6379 is not admitted from 169.254.7.127/32",
			"StatefulSet/redis-replicas: no ingress rule admits TCP 15008 (HBONE)",
		}},
		{"np-no-policytypes.yaml", "W3", native, "np-hbone-probe", []string{
			"StatefulSet/redis-replicas: kubelet probe port 6379 is not admitted from 169.254.7.127/32",
			"StatefulSet/redis-replicas: no ingress rule admits TCP 15008 (HBONE)",
		}},
		{"np-egress-only.yaml", "W3", native, "np-hbone-probe", nil},
		{"np-port-range.yaml", "W3", native, "np-hbone-probe", nil},
		{"np-udp-15008.yaml", "W3", native, "np-hbone-probe", []string{"StatefulSet/redis-replicas: no ingress rule admits TCP 15008 (HBONE)"}},
		{"np-two-policies.yaml", "W3", native, "np-hbone-probe", nil},
		{"np-selector-miss.yaml", "W3", native, "np-hbone-probe", nil},
		{"np-optout.yaml", "W3", native, "np-hbone-probe", nil},
		{"np-probe-blocked.yaml", "W3", native, "np-hbone-probe", []string{"Deployment/web: kubelet probe port 8080 is not admitted from 169.254.7.127/32"}},
		{"np-probe-ipblock.yaml", "W3", native, "np-hbone-probe", nil},
		{"np-probe-except.yaml", "W3", native, "np-hbone-probe", []string{"Deployment/web: kubelet probe port 8080 is not admitted from 169.254.7.127/32"}},

		// protocol
		{"protocol-serverfirst-http.yaml", "W1", native, "protocol-declared", []string{"Service/db: port 3306 is a documented server-first port and is declared http"}},
		{"protocol-serverfirst-http.yaml", "W3", native, "protocol-declared", nil},
		{"protocol-serverfirst-plain.yaml", "W1", native, "protocol-declared", nil},
		{"protocol-conflict.yaml", "W1", native, "protocol-declared", []string{"Deployment/server: container port 8080 is declared with different protocols by its Services"}},
		{"protocol-consistent.yaml", "W1", native, "protocol-declared", nil},

		// uid-1337
		{"uid-pod.yaml", "W1", native, "uid-1337", []string{"Deployment/app: container app runs as UID 1337"}},
		{"uid-pod.yaml", "W3", native, "uid-1337", nil},
		{"uid-container.yaml", "W1", native, "uid-1337", []string{"Job/migrate: container wait runs as UID 1337"}},
		{"uid-override.yaml", "W1", native, "uid-1337", nil},
		{"uid-optout.yaml", "W1", native, "uid-1337", nil},

		// reserved-port
		{"reserved-container.yaml", "W1", native, "reserved-port", []string{"DaemonSet/exporter: port 15020 is used by Istio inside the pod"}},
		{"reserved-container.yaml", "W3", native, "reserved-port", nil},
		{"reserved-ztunnel.yaml", "W3", native, "reserved-port", []string{"Deployment/app: port 15006 is used by Istio inside the pod"}},
		{"reserved-ztunnel.yaml", "W1", native, "reserved-port", []string{"Deployment/app: port 15006 is used by Istio inside the pod"}},
		{"reserved-hostnetwork.yaml", "W1", native, "reserved-port", nil},

		// startup-order
		{"startup-classic-init.yaml", "W1", native, "classic-sidecar-startup", []string{"Deployment/operator: init containers start before a classic sidecar"}},
		{"startup-classic-hold.yaml", "W1", native, "classic-sidecar-startup", nil},
		{"startup-native.yaml", "W1", native, "classic-sidecar-startup", nil},
		{"startup-native.yaml", "W1", classic, "classic-sidecar-startup", []string{"Deployment/operator: init containers start before a classic sidecar; holdApplicationUntilProxyStarts not set"}},
		{"startup-native.yaml", "W3", classic, "classic-sidecar-startup", nil},

		// job-lifecycle
		{"job-classic.yaml", "W1", native, "classic-sidecar-job", []string{"Job/migrate: pod meant to end gets a classic sidecar that keeps running"}},
		{"job-kinds.yaml", "W1", native, "classic-sidecar-job", nil},
		{"job-kinds.yaml", "W1", classic, "classic-sidecar-job", []string{
			"CronJob/backup: pod meant to end gets a classic sidecar that keeps running",
			"Job/migrate: pod meant to end gets a classic sidecar that keeps running",
			"Pod/test-connection: pod meant to end gets a classic sidecar that keeps running",
		}},
		{"job-optout.yaml", "W1", native, "classic-sidecar-job", nil},
		{"job-classic.yaml", "W3", native, "classic-sidecar-job", nil},

		// apiserver-caller
		{"webhook-in-mesh.yaml", "W1", native, "webhook-backend-in-mesh", []string{"ValidatingWebhookConfiguration/op-validating: the API server calls op-webhook:443, whose pod Deployment/op is in the mesh"}},
		{"webhook-in-mesh.yaml", "W3", native, "webhook-backend-in-mesh", []string{"ValidatingWebhookConfiguration/op-validating: the API server calls op-webhook:443, whose pod Deployment/op is in the mesh"}},
		{"webhook-excluded.yaml", "W1", native, "webhook-backend-in-mesh", nil},
		{"webhook-excluded.yaml", "W3", native, "webhook-backend-in-mesh", []string{"APIService/v1beta1.metrics.example.io: the API server calls op-webhook:443, whose pod Deployment/op is in the mesh"}},
		{"webhook-elsewhere.yaml", "W1", native, "webhook-backend-in-mesh", nil},

		// ambient-l7
		{"l7-authz-selector.yaml", "W3", native, "l7-without-waypoint", []string{"AuthorizationPolicy/allow-get: L7 attributes in a policy ztunnel enforces, and no waypoint"}},
		{"l7-authz-selector.yaml", "W1", native, "l7-without-waypoint", nil},
		{"l7-authz-l4only.yaml", "W3", native, "l7-without-waypoint", nil},
		{"l7-routes.yaml", "W3", native, "l7-without-waypoint", []string{
			"HTTPRoute/web-gamma: route attached to a Service (mesh), and no waypoint",
			"VirtualService/web-mesh: mesh VirtualService with http routes, and no waypoint",
		}},
		{"l7-with-waypoint.yaml", "W3", native, "l7-without-waypoint", nil},
	}
	for _, tc := range tests {
		name := strings.Join([]string{tc.check, tc.file, tc.world}, "/")
		if !tc.native {
			name += "/classic"
		}
		t.Run(name, func(t *testing.T) {
			res := result(t, runFile(t, tc.file, tc.world, tc.native), tc.check)
			var got []string
			for _, f := range res.Flags {
				got = append(got, f.Subject+": "+f.Reason)
				if len(f.Evidence) == 0 {
					t.Errorf("flag %q has no evidence", f.Subject)
				}
				for _, e := range f.Evidence {
					if e.Kind == "" || e.Name == "" || e.Path == "" {
						t.Errorf("flag %q has incomplete evidence %+v", f.Subject, e)
					}
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("flags:\n got %q\nwant %q", got, tc.want)
			}
			if res.Flagged != (len(tc.want) > 0) {
				t.Errorf("flagged = %v with %d flags", res.Flagged, len(tc.want))
			}
		})
	}
}

// Every fixture must leave the checks it is not about quiet, so a fixture
// exercises one rule and a stray flag shows up here.
func TestFixturesFlagOnlyTheirCheck(t *testing.T) {
	prefix := map[string]string{
		"np-": "np-hbone-probe", "protocol-": "protocol-declared", "uid-": "uid-1337",
		"reserved-": "reserved-port", "startup-": "classic-sidecar-startup", "job-": "classic-sidecar-job",
		"webhook-": "webhook-backend-in-mesh", "l7-": "l7-without-waypoint",
	}
	files, _ := filepath.Glob("testdata/*.yaml")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, path := range files {
		file := filepath.Base(path)
		own := ""
		for p, id := range prefix {
			if strings.HasPrefix(file, p) {
				own = id
			}
		}
		if own == "" {
			t.Errorf("fixture %s has no check prefix", file)
			continue
		}
		for _, w := range []string{"W1", "W3"} {
			for _, c := range runFile(t, file, w, true).Checks {
				// A webhook backend is a pod in the mesh like any other, so
				// the webhook fixtures are allowed nothing else either.
				if c.ID != own && c.Flagged {
					t.Errorf("%s %s: check %s flags %d times, want only %s", file, w, c.ID, len(c.Flags), own)
				}
			}
		}
	}
}

func TestOutputDeterministic(t *testing.T) {
	var all bytes.Buffer
	files, _ := filepath.Glob("testdata/*.yaml")
	sort.Strings(files)
	for _, f := range files {
		b, _ := os.ReadFile(f)
		all.Write(b)
		all.WriteString("\n---\n")
	}
	var first []byte
	for i := 0; i < 5; i++ {
		rep, err := run(bytes.NewReader(all.Bytes()), options{World: "W3", NativeSidecars: true}, "pilot")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(rep)
		if i == 0 {
			first = b
		} else if !bytes.Equal(b, first) {
			t.Fatal("output differs between runs")
		}
		for _, c := range rep.Checks {
			if !sort.SliceIsSorted(c.Flags, func(i, j int) bool {
				if c.Flags[i].Subject != c.Flags[j].Subject {
					return c.Flags[i].Subject < c.Flags[j].Subject
				}
				return c.Flags[i].Reason < c.Flags[j].Reason
			}) {
				t.Errorf("%s: flags not sorted", c.ID)
			}
		}
	}
}

func TestChecksInRegistryOrder(t *testing.T) {
	rep, err := run(strings.NewReader(""), options{World: "W1", NativeSidecars: true}, "pilot")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Checks) != len(checks) {
		t.Fatalf("%d results for %d checks", len(rep.Checks), len(checks))
	}
	for i, c := range rep.Checks {
		if c.ID != checks[i].ID || c.Flags == nil {
			t.Errorf("result %d: %+v", i, c)
		}
	}
}

func TestIstioProtocol(t *testing.T) {
	s := func(v string) *string { return &v }
	tests := []struct {
		port  int
		name  string
		app   *string
		proto string
		want  string
	}{
		{80, "http", nil, "TCP", "http"},
		{80, "http-web", nil, "TCP", "http"},
		{80, "HTTP2-x", nil, "TCP", "http2"},
		{80, "grpc-web-x", nil, "TCP", "grpc-web"},
		{80, "web", nil, "TCP", ""},
		{3306, "web", nil, "TCP", "tcp"},
		{3306, "http", nil, "TCP", "http"},
		{80, "http", s("tcp"), "TCP", "tcp"},
		{80, "tcp", s("kubernetes.io/h2c"), "TCP", "http2"},
		{80, "", s("https"), "TCP", "https"},
		{53, "dns", nil, "UDP", "udp"},
	}
	for _, tc := range tests {
		if got := istioProtocol(tc.port, tc.name, tc.proto, tc.app); got != tc.want {
			t.Errorf("istioProtocol(%d, %q, %s, %v) = %q, want %q", tc.port, tc.name, tc.proto, tc.app, got, tc.want)
		}
	}
}
