package chartcheck

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func analyzeFile(t *testing.T, file string, mode Mode, native bool) *Report {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := Parse(f, "demo")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Analyze(m, Options{Modes: []Mode{mode}, NativeSidecars: native})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func findingsOf(rep *Report, check string) []Finding {
	var out []Finding
	for _, f := range rep.Findings {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

func TestChecks(t *testing.T) {
	native := true
	classic := false
	tests := []struct {
		file   string
		mode   Mode
		native bool
		check  string
		// want is the sorted list of findings as "Kind/Name: reason"; nil
		// means no finding (including a check that does not apply).
		want []string
	}{
		// ambient: NetworkPolicy
		{"np-redis-default.yaml", Ambient, native, "np-hbone-probe", []string{"StatefulSet/redis-replicas: no ingress rule admits TCP 15008 (HBONE)"}},
		{"np-redis-default.yaml", Sidecar, native, "np-hbone-probe", nil},
		{"np-redis-fixed.yaml", Ambient, native, "np-hbone-probe", nil},
		{"np-rule-no-ports.yaml", Ambient, native, "np-hbone-probe", []string{"StatefulSet/redis-replicas: kubelet probe port 6379 is not admitted from 169.254.7.127/32"}},
		{"np-deny-all.yaml", Ambient, native, "np-hbone-probe", []string{
			"StatefulSet/redis-replicas: kubelet probe port 6379 is not admitted from 169.254.7.127/32",
			"StatefulSet/redis-replicas: no ingress rule admits TCP 15008 (HBONE)",
		}},
		{"np-no-policytypes.yaml", Ambient, native, "np-hbone-probe", []string{
			"StatefulSet/redis-replicas: kubelet probe port 6379 is not admitted from 169.254.7.127/32",
			"StatefulSet/redis-replicas: no ingress rule admits TCP 15008 (HBONE)",
		}},
		{"np-egress-only.yaml", Ambient, native, "np-hbone-probe", nil},
		{"np-port-range.yaml", Ambient, native, "np-hbone-probe", nil},
		{"np-udp-15008.yaml", Ambient, native, "np-hbone-probe", []string{"StatefulSet/redis-replicas: no ingress rule admits TCP 15008 (HBONE)"}},
		{"np-two-policies.yaml", Ambient, native, "np-hbone-probe", nil},
		{"np-selector-miss.yaml", Ambient, native, "np-hbone-probe", nil},
		{"np-optout.yaml", Ambient, native, "np-hbone-probe", nil},
		{"np-probe-blocked.yaml", Ambient, native, "np-hbone-probe", []string{"Deployment/web: kubelet probe port 8080 is not admitted from 169.254.7.127/32"}},
		{"np-probe-ipblock.yaml", Ambient, native, "np-hbone-probe", nil},
		{"np-probe-except.yaml", Ambient, native, "np-hbone-probe", []string{"Deployment/web: kubelet probe port 8080 is not admitted from 169.254.7.127/32"}},

		// protocol
		{"protocol-serverfirst-http.yaml", Sidecar, native, "protocol-declared", []string{"Service/db: port 3306 is a documented server-first port and is declared http"}},
		{"protocol-serverfirst-http.yaml", Ambient, native, "protocol-declared", nil},
		{"protocol-serverfirst-plain.yaml", Sidecar, native, "protocol-declared", nil},
		{"protocol-conflict.yaml", Sidecar, native, "protocol-declared", []string{"Deployment/server: container port 8080 is declared with different protocols by its Services"}},
		{"protocol-consistent.yaml", Sidecar, native, "protocol-declared", nil},

		// uid-1337
		{"uid-pod.yaml", Sidecar, native, "uid-1337", []string{"Deployment/app: container app runs as UID 1337"}},
		{"uid-pod.yaml", Ambient, native, "uid-1337", nil},
		{"uid-container.yaml", Sidecar, native, "uid-1337", []string{"Job/migrate: container wait runs as UID 1337"}},
		{"uid-override.yaml", Sidecar, native, "uid-1337", nil},
		{"uid-optout.yaml", Sidecar, native, "uid-1337", nil},

		// reserved-port
		{"reserved-container.yaml", Sidecar, native, "reserved-port", []string{"DaemonSet/exporter: port 15020 is used by Istio inside the pod"}},
		{"reserved-container.yaml", Ambient, native, "reserved-port", nil},
		{"reserved-ztunnel.yaml", Ambient, native, "reserved-port", []string{"Deployment/app: port 15006 is used by Istio inside the pod"}},
		{"reserved-ztunnel.yaml", Sidecar, native, "reserved-port", []string{"Deployment/app: port 15006 is used by Istio inside the pod"}},
		{"reserved-hostnetwork.yaml", Sidecar, native, "reserved-port", nil},

		// startup-order
		{"startup-classic-init.yaml", Sidecar, native, "classic-sidecar-startup", []string{"Deployment/operator: init containers start before a classic sidecar"}},
		{"startup-classic-hold.yaml", Sidecar, native, "classic-sidecar-startup", nil},
		{"startup-native.yaml", Sidecar, native, "classic-sidecar-startup", nil},
		{"startup-native.yaml", Sidecar, classic, "classic-sidecar-startup", []string{"Deployment/operator: init containers start before a classic sidecar; holdApplicationUntilProxyStarts not set"}},
		{"startup-native.yaml", Ambient, classic, "classic-sidecar-startup", nil},

		// job-lifecycle
		{"job-classic.yaml", Sidecar, native, "classic-sidecar-job", []string{"Job/migrate: pod meant to end gets a classic sidecar that keeps running"}},
		{"job-kinds.yaml", Sidecar, native, "classic-sidecar-job", nil},
		{"job-kinds.yaml", Sidecar, classic, "classic-sidecar-job", []string{
			"CronJob/backup: pod meant to end gets a classic sidecar that keeps running",
			"Job/migrate: pod meant to end gets a classic sidecar that keeps running",
			"Pod/test-connection: pod meant to end gets a classic sidecar that keeps running",
		}},
		{"job-optout.yaml", Sidecar, native, "classic-sidecar-job", nil},
		{"job-classic.yaml", Ambient, native, "classic-sidecar-job", nil},

		// apiserver-caller
		{"webhook-in-mesh.yaml", Sidecar, native, "webhook-backend-in-mesh", []string{"ValidatingWebhookConfiguration/op-validating: the API server calls op-webhook:443, whose pod Deployment/op is in the mesh"}},
		{"webhook-in-mesh.yaml", Ambient, native, "webhook-backend-in-mesh", []string{"ValidatingWebhookConfiguration/op-validating: the API server calls op-webhook:443, whose pod Deployment/op is in the mesh"}},
		{"webhook-excluded.yaml", Sidecar, native, "webhook-backend-in-mesh", nil},
		{"webhook-excluded.yaml", Ambient, native, "webhook-backend-in-mesh", []string{"APIService/v1beta1.metrics.example.io: the API server calls op-webhook:443, whose pod Deployment/op is in the mesh"}},
		{"webhook-elsewhere.yaml", Sidecar, native, "webhook-backend-in-mesh", nil},

		// ambient-l7
		{"l7-authz-selector.yaml", Ambient, native, "l7-without-waypoint", []string{"AuthorizationPolicy/allow-get: L7 attributes in a policy ztunnel enforces, and no waypoint"}},
		{"l7-authz-selector.yaml", Sidecar, native, "l7-without-waypoint", nil},
		{"l7-authz-l4only.yaml", Ambient, native, "l7-without-waypoint", nil},
		{"l7-routes.yaml", Ambient, native, "l7-without-waypoint", []string{
			"HTTPRoute/web-gamma: route attached to a Service (mesh), and no waypoint",
			"VirtualService/web-mesh: mesh VirtualService with http routes, and no waypoint",
		}},
		{"l7-with-waypoint.yaml", Ambient, native, "l7-without-waypoint", nil},
	}
	for _, tc := range tests {
		name := strings.Join([]string{tc.check, tc.file, string(tc.mode)}, "/")
		if !tc.native {
			name += "/classic"
		}
		t.Run(name, func(t *testing.T) {
			rep := analyzeFile(t, tc.file, tc.mode, tc.native)
			var got []string
			for _, f := range findingsOf(rep, tc.check) {
				got = append(got, f.Resource.Kind+"/"+f.Resource.Name+": "+f.Reason)
				if f.Mode != tc.mode {
					t.Errorf("finding is about mode %q, run for %q", f.Mode, tc.mode)
				}
				if f.Trigger.Kind == "" || f.Trigger.Name == "" || f.Trigger.Path == "" {
					t.Errorf("finding %q has an incomplete trigger %+v", f.Reason, f.Trigger)
				}
				if f.Resource.Kind == "" || f.Resource.Name == "" || f.Resource.Namespace == "" {
					t.Errorf("finding %q has an incomplete resource %+v", f.Reason, f.Resource)
				}
				if len(f.Evidence) == 0 {
					t.Errorf("finding %q has no evidence", f.Reason)
				}
				for _, e := range f.Evidence {
					if e.Kind == "" || e.Name == "" || e.Path == "" {
						t.Errorf("finding %q has incomplete evidence %+v", f.Reason, e)
					}
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("findings:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// Every fixture must leave the checks it is not about quiet, so a fixture
// exercises one rule and a stray finding shows up here.
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
		for _, mode := range []Mode{Sidecar, Ambient} {
			for _, f := range analyzeFile(t, file, mode, true).Findings {
				if f.Check != own {
					t.Errorf("%s %s: check %s fires (%s), want only %s", file, mode, f.Check, f.Reason, own)
				}
			}
		}
	}
}

func allFixtures(t *testing.T) []byte {
	t.Helper()
	var all bytes.Buffer
	files, _ := filepath.Glob("testdata/*.yaml")
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
		all.WriteString("\n---\n")
	}
	return all.Bytes()
}

func TestOutputDeterministic(t *testing.T) {
	data := allFixtures(t)
	var firstJSON, firstText []byte
	for i := 0; i < 5; i++ {
		m, err := Parse(bytes.NewReader(data), "demo")
		if err != nil {
			t.Fatal(err)
		}
		rep, err := Analyze(m, Options{NativeSidecars: false})
		if err != nil {
			t.Fatal(err)
		}
		rep.Input = "fixtures"
		var j, x bytes.Buffer
		if err := WriteJSON(&j, rep); err != nil {
			t.Fatal(err)
		}
		if err := WriteText(&x, rep); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstJSON, firstText = j.Bytes(), x.Bytes()
			if rep.Candidates == 0 {
				t.Fatal("the fixtures should produce candidates")
			}
			if !sort.SliceIsSorted(rep.Findings, func(a, b int) bool { return lessFinding(rep.Findings[a], rep.Findings[b]) }) {
				t.Error("findings are not sorted")
			}
			continue
		}
		if !bytes.Equal(j.Bytes(), firstJSON) || !bytes.Equal(x.Bytes(), firstText) {
			t.Fatal("output differs between runs")
		}
	}
}

func TestRulesMetadata(t *testing.T) {
	idRE := regexp.MustCompile(`^MM-CHART-\d{3}$`)
	ids, names := map[string]bool{}, map[string]bool{}
	var prev string
	for _, r := range Rules() {
		if !idRE.MatchString(r.ID) {
			t.Errorf("%s: not an MM-CHART-NNN id", r.ID)
		}
		if r.ID <= prev {
			t.Errorf("%s: rules are not in id order", r.ID)
		}
		prev = r.ID
		if ids[r.ID] || names[r.Check] {
			t.Errorf("%s/%s: duplicate", r.ID, r.Check)
		}
		ids[r.ID], names[r.Check] = true, true
		if r.Summary == "" || r.Effect == "" || r.Fix == "" || r.Confirm == "" || r.run == nil || len(r.Modes) == 0 {
			t.Errorf("%s: incomplete rule", r.ID)
		}
		if strings.Count(r.Effect, ". ") > 0 || !strings.HasSuffix(r.Effect, ".") {
			t.Errorf("%s: effect is not one sentence: %q", r.ID, r.Effect)
		}
		// A rule cites documentation or says plainly that it has none.
		if (len(r.Docs) == 0) == (r.DocsNote == "") {
			t.Errorf("%s: needs Docs or a DocsNote, and not both", r.ID)
		}
		for _, d := range r.Docs {
			if !strings.HasPrefix(d.URL, "https://istio.io/") || d.Quote == "" || d.Heading == "" {
				t.Errorf("%s: bad doc %+v", r.ID, d)
			}
		}
		for _, m := range r.Modes {
			if m != Sidecar && m != Ambient {
				t.Errorf("%s: unknown mode %q", r.ID, m)
			}
		}
	}
	if len(ids) != 8 {
		t.Errorf("%d rules, want 8", len(ids))
	}
}

// An ambient-only check does not run for sidecar, and the report says which
// checks ran, so a quiet report cannot be mistaken for a check that looked.
func TestModeSelectsChecks(t *testing.T) {
	data, err := os.ReadFile("testdata/np-redis-default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(bytes.NewReader(data), "demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		modes []Mode
		runs  int
		hits  int
	}{
		{[]Mode{Sidecar}, 6, 0},
		{[]Mode{Ambient}, 4, 1},
		{nil, 10, 1},
	} {
		rep, err := Analyze(m, Options{Modes: tc.modes, NativeSidecars: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Checks) != tc.runs || rep.Candidates != tc.hits {
			t.Errorf("modes %v: %d checks and %d candidates, want %d and %d", tc.modes, len(rep.Checks), rep.Candidates, tc.runs, tc.hits)
		}
	}
	if _, err := Analyze(m, Options{Modes: []Mode{"waypoint"}}); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

// The regression pair from the cluster runs: Bitnami redis 28.3.9 with its
// default NetworkPolicy breaks in ambient because nothing admits port 15008,
// and the one-rule fix (networkPolicy.extraIngress) clears it.
func TestRedisDefaultVersusFixed(t *testing.T) {
	def := analyzeFile(t, "np-redis-default.yaml", Ambient, true)
	if def.Candidates != 1 || def.Findings[0].ID != "MM-CHART-001" || def.Findings[0].Resource.Name != "redis-replicas" {
		t.Fatalf("default: %+v", def.Findings)
	}
	if got := def.Findings[0].Trigger; got.Kind != "NetworkPolicy" || got.Path != "spec.ingress" || !strings.Contains(got.Value, "6379") {
		t.Errorf("trigger: %+v", got)
	}
	fixed := analyzeFile(t, "np-redis-fixed.yaml", Ambient, true)
	if fixed.Candidates != 0 {
		t.Fatalf("fixed: %+v", fixed.Findings)
	}
	if len(fixed.Checks) == 0 || fixed.Examined.PodTemplates == 0 {
		t.Error("a clean report must still show what was looked at")
	}
}

func TestEmptyInputIsNotClean(t *testing.T) {
	for _, in := range []string{"", "---\n---\n", "# nothing but a comment\n"} {
		m, err := Parse(strings.NewReader(in), "demo")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Analyze(m, Options{}); err == nil {
			t.Errorf("empty input %q was reported as checked", in)
		}
	}
}

func TestBadYAML(t *testing.T) {
	if _, err := Parse(strings.NewReader("kind: [unclosed\n"), "demo"); err == nil {
		t.Error("invalid YAML was accepted")
	}
}

// The JSON report keeps its shape: the golden files are the documented schema
// at version 1, and a change to them is a decision, not an accident.
func TestGolden(t *testing.T) {
	rep := analyzeFile(t, "np-redis-default.yaml", Ambient, true)
	rep.Input = "manifests:np-redis-default.yaml"
	var j, x bytes.Buffer
	if err := WriteJSON(&j, rep); err != nil {
		t.Fatal(err)
	}
	if err := WriteText(&x, rep); err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]byte{"golden-redis-default.json": j.Bytes(), "golden-redis-default.txt": x.Bytes()} {
		path := filepath.Join("testdata", "golden", name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from the golden file; rerun with -update if the change is intended", name)
		}
	}
	var v map[string]any
	if err := json.Unmarshal(j.Bytes(), &v); err != nil || v["schemaVersion"] != float64(1) {
		t.Errorf("schemaVersion: %v %v", v["schemaVersion"], err)
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
