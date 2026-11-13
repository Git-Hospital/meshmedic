package kube

import (
	"encoding/json"
	"fmt"
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

func checkRedacted(t *testing.T, name string, got string, leaks, wants []string) {
	t.Helper()
	for _, l := range leaks {
		if strings.Contains(got, l) {
			t.Errorf("%s: leaks %q: %s", name, l, got)
		}
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s: missing %q: %s", name, w, got)
		}
	}
}

// In a ConfigMap, a value under data or binaryData whose key is secret-like is hidden, whatever
// its type; other keys stay readable. kind is compared without case and whatever the apiVersion is
// (#43).
func TestRedactConfigMapData(t *testing.T) {
	cm := func(kind, apiVersion string) map[string]any {
		o := map[string]any{"kind": kind,
			"data":       map[string]any{"DB_PASSWORD": "fake-pw-1", "api_token": "fake-t-1", "pin_key": 4711.0, "tls.cert": map[string]any{"x": "fake-n"}, "gone_secret": nil, "host": "db.local", "replicas": "3"},
			"binaryData": map[string]any{"signing-key": "ZmFrZS1rZXk=", "logo": "ZmFrZS1sb2dv"}}
		if apiVersion != "" {
			o["apiVersion"] = apiVersion
		}
		return o
	}
	for _, o := range []map[string]any{cm("ConfigMap", "v1"), cm("configmap", "v1"), cm("CONFIGMAP", ""), cm("ConfigMap", "example.io/v9")} {
		got := marshalRedacted(t, o)
		checkRedacted(t, fmt.Sprint(o["kind"], o["apiVersion"]), got,
			[]string{"fake-pw-1", "fake-t-1", "4711", "fake-n", "ZmFrZS1rZXk="},
			[]string{`"DB_PASSWORD":"[redacted]"`, `"api_token":"[redacted]"`, `"pin_key":"[redacted]"`, `"tls.cert":"[redacted]"`, `"gone_secret":null`,
				`"signing-key":"[redacted]"`, `"host":"db.local"`, `"replicas":"3"`, `"logo":"ZmFrZS1sb2dv"`})
	}
	// Another kind with the same shape is not a ConfigMap: no rule on its keys.
	got := marshalRedacted(t, map[string]any{"kind": "Widget", "data": map[string]any{"DB_PASSWORD": "fake-pw-1"}})
	checkRedacted(t, "other kind", got, nil, []string{"fake-pw-1"})
}

// In a Secret every value under data and stringData is hidden, whatever its key; kind is compared
// without case and whatever the apiVersion is, at any depth.
func TestRedactSecretData(t *testing.T) {
	secret := func(kind, apiVersion string) map[string]any {
		o := map[string]any{"kind": kind, "metadata": map[string]any{"name": "creds", "namespace": "app"}, "type": "Opaque",
			"data":       map[string]any{"username": "ZmFrZS11c2Vy", "config": "ZmFrZS1jZmc=", "n": 7.0, "m": map[string]any{"x": "fake-nested"}, "gone": nil},
			"stringData": map[string]any{"dsn": "postgres://fake-dsn", "plain": "fake-plain"}}
		if apiVersion != "" {
			o["apiVersion"] = apiVersion
		}
		return o
	}
	leaks := []string{"ZmFrZS11c2Vy", "ZmFrZS1jZmc=", "fake-nested", "postgres://fake-dsn", "fake-plain", ":7"}
	wants := []string{`"username":"[redacted]"`, `"dsn":"[redacted]"`, `"m":"[redacted]"`, `"gone":null`, `"type":"Opaque"`, `"name":"creds"`}
	for _, o := range []map[string]any{secret("Secret", "v1"), secret("secret", "v1"), secret("Secret", "example.io/v2"), secret("Secret", "")} {
		checkRedacted(t, fmt.Sprint(o["kind"], o["apiVersion"]), marshalRedacted(t, o), leaks, wants)
	}
	// Inside a kubectl List, at any depth.
	list := map[string]any{"kind": "List", "items": []any{secret("Secret", "v1")}}
	checkRedacted(t, "List", marshalRedacted(t, list), leaks, wants)
	// A SecretList or ConfigMapList item that carries no kind is read as the list's kind.
	noKind := secret("", "")
	delete(noKind, "kind")
	checkRedacted(t, "SecretList", marshalRedacted(t, map[string]any{"kind": "SecretList", "items": []any{noKind}}), leaks, wants)
	cmItem := map[string]any{"data": map[string]any{"DB_PASSWORD": "fake-pw-1", "host": "db.local"}}
	got := marshalRedacted(t, map[string]any{"kind": "ConfigMapList", "items": []any{cmItem}})
	checkRedacted(t, "ConfigMapList", got, []string{"fake-pw-1"}, []string{`"host":"db.local"`})
	// The item is shown as it came: only the list's own kind appears.
	if strings.Count(marshalRedacted(t, map[string]any{"kind": "SecretList", "items": []any{noKind}}), `"kind"`) != 1 {
		t.Error("an item without a kind gained one")
	}
	// Another kind with the same shape is not a Secret.
	checkRedacted(t, "other kind", marshalRedacted(t, secret("Widget", "v1")), nil, []string{"fake-plain"})
	// data that is not an object is replaced whole.
	odd := marshalRedacted(t, map[string]any{"kind": "Secret", "data": "ZmFrZQ=="})
	checkRedacted(t, "string data", odd, []string{"ZmFrZQ=="}, []string{`"data":"[redacted]"`})
}

// A Secret whose last-applied annotation holds its data: the annotation is parsed, and its kind
// is checked like any other object's.
func TestRedactSecretInLastApplied(t *testing.T) {
	inner := `{"kind":"Secret","apiVersion":"v1","data":{"username":"ZmFrZS11c2Vy"},"metadata":{"name":"s"}}`
	obj := map[string]any{"kind": "Secret", "apiVersion": "v1", "metadata": map[string]any{"name": "s", "annotations": map[string]any{lastAppliedKey: inner}}}
	got, err := ExtractField(obj, "metadata.annotations")
	if err != nil {
		t.Fatal(err)
	}
	checkRedacted(t, "last-applied", got, []string{"ZmFrZS11c2Vy"}, []string{`\"username\":\"[redacted]\"`})
	// and in a Deployment's annotation, not itself a Secret
	dep := deploymentWithAnnotation(inner)
	got, err = ExtractField(dep, "metadata.annotations")
	if err != nil {
		t.Fatal(err)
	}
	checkRedacted(t, "last-applied in a Deployment", got, []string{"ZmFrZS11c2Vy"}, nil)
}

// In the annotations of a metadata object, at any depth, a value whose key is secret-like is
// hidden. Other keys are readable; labels are not redacted (a label is also printed in selectors),
// and neither are annotation-like maps elsewhere.
func TestRedactMetadataAnnotationsAndLabels(t *testing.T) {
	dep := map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "web",
		"annotations": map[string]any{"checksum/secret": "fake-sum", "sidecar.istio.io/userVolume": "[{\"name\":\"v\"}]"},
		"labels":      map[string]any{"vendor-key": "fake-k", "app": "web"}},
		"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
			"annotations": map[string]any{"checksum/secret": "fake-sum-2", "n": 5.0, "cert-hash": 9.0}},
			"spec": map[string]any{"containers": []any{}}}}}
	got := marshalRedacted(t, dep)
	checkRedacted(t, "deployment", got, []string{"fake-sum", "fake-sum-2", ":9"},
		[]string{`"checksum/secret":"[redacted]"`, `"vendor-key":"fake-k"`, `"cert-hash":"[redacted]"`, `"app":"web"`, `"n":5`, `sidecar.istio.io/userVolume":"[{\"name\":\"v\"}]"`})
	// Not under metadata: no rule.
	got = marshalRedacted(t, map[string]any{"spec": map[string]any{"labels": map[string]any{"api_key": "fake-k"}, "annotations": map[string]any{"token": "fake-t"}}})
	checkRedacted(t, "not metadata", got, nil, []string{"fake-k", "fake-t"})
}

// A catalog path that points at one value, or at the data object, goes through the same rules.
func TestExtractFieldRedactsDataPaths(t *testing.T) {
	cm := map[string]any{"kind": "ConfigMap", "data": map[string]any{"DB_PASSWORD": "fake-pw", "host": "db"}}
	secret := map[string]any{"kind": "Secret", "data": map[string]any{"username": "ZmFrZQ==", "host": "aG9zdA=="}}
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

// A hidden value that changed reads as changed; identical pod templates never do.
func TestDiffTemplatesHiddenValuesChanged(t *testing.T) {
	obj := func(sum, pw string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"annotations": map[string]any{"checksum/secret": sum, "app": "web"}},
			"spec":     map[string]any{"template": map[string]any{"metadata": map[string]any{"annotations": map[string]any{"checksum/secret": "t-" + sum}}}},
			"cm":       map[string]any{"kind": "ConfigMap", "data": map[string]any{"token": pw}},
			"sec":      map[string]any{"kind": "Secret", "data": map[string]any{"token": "s-" + pw}}}
	}
	if got := diffTemplates(obj("one", "p"), obj("one", "p")); strings.Contains(got, "changed") {
		t.Errorf("identical templates: %s", got)
	}
	got := diffTemplates(obj("one", "p"), obj("one", "q"))
	if strings.Contains(got, `"q"`) || strings.Count(got, "[redacted, changed]") != 2 {
		t.Errorf("changed Secret and ConfigMap values: %s", got)
	}
	got = diffTemplates(obj("one", "p"), obj("ONE", "p"))
	if strings.Contains(got, "ONE") || !strings.Contains(got, "[redacted, changed]") {
		t.Errorf("changed checksum/secret: %s", got)
	}
}

// The text and env rules under an annotation are keyed by the scope as before, not by list
// position: a list item that moves must still show a changed value as changed.
func TestDiffTemplatesAnnotationTextKeyedAsBefore(t *testing.T) {
	item := func(cfg string) map[string]any {
		return map[string]any{"metadata": map[string]any{"annotations": map[string]any{"cfg": cfg}}}
	}
	oldT := map[string]any{"x": []any{item("password=a")}, "z": 1.0}
	newT := map[string]any{"x": []any{map[string]any{"q": 1.0}, item("password=b")}, "z": 2.0}
	got := diffTemplates(oldT, newT)
	for _, want := range []string{`- "cfg": "password=[redacted]"`, `+ "cfg": "password=[redacted, changed]"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
}

// An item of a SecretList keeps a kind it came with, even an empty or odd one.
func TestRedactListItemKeepsItsOwnKind(t *testing.T) {
	for _, k := range []any{"", 7.0} {
		got := marshalRedacted(t, map[string]any{"kind": "SecretList", "items": []any{map[string]any{"kind": k, "data": map[string]any{"u": "fake"}}}})
		if strings.Count(got, `"kind"`) != 2 {
			t.Errorf("item kind %v dropped: %s", k, got)
		}
		if strings.Contains(got, "fake") {
			t.Errorf("an item whose kind is %v was not read as a Secret: %s", k, got)
		}
	}
}

func guardTemplate(token string) map[string]any {
	return map[string]any{"kind": "Deployment", "apiVersion": "apps/v1", "spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"automountServiceAccountToken": false,
		"tolerations":                  []any{map[string]any{"key": "node-role", "operator": "Equal", "value": "infra", "effect": "NoSchedule"}},
		"topologySpreadConstraints":    []any{map[string]any{"topologyKey": "kubernetes.io/hostname", "maxSkew": 1.0}},
		"volumes":                      []any{map[string]any{"name": "creds", "secret": map[string]any{"secretName": "db-creds"}}},
		"containers": []any{map[string]any{"name": "app", "image": "app:1", "env": []any{
			map[string]any{"name": "DB_USER", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "db", "key": "user"}}},
			map[string]any{"name": "MODE", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "cfg", "key": "mode"}}},
			map[string]any{"name": "API_TOKEN", "value": token},
		}}},
	}}}}
}

// Kubernetes reference fields (secretKeyRef, configMapKeyRef, secretName, topologyKey, a
// toleration's key, automountServiceAccountToken) render as they did before any map-key rule. The
// expected strings were produced by running origin/main's code (e2ec1f3) on this same template.
func TestRegressionGuardReferenceFieldsStayReadable(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"spec.template.spec", "{\"automountServiceAccountToken\":false,\"containers\":[{\"env\":[{\"name\":\"DB_USER\",\"valueFrom\":{\"secretKeyRef\":{\"key\":\"user\",\"name\":\"db\"}}},{\"name\":\"MODE\",\"valueFrom\":{\"configMapKeyRef\":{\"key\":\"mode\",\"name\":\"cfg\"}}},{\"name\":\"API_TOKEN\",\"value\":\"[redacted]\"}],\"image\":\"app:1\",\"name\":\"app\"}],\"tolerations\":[{\"effect\":\"NoSchedule\",\"key\":\"node-role\",\"operator\":\"Equal\",\"value\":\"infra\"}],\"topologySpreadConstraints\":[{\"maxSkew\":1,\"topologyKey\":\"kubernetes.io/hostname\"}],\"volumes\":[{\"name\":\"creds\",\"secret\":{\"secretName\":\"db-creds\"}}]}"},
		{"spec.template.spec.containers[*].env", "{\"name\":\"DB_USER\",\"valueFrom\":{\"secretKeyRef\":{\"key\":\"user\",\"name\":\"db\"}}} {\"name\":\"MODE\",\"valueFrom\":{\"configMapKeyRef\":{\"key\":\"mode\",\"name\":\"cfg\"}}} API_TOKEN=[redacted]"},
		{"spec.template.spec.volumes", "{\"name\":\"creds\",\"secret\":{\"secretName\":\"db-creds\"}}"},
		{"spec.template.spec.tolerations", "[{\"effect\":\"NoSchedule\",\"key\":\"node-role\",\"operator\":\"Equal\",\"value\":\"infra\"}]"},
		{"spec.template.spec.topologySpreadConstraints", "[{\"maxSkew\":1,\"topologyKey\":\"kubernetes.io/hostname\"}]"},
		{"spec.template.spec.automountServiceAccountToken", "false"},
	} {
		got, err := ExtractField(guardTemplate("tok-1"), tc.path)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.path, got, err, tc.want)
		}
	}
	if got, want := diffTemplates(guardTemplate("tok-1"), guardTemplate("tok-2")), "- \"value\": \"[redacted]\"\n+ \"value\": \"[redacted, changed]\""; got != want {
		t.Errorf("diff: got %q; want %q", got, want)
	}
}
