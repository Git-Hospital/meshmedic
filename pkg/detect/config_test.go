package detect

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Git-Hospital/meshmedic/pkg/catalog"
	"github.com/Git-Hospital/meshmedic/pkg/prom"
)

func TestUnknownScenarios(t *testing.T) {
	scenarios := []catalog.Scenario{{ID: "known"}, {ID: "also-known"}}
	for _, tt := range []struct {
		name    string
		targets []Target
		want    map[int][]string
	}{
		{"known", []Target{{Scenarios: []string{"known", "also-known"}}}, map[int][]string{}},
		{"unknown", []Target{{Scenarios: []string{"known"}}, {Scenarios: []string{"typo", "also-known", "other-typo"}}}, map[int][]string{1: {"typo", "other-typo"}}},
		{"all", []Target{{}}, map[int][]string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := UnknownScenarios(tt.targets, scenarios); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UnknownScenarios = %v, want %v", got, tt.want)
			}
		})
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "watch.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigDefaultsInterval(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
prometheus: http://localhost:9090
targets:
  - params:
      service: payments
      namespace: demo
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IntervalDuration() != 30*time.Second {
		t.Fatalf("got %v, want default 30s", cfg.IntervalDuration())
	}
}

func TestLoadConfigRejectsMissingTargets(t *testing.T) {
	if _, err := LoadConfig(writeConfig(t, `prometheus: http://localhost:9090`)); err == nil {
		t.Fatal("want error for config without targets")
	}
}

func TestLoadConfigGitOpsDefaultsAndValidation(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
prometheus: http://localhost:9090
gitops:
  repo: kassvl/config
targets:
  - params:
      service: payments
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitOps.Path != "meshmedic/{{.namespace}}/{{.scenario}}.yaml" {
		t.Fatalf("got default path %q", cfg.GitOps.Path)
	}

	_, err = LoadConfig(writeConfig(t, `
prometheus: http://localhost:9090
gitops:
  repo: nomissingowner
targets:
  - params:
      service: payments
`))
	if err == nil {
		t.Fatal("want error for gitops.repo without owner/")
	}
}

func TestLoadConfigRejectsBadInterval(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, `
prometheus: http://localhost:9090
interval: soon
targets:
  - params:
      service: payments
`))
	if err == nil {
		t.Fatal("want error for unparseable interval")
	}
}

func TestConfigFromFlagsBuildsAWatchableConfig(t *testing.T) {
	cfg, err := ConfigFromFlags("http://prom:9090", "10s", []string{
		"service=payments,namespace=demo",
		"service=ledger, namespace=demo",
	})
	if err != nil {
		t.Fatalf("ConfigFromFlags: %v", err)
	}
	if cfg.Prometheus != "http://prom:9090" {
		t.Errorf("prometheus = %q", cfg.Prometheus)
	}
	if cfg.IntervalDuration() != 10*time.Second {
		t.Errorf("interval = %v, want 10s", cfg.IntervalDuration())
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(cfg.Targets))
	}
	// Whitespace around a pair is an ordinary thing to type and must not
	// become part of the key or the value.
	if got := cfg.Targets[1].Params["service"]; got != "ledger" {
		t.Errorf("second target service = %q, want ledger", got)
	}
	if got := cfg.Targets[1].Params["namespace"]; got != "demo" {
		t.Errorf("second target namespace = %q, want demo", got)
	}
}

func TestConfigFromFlagsRequiresPrometheusAndTarget(t *testing.T) {
	if _, err := ConfigFromFlags("", "", []string{"service=payments"}); err == nil {
		t.Error("expected an error with no prometheus URL")
	}
	if _, err := ConfigFromFlags("http://prom:9090", "", nil); err == nil {
		t.Error("expected an error with no targets")
	}
	if _, err := ConfigFromFlags("http://prom:9090", "nope", []string{"service=payments"}); err == nil {
		t.Error("expected an error on an unparseable interval")
	}
}

func TestParseTargetSpecRejectsMalformedInput(t *testing.T) {
	for _, spec := range []string{"bogus", "=payments", "", ",,", "service=a,service=b"} {
		if _, err := ParseTargetSpec(spec); err == nil {
			t.Errorf("ParseTargetSpec(%q) = nil error, want a rejection", spec)
		}
	}
	// An empty value is legitimate: a param can be deliberately blank.
	params, err := ParseTargetSpec("service=payments,subset=")
	if err != nil {
		t.Fatalf("ParseTargetSpec: %v", err)
	}
	if params["subset"] != "" || len(params) != 2 {
		t.Errorf("params = %v", params)
	}
}

func TestLoadConfigRejectsUnknownKeys(t *testing.T) {
	// The message must name the offending key and line so a typo is easy to
	// find; a silent ignore would leave the feature switched off.
	cases := map[string]struct{ content, key string }{
		"stateFile typo": {
			content: `prometheus: http://localhost:9090
statefile: /var/lib/meshmedic/state.json
targets:
  - params:
      service: payments
`,
			key: "statefile",
		},
		"scenarios typo on a target": {
			content: `prometheus: http://localhost:9090
targets:
  - params:
      service: payments
    scenario: canary-latency-rollback
`,
			key: "scenario",
		},
		"baselineState typo": {
			content: `prometheus: http://localhost:9090
baselinestate: /tmp/baseline.json
targets:
  - params:
      service: payments
`,
			key: "baselinestate",
		},
		"gitops key typo": {
			content: `prometheus: http://localhost:9090
gitops:
  repo: kassvl/config
  branch: main
targets:
  - params:
      service: payments
`,
			key: "branch",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, tc.content))
			if err == nil {
				t.Fatalf("want an error for unknown key %q", tc.key)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error %q does not name the offending key %q", err, tc.key)
			}
			if !strings.Contains(err.Error(), "line") {
				t.Fatalf("error %q does not include the line", err)
			}
		})
	}
}

func TestLoadConfigAcceptsRepositoryExamples(t *testing.T) {
	for _, path := range []string{
		"../../examples/watch.yaml",
		"../../demo/watch.yaml",
		"../../demo/f9-recorder/watch.yaml",
	} {
		if _, err := LoadConfig(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestLoadConfigPrometheusAuth(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
prometheus: https://prom.example
prometheusAuth:
  bearerTokenEnv: PROM_TOKEN
  caFile: /etc/meshmedic/ca.pem
targets:
  - params:
      service: payments
`))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"PROM_TOKEN": "t0k3n"}
	a, err := cfg.PrometheusAuth.Resolve(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if a.BearerToken != "t0k3n" || a.CAFile != "/etc/meshmedic/ca.pem" || a.Username != "" {
		t.Fatalf("resolved %+v", a)
	}

	var none *PrometheusAuth
	if a, err := none.Resolve(func(string) string { return "x" }); err != nil || a != (prom.Auth{}) {
		t.Fatalf("no auth section resolved to %+v, %v", a, err)
	}
}

// A credential pasted where a variable name belongs must not be repeated in an error, which
// ends up in logs.
func TestPrometheusAuthErrorsDoNotRepeatValues(t *testing.T) {
	const pasted = "eyJhbGciOiJIUzI1NiJ9.c2VjcmV0.c2ln"
	for name, tc := range map[string]struct{ auth, want string }{
		"token as the name":    {"bearerTokenEnv: " + pasted, "must be the name of an environment variable"},
		"password as the name": {"usernameEnv: U\n  passwordEnv: \"hunter2!\"", "passwordEnv must be the name"},
		"bearer and basic":     {"bearerTokenEnv: T\n  usernameEnv: U\n  passwordEnv: P", "alternatives"},
		"half of basic":        {"usernameEnv: U", "needs both"},
		"empty":                {"caFile: \"\"", "is empty"},
	} {
		_, err := LoadConfig(writeConfig(t, "prometheus: https://p\nprometheusAuth:\n  "+tc.auth+"\ntargets:\n  - params:\n      service: payments\n"))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), pasted) || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: the error repeats the value: %v", name, err)
		}
	}

	// A variable that is named but not set fails before any request, without its name: a token
	// can look like a variable name.
	a := &PrometheusAuth{BearerTokenEnv: "PASTED_CREDENTIAL_NOT_A_NAME"}
	_, err := a.Resolve(func(string) string { return "" })
	if err == nil || strings.Contains(err.Error(), "PASTED_CREDENTIAL_NOT_A_NAME") || !strings.Contains(err.Error(), "bearerTokenEnv") {
		t.Errorf("unset variable: %v", err)
	}
}
