package kube

import (
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

// redactTemplates returns copies of two pod templates with the plain values of secret-like env
// entries replaced. A value that differs between the two is marked changed in the newer one, so
// the diff still shows that a credential changed without showing either value. valueFrom
// entries hold no value and are left as they are.
func redactTemplates(oldT, newT any) (any, any) {
	before := map[string]string{}
	oldR := redactEnv(oldT, func(name, value string) string {
		if _, seen := before[name]; !seen {
			before[name] = value
		}
		return redacted
	})
	newR := redactEnv(newT, func(name, value string) string {
		if prev, seen := before[name]; seen && prev != value {
			return redactedChanged
		}
		return redacted
	})
	return oldR, newR
}

// redactEnv copies v and, in every object shaped like a Kubernetes env entry (a string name and
// a string value) with a secret-like name, replaces the value with what mark returns.
func redactEnv(v any, mark func(name, value string) string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = redactEnv(e, mark)
		}
		name, okName := t["name"].(string)
		value, okValue := t["value"].(string)
		if okName && okValue && secretLike(name) {
			out["value"] = mark(name, value)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactEnv(e, mark)
		}
		return out
	default:
		return v
	}
}
