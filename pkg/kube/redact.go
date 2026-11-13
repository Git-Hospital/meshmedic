package kube

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
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
// annotations and labels of any metadata the values whose key is secret-like (#43); and in every
// other string the values of secret-like name=value pairs. changed is asked, with a key for the value's place in the tree
// and the plain value, whether to mark it changed. scope names the place: the object keys on the
// way down and the names of named objects such as containers, never list indexes.
func redactTree(v any, scope string, changed func(key, value string) bool) any {
	return redactNode(v, scope, "", "", changed)
}

// redactNode is redactTree with two more arguments. parent is the key this value sits under, which
// tells annotations and labels of a metadata object from other maps. pos tells apart objects that
// have no name of their own, in a list: the changed marker compares a hidden value with the one at
// the same place, and without pos two sibling objects with the same key would be compared with each
// other. pos is used only for hidden values, never for the env and text rules.
func redactNode(v any, scope, parent, pos string, changed func(key, value string) bool) any {
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
				out[k] = redactEntries(e, inner+pos+"."+k, objectID(t), func(string) bool { return true }, changed)
				continue
			case kind == "configmap" && (k == "data" || k == "binaryData"):
				// A ConfigMap: a person chose these keys, so a secret-like one names a credential.
				out[k] = redactEntries(e, inner+pos+"."+k, objectID(t), secretLike, changed)
				continue
			case parent == "metadata" && (k == "annotations" || k == "labels"):
				out[k] = redactEntries(e, inner+pos+"."+k, "", secretLike, changed)
				continue
			case k == "items" && (kind == "secretlist" || kind == "configmaplist"):
				if list, ok := e.([]any); ok {
					out[k] = redactListItems(list, strings.TrimSuffix(kind, "list"), inner+"."+k, pos, changed)
					continue
				}
			}
			out[k] = redactNode(e, inner+"."+k, k, pos, changed)
		}
		if okName && rawValue != nil && secretLike(name) {
			// Any non-nil value is hidden, whatever its type: the API server only accepts strings,
			// but a hand-written annotation is never validated. A non-string is compared by its JSON.
			value, isString := rawValue.(string)
			if !isString {
				b, _ := json.Marshal(rawValue)
				value = string(b)
			}
			out["value"] = redacted
			if changed(scope+"\x00env\x00"+name, value) {
				out["value"] = redactedChanged
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactNode(e, scope, parent, itemPos(e, pos, i), changed)
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

// objectID is namespace/name of an object, so the same key in two Secrets is never compared.
func objectID(m map[string]any) string {
	md, _ := m["metadata"].(map[string]any)
	ns, _ := md["namespace"].(string)
	name, _ := md["name"].(string)
	return ns + "/" + name
}

// itemPos extends pos for a list item that has no name of its own: by namespace/name when it is an
// object with metadata, else by list index. Items with a name are told apart by the scope already.
func itemPos(e any, pos string, i int) string {
	m, ok := e.(map[string]any)
	if !ok {
		return pos
	}
	if id := objectID(m); id != "/" {
		return pos + "[" + id + "]"
	}
	if _, named := m["name"].(string); named {
		return pos
	}
	return pos + "[" + strconv.Itoa(i) + "]"
}

// redactListItems redacts the items of a SecretList or ConfigMapList as Secrets or ConfigMaps even
// when an item carries no kind of its own.
func redactListItems(list []any, kind, scope, pos string, changed func(key, value string) bool) []any {
	out := make([]any, len(list))
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok || objectKind(m) != "" {
			out[i] = redactNode(e, scope, "items", itemPos(e, pos, i), changed)
			continue
		}
		withKind := make(map[string]any, len(m)+1)
		for k, x := range m {
			withKind[k] = x
		}
		withKind["kind"] = kind
		r := redactNode(withKind, scope, "items", itemPos(e, pos, i), changed).(map[string]any)
		delete(r, "kind") // the output shows the item as it came
		out[i] = r
	}
	return out
}

// redactEntries hides, in the map v, the non-nil values whose key hide accepts, whatever type the
// value has; the map and its keys stay, so a path such as data.DB_PASSWORD still resolves and shows
// the marker. The other entries are redacted by the usual rules. A v that is not a map (data as a
// string) is replaced whole; nil stays nil. A hidden value that differs from the older template's
// shows as changed: the comparison key is the place (scope, which has the names of named objects
// and the position of unnamed ones), the object's namespace and name when given, and the entry key.
// A non-string is compared by its JSON.
func redactEntries(v any, scope, id string, hide func(key string) bool, changed func(key, value string) bool) any {
	mark := func(entryKey string, value any) string {
		var plain string
		if s, ok := value.(string); ok {
			plain = s
		} else {
			b, _ := json.Marshal(value)
			plain = string(b)
		}
		if changed(scope+"\x00hidden\x00"+id+"\x00"+entryKey, plain) {
			return redactedChanged
		}
		return redacted
	}
	m, ok := v.(map[string]any)
	if !ok {
		if v == nil {
			return nil
		}
		return mark("", v)
	}
	rest := make(map[string]any, len(m))
	out := make(map[string]any, len(m))
	for k, e := range m {
		if e != nil && hide(k) {
			out[k] = mark(k, e)
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
