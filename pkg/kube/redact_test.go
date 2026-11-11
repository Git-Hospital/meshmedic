package kube

import (
	"encoding/json"
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

// Two containers with the same env name and different values: each value is compared with its own
// container's, by name, so a rollout that leaves both alone shows no credential change and a real
// change in one shows (review of #29). Map order varies between runs; run it with -count to see.
func TestDiffTemplatesKeysEnvByContainer(t *testing.T) {
	c := func(name, password, image string) map[string]any {
		return map[string]any{"name": name, "image": image, "env": []any{map[string]any{"name": "DB_PASSWORD", "value": password}}}
	}
	tmpl := func(initC, app map[string]any) map[string]any {
		return map[string]any{"spec": map[string]any{"initContainers": []any{initC}, "containers": []any{app}}}
	}
	old := tmpl(c("migrate", "admin-pw", "m:1"), c("app", "app-pw", "a:1"))

	diff := diffTemplates(old, tmpl(c("migrate", "admin-pw", "m:1"), c("app", "app-pw", "a:2")))
	if strings.Contains(diff, "changed") || !strings.Contains(diff, `+ "image": "a:2"`) {
		t.Errorf("image-only rollout:\n%s", diff)
	}

	diff = diffTemplates(old, tmpl(c("migrate", "admin-pw", "m:1"), c("app", "admin-pw", "a:1")))
	if !strings.Contains(diff, `+ "value": "[redacted, changed]"`) || strings.Contains(diff, "-pw") {
		t.Errorf("the app's password changed to the initContainer's:\n%s", diff)
	}

	// The same containers in another order: no credential changed (the order did, and is said so).
	two := func(a, b map[string]any) map[string]any {
		return map[string]any{"spec": map[string]any{"containers": []any{a, b}}}
	}
	diff = diffTemplates(two(c("x", "x-pw", "i"), c("y", "y-pw", "i")), two(c("y", "y-pw", "i"), c("x", "x-pw", "i")))
	if strings.Contains(diff, "[redacted, changed]") || strings.Contains(diff, "-pw") {
		t.Errorf("reordered containers:\n%s", diff)
	}
}

// A secret-like name=value in a string, such as an argument, is redacted in the diff, and a change
// to its value is marked without showing either value.
func TestDiffTemplatesRedactsArgs(t *testing.T) {
	tmpl := func(password, level string) map[string]any {
		return map[string]any{"spec": map[string]any{"containers": []any{map[string]any{
			"name": "app", "args": []any{"--db-password=" + password, "--log-level=" + level},
		}}}}
	}
	diff := diffTemplates(tmpl("hunter2", "info"), tmpl("hunter3", "info"))
	if strings.Contains(diff, "hunter") || !strings.Contains(diff, `+ "--db-password=[redacted, changed]"`) {
		t.Errorf("a changed argument:\n%s", diff)
	}
	diff = diffTemplates(tmpl("hunter2", "info"), tmpl("hunter2", "debug"))
	if strings.Contains(diff, "hunter") || strings.Contains(diff, "changed") || !strings.Contains(diff, `+ "--log-level=debug"`) {
		t.Errorf("an unchanged argument:\n%s", diff)
	}
}

// A catalog path that ends on the value itself is redacted like one that ends on the list.
func TestExtractFieldRedactsValuePaths(t *testing.T) {
	obj := map[string]any{"spec": map[string]any{"containers": []any{map[string]any{
		"name": "app",
		"args": []any{"--api-token=tok-123"},
		"env":  []any{map[string]any{"name": "DB_PASSWORD", "value": "s3cr3t"}, map[string]any{"name": "MODE", "value": "fast"}},
	}}}}
	for path, want := range map[string]string{
		"spec.containers[*].env[*].value": "[redacted]; fast",
		"spec.containers[0].env[0].value": "[redacted]",
		"spec.containers[0].args[0]":      "--api-token=[redacted]",
	} {
		got, err := ExtractField(obj, path)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", path, got, err, want)
		}
	}
	if obj["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["args"].([]any)[0] != "--api-token=tok-123" {
		t.Error("ExtractField changed the object it was given")
	}
}

// Redaction can make two different templates print alike; the diff still says something changed.
func TestDiffTemplatesNeverHidesAChange(t *testing.T) {
	tmpl := func(args ...any) map[string]any {
		return map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "args": args}}}}
	}
	diff := diffTemplates(tmpl("--password=a", "--password=b"), tmpl("--password=a", "--password=a"))
	if strings.Contains(diff, "no template change") || strings.Contains(diff, "=a") || strings.Contains(diff, "=b") {
		t.Errorf("a change between equal-looking secrets: %s", diff)
	}
	if diff := diffTemplates(tmpl("--password=a"), tmpl("--password=a")); diff != "(no template change: restart or scale only)" {
		t.Errorf("no change: %s", diff)
	}
}

const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"

// deploymentWithAnnotation is a Deployment as kubectl apply leaves it: the applied spec, with its
// env and args, sits in one annotation as a JSON string.
func deploymentWithAnnotation(value string) map[string]any {
	return map[string]any{"metadata": map[string]any{
		"name":        "app",
		"annotations": map[string]any{lastApplied: value, "team": "payments"},
	}}
}

const appliedJSON = `{"apiVersion":"apps/v1","kind":"Deployment","spec":{"template":{"spec":{"containers":[{"name":"app","image":"app:1",` +
	`"args":["--api-token=tok-fake-123","--mode=fast"],` +
	`"env":[{"name":"DB_PASSWORD","value":"fake-pw-xyz"},{"name":"MODE","value":"fast"}]}]}}}}`

// The applied spec inside the annotation is parsed and redacted like an object (#33).
func TestExtractFieldRedactsLastAppliedAnnotation(t *testing.T) {
	obj := deploymentWithAnnotation(appliedJSON)
	for _, path := range []string{"metadata", "metadata.annotations"} {
		got, err := ExtractField(obj, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, leak := range []string{"fake-pw-xyz", "tok-fake-123"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s leaks %q: %s", path, leak, got)
			}
		}
		for _, want := range []string{"payments", `app:1`, `--mode=fast`, `[redacted]`} {
			if !strings.Contains(got, want) {
				t.Errorf("%s lost %q: %s", path, want, got)
			}
		}
	}
	// The key contains dots, so a catalog path cannot name it; it must not match, let alone leak.
	if got, err := ExtractField(obj, "metadata.annotations."+lastApplied); err == nil {
		t.Errorf("dotted key path matched: %s", got)
	}
}

// The exact annotation key, reached as a value rather than by a dotted path.
func TestRedactAllRedactsLastAppliedByKey(t *testing.T) {
	annotations := map[string]any{lastApplied: appliedJSON}
	got := renderValue(annotations)
	if strings.Contains(got, "fake-pw-xyz") || strings.Contains(got, "tok-fake-123") {
		t.Errorf("annotation value leaks: %s", got)
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatal(err)
	}
	var applied map[string]any
	if err := json.Unmarshal([]byte(out[lastApplied]), &applied); err != nil {
		t.Fatalf("annotation is no longer JSON: %v", err)
	}
	if annotations[lastApplied] != appliedJSON {
		t.Error("redaction changed the object it was given")
	}
}

// A value that is not JSON is replaced whole, since nothing can be said about what it holds.
func TestRedactLastAppliedFailsClosed(t *testing.T) {
	for _, bad := range []string{`not json DB_PASSWORD fake-pw-xyz`, `{"env":[{"name":"DB_PASSWORD","value":"fake-pw-xyz"}`, ``} {
		got, err := ExtractField(deploymentWithAnnotation(bad), "metadata.annotations")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "fake-pw-xyz") || strings.Contains(got, "not json") {
			t.Errorf("non-JSON annotation passes through: %s", got)
		}
		if !strings.Contains(got, `"`+lastApplied+`":"[redacted]"`) {
			t.Errorf("non-JSON annotation not replaced by the marker: %s", got)
		}
	}
}

// Secret-like name=value pairs nested in the annotation JSON, in args and in other strings.
func TestRedactLastAppliedNested(t *testing.T) {
	applied := `{"spec":{"template":{"spec":{"initContainers":[{"name":"init","command":["sh","-c","export DB_PASSWORD=fake-pw-xyz; run"]}],` +
		`"containers":[{"name":"app","args":["--db-password=fake-pw-xyz","API_KEY=fake-key-abc"]}]}}}}`
	got, err := ExtractField(deploymentWithAnnotation(applied), "metadata")
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"fake-pw-xyz", "fake-key-abc"} {
		if strings.Contains(got, leak) {
			t.Errorf("nested value leaks %q: %s", leak, got)
		}
	}
}

// In a rollout diff the annotation is compared after redaction, and a changed credential inside it
// still shows that something changed.
func TestDiffTemplatesRedactsLastAppliedAnnotation(t *testing.T) {
	tmpl := func(pw string) map[string]any {
		return map[string]any{"metadata": map[string]any{"annotations": map[string]any{
			lastApplied: `{"env":[{"name":"DB_PASSWORD","value":"` + pw + `"}]}`,
		}}}
	}
	oldR, newR := redactTemplates(tmpl("fake-old-1"), tmpl("fake-new-2"))
	b, _ := json.Marshal([]any{oldR, newR})
	if strings.Contains(string(b), "fake-old-1") || strings.Contains(string(b), "fake-new-2") {
		t.Errorf("diff input leaks: %s", b)
	}
	if !strings.Contains(string(b), "redacted, changed") {
		t.Errorf("changed credential in the annotation not marked: %s", b)
	}
}
