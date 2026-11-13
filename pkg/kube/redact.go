package kube

import (
	"bytes"
	"encoding/json"
	"io"
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
// name and a value) with a secret-like name, the value; in a ConfigMap the values under data and
// binaryData whose key is secret-like; in a Secret every value under data and stringData; in the
// annotations of any metadata the values whose key is secret-like (#43); and in every
// other string the values of secret-like name=value pairs. changed is asked, with a key for the value's place in the tree
// and the plain value, whether to mark it changed. scope names the place: the object keys on the
// way down and the names of named objects such as containers, never list indexes.
func redactTree(v any, scope string, changed func(key, value string) bool) any {
	return redactNode(v, scope, "", changed)
}

// redactNode is redactTree with one more argument: parent, the key this value sits under, which
// tells the annotations of a metadata object from other maps.
func redactNode(v any, scope, parent string, changed func(key, value string) bool) any {
	switch t := v.(type) {
	case map[string]any:
		name, okName := t["name"].(string)
		rawValue := t["value"]
		inner := scope
		if okName {
			inner = scope + "/" + name
		}
		out := make(map[string]any, len(t))
		kind := objectKind(t)
		for k, e := range t {
			if str, ok := e.(string); ok && k == lastAppliedKey {
				out[k] = redactLastApplied(str, inner+"."+k, changed)
				continue
			}
			switch {
			case kind == "secret" && (k == "data" || k == "stringData"):
				// A Secret: every value, whatever its key; data is only base64, the value itself.
				out[k] = redactEntries(e, inner+"."+k, func(string) bool { return true }, changed)
				continue
			case kind == "configmap" && (k == "data" || k == "binaryData"):
				// A ConfigMap: a person chose these keys, so a secret-like one names a credential.
				out[k] = redactEntries(e, inner+"."+k, secretLike, changed)
				continue
			case parent == "metadata" && k == "annotations":
				out[k] = redactEntries(e, inner+"."+k, secretLike, changed)
				continue
			case k == "items" && (kind == "secretlist" || kind == "configmaplist"):
				if list, ok := e.([]any); ok {
					out[k] = redactListItems(list, strings.TrimSuffix(kind, "list"), inner+"."+k, changed)
					continue
				}
			}
			out[k] = redactNode(e, inner+"."+k, k, changed)
		}
		if okName && rawValue != nil && secretLike(name) {
			// Any non-nil value is hidden, whatever its type: the API server only accepts strings,
			// but a hand-written annotation is never validated. A non-string is compared by its JSON.
			out["value"] = redactedOrChanged(scope+"\x00env\x00"+name, plainValue(rawValue), changed)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactNode(e, scope, parent, changed)
		}
		return out
	case string:
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

// objectKind is the lower-cased kind of an object, "" when it has none. Kind is compared without
// case and whatever the apiVersion is, at any depth, so a Secret inside a kubectl List or inside the
// last-applied annotation's JSON is found as well as a top-level one.
func objectKind(m map[string]any) string {
	k, _ := m["kind"].(string)
	return strings.ToLower(k)
}

// plainValue is the text a value is compared by: the string itself, else its JSON.
func plainValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// redactedOrChanged is the marker for a hidden value: the changed marker when the value differs
// from the older template's at the same key, else the plain one.
func redactedOrChanged(key, value string, changed func(key, value string) bool) string {
	if changed(key, value) {
		return redactedChanged
	}
	return redacted
}

// redactListItems redacts the items of a SecretList or ConfigMapList as Secrets or ConfigMaps even
// when an item names no kind of its own: none at all, an empty one, or one that is not a string.
// The item is given the list's kind for the walk only. Afterwards its kind field is as it came.
func redactListItems(list []any, kind, scope string, changed func(key, value string) bool) []any {
	out := make([]any, len(list))
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			out[i] = redactNode(e, scope, "items", changed)
			continue
		}
		if objectKind(m) != "" {
			out[i] = redactNode(e, scope, "items", changed)
			continue
		}
		own, had := m["kind"]
		withKind := make(map[string]any, len(m)+1)
		for k, x := range m {
			withKind[k] = x
		}
		withKind["kind"] = kind
		r := redactNode(withKind, scope, "items", changed).(map[string]any)
		if had {
			r["kind"] = own
		} else {
			delete(r, "kind")
		}
		out[i] = r
	}
	return out
}

// redactEntries hides, in the map v, the non-nil values whose key hide accepts, whatever type the
// value has; the map and its keys stay, so a path such as data.DB_PASSWORD still resolves and shows
// the marker. The other entries are redacted by the usual rules. A v that is not a map (data as a
// string) is replaced whole; nil stays nil. A hidden value that differs from the older template's
// shows as changed. scope is the map's place, built as for env entries (the keys on the way down and
// the names of named objects, no list indexes), and the comparison key is that scope plus the entry
// key. It is unique within a pod template, the only input the diff receives; two objects with the
// same entry key in one list would share a key, which a pod template does not have.
func redactEntries(v any, scope string, hide func(key string) bool, changed func(key, value string) bool) any {
	m, ok := v.(map[string]any)
	if !ok {
		if v == nil {
			return nil
		}
		return redactedOrChanged(scope+"\x00hidden\x00", plainValue(v), changed)
	}
	rest := make(map[string]any, len(m))
	out := make(map[string]any, len(m))
	for k, e := range m {
		if e != nil && hide(k) {
			out[k] = redactedOrChanged(scope+"\x00hidden\x00"+k, plainValue(e), changed)
		} else {
			rest[k] = e
		}
	}
	for k, e := range redactTree(rest, scope, changed).(map[string]any) {
		out[k] = e
	}
	return out
}

// lastAppliedKey is the annotation kubectl apply uses to keep the whole applied spec, env entries
// included, as a JSON string (#33).
const lastAppliedKey = "kubectl.kubernetes.io/last-applied-configuration"

// redactLastApplied redacts the JSON held in the last-applied-configuration annotation like any
// object, and serialises it again. The value is not parsed anywhere else, so env entries inside it
// would otherwise pass as plain text. A value that does not parse as one JSON document is replaced
// whole: nothing can be said about what it holds, so it fails closed. In a diff the replacement is
// marked changed when the value differs from the older one, as for an env value.
func redactLastApplied(value, scope string, changed func(key, value string) bool) string {
	failed := func() string {
		if changed(scope+"\x00annotation", value) {
			return redactedChanged
		}
		return redacted
	}
	dec := json.NewDecoder(strings.NewReader(value))
	dec.UseNumber() // keep large numbers as written
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return failed()
	}
	// Exactly one document: nothing but whitespace may follow. Decoder.More is no test for this,
	// since it reports false before a stray closing bracket.
	if _, err := dec.Token(); err != io.EOF {
		return failed()
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redactTree(parsed, scope, changed)); err != nil {
		return failed()
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
