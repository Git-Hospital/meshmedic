package kube

import (
	"strings"
	"testing"
)

// Plain values of secret-like env names never reach a report; other names and valueFrom
// entries render as before (#20).
func TestRenderEnvListRedactsSecretLikeNames(t *testing.T) {
	got, ok := renderEnvList([]any{
		map[string]any{"name": "DB_PASSWORD", "value": "s3cr3t"},
		map[string]any{"name": "api_token", "value": "tok-123"},
		map[string]any{"name": "ERROR_RATE", "value": "0.4"},
		map[string]any{"name": "TLS_CERT", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "tls", "key": "crt"}}},
	})
	if !ok {
		t.Fatal("env list not rendered")
	}
	for _, leak := range []string{"s3cr3t", "tok-123"} {
		if strings.Contains(got, leak) {
			t.Errorf("rendered env leaks %q: %s", leak, got)
		}
	}
	for _, want := range []string{"DB_PASSWORD=[redacted]", "api_token=[redacted]", "ERROR_RATE=0.4", `"secretKeyRef"`} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered env missing %q: %s", want, got)
		}
	}
}

// A catalog path that ends on a container, or on one env entry, is printed as JSON, not as an env
// list, and is redacted too.
func TestRenderValueRedactsContainersAndEntries(t *testing.T) {
	entry := map[string]any{"name": "DB_PASSWORD", "value": "s3cr3t"}
	container := map[string]any{"name": "app", "image": "app:1", "env": []any{entry, map[string]any{"name": "MODE", "value": "fast"}}}
	for name, v := range map[string]any{"container": container, "entry": entry, "containers": []any{container}} {
		got := renderValue(v)
		if strings.Contains(got, "s3cr3t") || !strings.Contains(got, `"[redacted]"`) {
			t.Errorf("%s: %s", name, got)
		}
	}
	if got := renderValue(container); !strings.Contains(got, `"fast"`) || !strings.Contains(got, `"app:1"`) {
		t.Errorf("redaction changed more than the secret: %s", got)
	}
	if entry["value"] != "s3cr3t" {
		t.Error("renderValue changed the object it was given")
	}
}

func TestDiffTemplatesRedactsSecretLikeEnv(t *testing.T) {
	tmpl := func(password, mode string) map[string]any {
		return map[string]any{"spec": map[string]any{"containers": []any{map[string]any{
			"name": "app",
			"env": []any{
				map[string]any{"name": "DB_PASSWORD", "value": password},
				map[string]any{"name": "MODE", "value": mode},
			},
		}}}}
	}

	// The credential changed: the diff says so without either value.
	diff := diffTemplates(tmpl("old-pass", "a"), tmpl("new-pass", "a"))
	for _, leak := range []string{"old-pass", "new-pass"} {
		if strings.Contains(diff, leak) {
			t.Errorf("diff leaks %q:\n%s", leak, diff)
		}
	}
	if !strings.Contains(diff, `- "value": "[redacted]"`) || !strings.Contains(diff, `+ "value": "[redacted, changed]"`) {
		t.Errorf("diff does not show the redacted credential change:\n%s", diff)
	}

	// Only an ordinary value changed: it shows, and the unchanged credential stays out.
	diff = diffTemplates(tmpl("same", "a"), tmpl("same", "b"))
	if !strings.Contains(diff, `- "value": "a"`) || !strings.Contains(diff, `+ "value": "b"`) || strings.Contains(diff, "same") {
		t.Errorf("diff of an ordinary value:\n%s", diff)
	}

	// The input templates are not modified.
	in := tmpl("keep", "a")
	diffTemplates(in, in)
	env := in["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"].([]any)
	if env[0].(map[string]any)["value"] != "keep" {
		t.Error("diffTemplates changed its input")
	}
}

func TestRedactLogText(t *testing.T) {
	for in, want := range map[string]string{
		"connecting with DB_PASSWORD=hunter2 to db":      "connecting with DB_PASSWORD=[redacted] to db",
		`token="abc def" user=bob`:                       `token=[redacted] user=bob`,
		"apiKey=xyz,retry=3":                             "apiKey=[redacted],retry=3",
		"error_rate=0.4 latency=12ms":                    "error_rate=0.4 latency=12ms",
		"curl: (6) Could not resolve host: payments-svc": "curl: (6) Could not resolve host: payments-svc",
	} {
		if got := redactLogText(in); got != want {
			t.Errorf("redactLogText(%q) = %q, want %q", in, got, want)
		}
	}
}
