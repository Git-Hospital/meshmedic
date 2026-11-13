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

// A secret carried only as JSON structure (env entries, also inside an initContainer) can be
// redacted only by parsing the annotation; text redaction cannot see it.
func TestRedactLastAppliedNested(t *testing.T) {
	applied := `{"spec":{"template":{"spec":{"initContainers":[{"name":"init","env":[{"name":"DB_PASSWORD","value":"fake-pw-xyz"}],` +
		`"command":["sh","-c","export DB_PASSWORD=fake-pw-xyz; run"]}],` +
		`"containers":[{"name":"app","env":[{"name":"API_KEY","value":"fake-key-abc"}],"args":["--db-password=fake-pw-xyz"]}]}}}}`
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

// Anything after the first JSON document fails closed, including a stray closing bracket, which
// Decoder.More does not count as more data. Whitespace alone is fine.
func TestRedactLastAppliedTrailingData(t *testing.T) {
	doc := `{"env":[{"name":"DB_PASSWORD","value":"fake-pw-xyz"}]}`
	for _, bad := range []string{doc + `}`, doc + `] x`, doc + `]`, `{}{}`, doc + ` x`, doc + doc} {
		got, err := ExtractField(deploymentWithAnnotation(bad), "metadata.annotations")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "fake-pw-xyz") {
			t.Errorf("%q leaks: %s", bad, got)
		}
		if !strings.Contains(got, `"`+lastApplied+`":"[redacted]"`) {
			t.Errorf("%q not replaced by the marker: %s", bad, got)
		}
	}
	for _, ok := range []string{doc + " ", doc + "\n", doc + " \t\r\n"} {
		got, err := ExtractField(deploymentWithAnnotation(ok), "metadata.annotations")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "fake-pw-xyz") {
			t.Errorf("%q leaks: %s", ok, got)
		}
		if strings.Contains(got, `"`+lastApplied+`":"[redacted]"`) || !strings.Contains(got, "DB_PASSWORD") {
			t.Errorf("%q with trailing whitespace was not parsed and redacted normally: %s", ok, got)
		}
	}
}

// A secret-like env name hides its value whatever type the value has: the API server rejects
// non-string values, but a hand-written annotation is never validated.
func TestRedactNonStringEnvValue(t *testing.T) {
	values := map[string]string{
		"object": `{"x":"fake-pw-xyz"}`,
		"number": `12345`,
		"array":  `["fake-pw-xyz"]`,
		"bool":   `true`,
	}
	for kind, v := range values {
		entry := `{"name":"DB_PASSWORD","value":` + v + `}`
		// On the annotation path.
		got, err := ExtractField(deploymentWithAnnotation(`{"env":[`+entry+`]}`), "metadata.annotations")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "fake-pw-xyz") || strings.Contains(got, "12345") || strings.Contains(got, "true") {
			t.Errorf("%s value leaks on the annotation path: %s", kind, got)
		}
		if !strings.Contains(got, "[redacted]") {
			t.Errorf("%s value not marked on the annotation path: %s", kind, got)
		}
		// On a plain object.
		var obj any
		if err := json.Unmarshal([]byte(entry), &obj); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(redactAll(obj))
		if strings.Contains(string(b), "fake-pw-xyz") || strings.Contains(string(b), "12345") || strings.Contains(string(b), "true") {
			t.Errorf("%s value leaks on a plain object: %s", kind, b)
		}
		if !strings.Contains(string(b), "[redacted]") {
			t.Errorf("%s value not marked on a plain object: %s", kind, b)
		}
	}
}

// Two different non-JSON annotation values print alike once redacted, so the diff must say that
// they changed, and show neither.
func TestDiffTemplatesNonJSONAnnotationChanged(t *testing.T) {
	tmpl := func(v string) map[string]any {
		return map[string]any{"metadata": map[string]any{"annotations": map[string]any{lastApplied: v}}}
	}
	oldR, newR := redactTemplates(tmpl("not json fake-old-1"), tmpl("not json fake-new-2"))
	b, _ := json.Marshal([]any{oldR, newR})
	if strings.Contains(string(b), "fake-old-1") || strings.Contains(string(b), "fake-new-2") {
		t.Errorf("diff input leaks: %s", b)
	}
	if !strings.Contains(string(b), "[redacted, changed]") {
		t.Errorf("changed non-JSON annotation not marked: %s", b)
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

func marshalRedacted(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(redactAll(v))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A secret-like key hides its value in any map, not only in an env entry (#43). Ordinary keys,
// and the key names themselves, stay readable.
func TestRedactSecretLikeMapKeys(t *testing.T) {
	cm := func(data map[string]any) map[string]any {
		return map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "data": data}
	}
	tests := []struct {
		name  string
		obj   map[string]any
		leaks []string
		wants []string
	}{
		{"plain key", cm(map[string]any{"DB_PASSWORD": "fake-pw-1", "host": "db.local"}),
			[]string{"fake-pw-1"}, []string{`"DB_PASSWORD":"[redacted]"`, `"host":"db.local"`}},
		{"lower and mixed case", cm(map[string]any{"api_token": "fake-t-1", "ApiKey": "fake-k-1", "tls.cert": "fake-c-1"}),
			[]string{"fake-t-1", "fake-k-1", "fake-c-1"}, []string{`"api_token":"[redacted]"`, `"ApiKey":"[redacted]"`, `"tls.cert":"[redacted]"`}},
		{"nested map", cm(map[string]any{"db": map[string]any{"user": "app", "password": "fake-pw-2"}}),
			[]string{"fake-pw-2"}, []string{`"user":"app"`, `"password":"[redacted]"`}},
		{"list of maps", map[string]any{"items": []any{
			map[string]any{"name": "a", "secret": "fake-s-1"},
			map[string]any{"name": "b", "replicas": 3.0},
		}}, []string{"fake-s-1"}, []string{`"secret":"[redacted]"`, `"replicas":3`, `"name":"b"`}},
		{"number and bool", cm(map[string]any{"pin_token": 4711.0, "key_enabled": true}),
			[]string{"4711", "true"}, []string{`"pin_token":"[redacted]"`, `"key_enabled":"[redacted]"`}},
		{"map under a secret-like key is decided by its own keys", cm(map[string]any{"credentials": map[string]any{"user": "app", "password": "fake-pw-3"}}),
			[]string{"fake-pw-3"}, []string{`"user":"app"`, `"password":"[redacted]"`}},
		{"list of plain values hides its length", cm(map[string]any{"tokens": []any{"fake-a", "fake-b"}}),
			[]string{"fake-a", "fake-b"}, []string{`"tokens":"[redacted]"`}},
		{"list of objects keeps its shape", cm(map[string]any{"secrets": []any{map[string]any{"name": "regcred"}}}),
			nil, []string{`"name":"regcred"`}},
		{"null stays null", cm(map[string]any{"password": nil}), nil, []string{`"password":null`}},
		{"ordinary config is untouched", cm(map[string]any{"replicas": 3.0, "host": "h", "name": "n", "mode": "fast"}),
			nil, []string{`"replicas":3`, `"host":"h"`, `"name":"n"`, `"mode":"fast"`}},
	}
	for _, tc := range tests {
		got := marshalRedacted(t, tc.obj)
		for _, l := range tc.leaks {
			if strings.Contains(got, l) {
				t.Errorf("%s: leaks %q: %s", tc.name, l, got)
			}
		}
		for _, w := range tc.wants {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q: %s", tc.name, w, got)
			}
		}
	}
}

// Every value under a core Secret's data and stringData is hidden, whatever its key is called;
// only a Secret with apiVersion v1 is recognised, and other kinds keep an innocent key readable.
func TestRedactSecretData(t *testing.T) {
	secret := func(kind, apiVersion string) map[string]any {
		return map[string]any{
			"apiVersion": apiVersion, "kind": kind,
			"metadata":   map[string]any{"name": "creds", "namespace": "app"},
			"type":       "Opaque",
			"data":       map[string]any{"username": "ZmFrZS11c2Vy", "config": "ZmFrZS1jZmc=", "n": 7.0, "m": map[string]any{"x": "fake-nested"}, "gone": nil},
			"stringData": map[string]any{"dsn": "postgres://fake-dsn", "plain": "fake-plain"},
		}
	}
	got := marshalRedacted(t, secret("Secret", "v1"))
	for _, leak := range []string{"ZmFrZS11c2Vy", "ZmFrZS1jZmc=", "fake-nested", "postgres://fake-dsn", "fake-plain", ":7"} {
		if strings.Contains(got, leak) {
			t.Errorf("Secret leaks %q: %s", leak, got)
		}
	}
	for _, want := range []string{`"username":"[redacted]"`, `"dsn":"[redacted]"`, `"m":"[redacted]"`, `"gone":null`, `"type":"Opaque"`, `"name":"creds"`} {
		if !strings.Contains(got, want) {
			t.Errorf("Secret output missing %q: %s", want, got)
		}
	}
	// A Secret inside a List is recognised by its own kind.
	list := marshalRedacted(t, map[string]any{"kind": "List", "items": []any{secret("Secret", "v1")}})
	if strings.Contains(list, "ZmFrZS11c2Vy") {
		t.Errorf("Secret in a List leaks: %s", list)
	}
	// Not a core Secret: innocent keys are shown as they are.
	for _, other := range []map[string]any{secret("ConfigMap", "v1"), secret("Secret", "example.io/v1"), secret("Secret", "")} {
		out := marshalRedacted(t, other)
		if !strings.Contains(out, "ZmFrZS11c2Vy") || !strings.Contains(out, "fake-plain") {
			t.Errorf("%v/%v over-redacted: %s", other["kind"], other["apiVersion"], out)
		}
	}
	// data that is not an object is replaced whole.
	odd := marshalRedacted(t, map[string]any{"apiVersion": "v1", "kind": "Secret", "data": "ZmFrZQ=="})
	if strings.Contains(odd, "ZmFrZQ==") || !strings.Contains(odd, `"data":"[redacted]"`) {
		t.Errorf("non-object data: %s", odd)
	}
}

// A catalog path that points at one value, or at the data object, goes through the same rules.
func TestExtractFieldRedactsMapKeysAndSecretData(t *testing.T) {
	cm := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "data": map[string]any{"DB_PASSWORD": "fake-pw", "host": "db"}}
	secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "data": map[string]any{"username": "ZmFrZQ==", "host": "aG9zdA=="}}
	for _, tc := range []struct {
		obj  map[string]any
		path string
		want string
	}{
		{cm, "data.DB_PASSWORD", "[redacted]"},
		{cm, "data.host", "db"},
		{cm, "data", `{"DB_PASSWORD":"[redacted]","host":"db"}`},
		{secret, "data.username", "[redacted]"},
		{secret, "data.host", "[redacted]"},
		{secret, "data", `{"host":"[redacted]","username":"[redacted]"}`},
	} {
		got, err := ExtractField(tc.obj, tc.path)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.path, got, err, tc.want)
		}
	}
	if cm["data"].(map[string]any)["DB_PASSWORD"] != "fake-pw" {
		t.Error("ExtractField changed the object it was given")
	}
}

// A redacted map value or Secret value that changed still reads as changed, and no template
// that differs ever prints as no change.
func TestDiffTemplatesMarksChangedMapKeysAndSecretData(t *testing.T) {
	obj := func(pw, key, host string) map[string]any {
		return map[string]any{"kind": "Secret", "apiVersion": "v1",
			"metadata":   map[string]any{"name": "s", "namespace": "n"},
			"data":       map[string]any{"user": key},
			"stringData": map[string]any{"host": host},
			"auth":       map[string]any{"password": pw}}
	}
	got := diffTemplates(obj("fake-pw-old", "fake-u-old", "same"), obj("fake-pw-new", "fake-u-new", "same"))
	for _, leak := range []string{"fake-pw-old", "fake-pw-new", "fake-u-old", "fake-u-new", `"same"`} {
		if strings.Contains(got, leak) {
			t.Errorf("diff leaks %q: %s", leak, got)
		}
	}
	for _, want := range []string{`"password": "[redacted, changed]"`, `"user": "[redacted, changed]"`} {
		if !strings.Contains(got, want) {
			t.Errorf("diff missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, `"host": "[redacted, changed]"`) {
		t.Errorf("unchanged Secret value marked changed: %s", got)
	}
}
