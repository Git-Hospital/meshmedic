package kube

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
)

// Secret-like values are kept out of incident reports (#20). A report becomes a pull request
// body in a configuration repository, and a workload can carry a credential as a plain env
// value. The rule is a name match, case-insensitive, on any of these words. It errs toward
// redacting: a name like MONKEY_COUNT is redacted too, which costs a reviewer one value.
var secretWords = []string{"PASSWORD", "PASSWD", "SECRET", "TOKEN", "KEY", "CREDENTIAL", "CERT"}

const (
	redacted        = "[redacted]"
	redactedChanged = "[redacted, changed]"
)

// secretLike reports whether an env var or key name looks like it holds a credential.
func secretLike(name string) bool {
	upper := strings.ToUpper(name)
	for _, w := range secretWords {
		if strings.Contains(upper, w) {
			return true
		}
	}
	return false
}

// logAssignment matches name=value text in a log line whose name is secret-like. The value
// runs to the next space, or is a quoted string.
var logAssignment = regexp.MustCompile(`(?i)\b([A-Z0-9_.-]*(?:PASSWORD|PASSWD|SECRET|TOKEN|KEY|CREDENTIAL|CERT)[A-Z0-9_.-]*)=("[^"]*"|'[^']*'|[^\s"',;&]+)`)

// redactLogText replaces the value of each secret-like name=value pair in log text.
func redactLogText(text string) string {
	return logAssignment.ReplaceAllString(text, "${1}="+redacted)
}

// redactTemplates returns copies of two pod templates with secret-like values replaced: the plain
// values of secret-like env entries, and secret-like name=value text in other strings, such as
// --db-password=... in args. A value that differs between the two is marked changed in the newer
// one, so the diff still shows that a credential changed without showing either value. Values are
// matched by where they are, not by name alone: an env var is known by its container's name and its
// own, since two containers (an initContainer and the app, say) often share a name like DB_PASSWORD
// with different values. Container names, not list indexes, so reordering changes nothing.
// valueFrom entries hold no value and are left as they are.
func redactTemplates(oldT, newT any) (any, any) {
	before := map[string]string{}
	oldR := redactTree(oldT, "", func(key, value string) bool {
		if _, seen := before[key]; !seen {
			before[key] = value
		}
		return false
	})
	newR := redactTree(newT, "", func(key, value string) bool {
		prev, seen := before[key]
		return seen && prev != value
	})
	return oldR, newR
}

// redactAll returns a copy of v with every secret-like value replaced.
func redactAll(v any) any {
	return redactTree(v, "", func(string, string) bool { return false })
}

// redactTree copies v and replaces, in every object shaped like a Kubernetes env entry (a string
// name and a string value) with a secret-like name, the value, and in every other string the values
// of secret-like name=value pairs. changed is asked, with a key for the value's place in the tree
// and the plain value, whether to mark it changed. scope names the place: the object keys on the
// way down and the names of named objects such as containers, never list indexes.
func redactTree(v any, scope string, changed func(key, value string) bool) any {
	switch t := v.(type) {
	case map[string]any:
		name, okName := t["name"].(string)
		value, okValue := t["value"].(string)
		inner := scope
		if okName {
			inner = scope + "/" + name
		}
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = redactTree(e, inner+"."+k, changed)
		}
		if okName && okValue && secretLike(name) {
			out["value"] = redacted
			if changed(scope+"\x00env\x00"+name, value) {
				out["value"] = redactedChanged
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactTree(e, scope, changed)
		}
		return out
	case string:
		if j, ok := redactJSONText(t, scope, changed); ok {
			return j
		}
		r := redactLogText(t)
		if r == t {
			return t
		}
		if changed(scope+"\x00text\x00"+r, t) {
			return logAssignment.ReplaceAllString(t, "${1}="+redactedChanged)
		}
		return r
	default:
		return v
	}
}

// redactJSONText handles a string that holds a JSON object or list, such as the annotation
// kubectl.kubernetes.io/last-applied-configuration, where `kubectl apply` keeps the applied spec
// (#33). Its env entries are objects inside a string, which the name=value match does not see, so
// the text is parsed, redacted like any other tree, and written back as compact JSON. ok is false,
// and the string is left to the name=value match, when it is not JSON or holds nothing to redact.
func redactJSONText(text, scope string, changed func(key, value string) bool) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber() // large integers stay as written
	var parsed any
	if err := dec.Decode(&parsed); err != nil || dec.More() {
		return "", false
	}
	out := redactTree(parsed, scope, changed)
	if reflect.DeepEqual(parsed, out) {
		return "", false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return "", false
	}
	return strings.TrimSuffix(buf.String(), "\n"), true
}
