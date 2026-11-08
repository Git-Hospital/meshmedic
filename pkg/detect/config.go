package detect

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Git-Hospital/meshmedic/pkg/catalog"
	"github.com/Git-Hospital/meshmedic/pkg/prom"
	"gopkg.in/yaml.v3"
)

// Config is the watch configuration: where Prometheus lives, how often to
// evaluate, and which targets to watch.
type Config struct {
	Prometheus string `yaml:"prometheus"`
	// PrometheusAuth reaches a Prometheus behind a token, basic auth or a
	// private CA. Nil sends no credentials.
	PrometheusAuth *PrometheusAuth `yaml:"prometheusAuth"`
	Interval       string          `yaml:"interval"`
	Targets        []Target        `yaml:"targets"`
	GitOps         *GitOps         `yaml:"gitops"`
	// BaselineState is where the learned per-signal baseline is persisted.
	// Empty disables baseline-relative thresholds; scenarios then use their
	// static thresholds only.
	BaselineState string `yaml:"baselineState"`

	// StateFile is where the incident lifecycle is persisted between ticks,
	// so a restart does not re-open incidents that are still open. Empty
	// disables persistence.
	StateFile string `yaml:"stateFile"`

	// CoverageProbe is the default control query proving a target is visible
	// at all. Empty uses DefaultCoverageProbe; a target may override it.
	CoverageProbe string `yaml:"coverageProbe"`

	// AnomalyWatch and UnmatchedLog drive the unmatched-incident recorder:
	// generic signals baselined per target, with a fingerprint appended to
	// UnmatchedLog when one deviates while no catalog scenario is active.
	// Both must be set, and BaselineState too, for the recorder to run.
	AnomalyWatch []catalog.Query `yaml:"anomalyWatch"`
	UnmatchedLog string          `yaml:"unmatchedLog"`
}

// PrometheusAuth names the environment variables that hold the credentials,
// never the credentials themselves, so a config file can be committed and
// shared the way the GitHub token is kept out of it (SECURITY.md).
type PrometheusAuth struct {
	BearerTokenEnv string `yaml:"bearerTokenEnv"`
	UsernameEnv    string `yaml:"usernameEnv"`
	PasswordEnv    string `yaml:"passwordEnv"`
	// CAFile is a PEM file of extra CA certificates to trust.
	CAFile string `yaml:"caFile"`
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Check reports a malformed auth section. Its messages name the field and
// never repeat the value, in case a credential was pasted where a variable
// name belongs.
func (a *PrometheusAuth) Check() error {
	for _, f := range a.envFields() {
		if f.name != "" && !envName.MatchString(f.name) {
			return fmt.Errorf("prometheusAuth.%s must be the name of an environment variable (letters, digits and _), not the credential itself", label(f.field))
		}
	}
	basic := a.UsernameEnv != "" || a.PasswordEnv != ""
	switch {
	case a.BearerTokenEnv != "" && basic:
		return errors.New("prometheusAuth: bearerTokenEnv and usernameEnv/passwordEnv are alternatives; set one")
	case basic && (a.UsernameEnv == "" || a.PasswordEnv == ""):
		return errors.New("prometheusAuth: basic auth needs both usernameEnv and passwordEnv")
	case a.BearerTokenEnv == "" && !basic && a.CAFile == "":
		return errors.New("prometheusAuth is empty; remove it or set bearerTokenEnv, usernameEnv/passwordEnv or caFile")
	}
	return nil
}

// Resolve reads the named variables with getenv and returns the client
// settings. A named variable that is unset or empty is an error: sending a
// request without the credential the config asks for would only fail later,
// as a 401 nobody connects to the missing variable.
func (a *PrometheusAuth) Resolve(getenv func(string) string) (prom.Auth, error) {
	if a == nil {
		return prom.Auth{}, nil
	}
	if err := a.Check(); err != nil {
		return prom.Auth{}, err
	}
	out := prom.Auth{CAFile: a.CAFile}
	dst := map[string]*string{"bearerTokenEnv": &out.BearerToken, "usernameEnv": &out.Username, "passwordEnv": &out.Password}
	for _, f := range a.envFields() {
		if f.name == "" {
			continue
		}
		// The variable's name is left out too: a token can look like a name.
		if *dst[f.field] = getenv(f.name); *dst[f.field] == "" {
			return prom.Auth{}, fmt.Errorf("the environment variable named in prometheusAuth.%s is not set", label(f.field))
		}
	}
	return out, nil
}

// label names a field the way the user may have set it: the token also comes from a flag.
func label(field string) string {
	if field == "bearerTokenEnv" {
		return field + " (--prometheus-token-env)"
	}
	return field
}

func (a *PrometheusAuth) envFields() []struct{ field, name string } {
	return []struct{ field, name string }{
		{"bearerTokenEnv", a.BearerTokenEnv},
		{"usernameEnv", a.UsernameEnv},
		{"passwordEnv", a.PasswordEnv},
	}
}

// GitOps configures where remediation pull requests go. Absent means watch
// only prints incident reports.
type GitOps struct {
	Repo string `yaml:"repo"` // owner/repo of the config repository
	Base string `yaml:"base"` // base branch; empty means the repo default
	Path string `yaml:"path"` // patch file path template inside the repo
}

const defaultInterval = 30 * time.Second

// UnknownScenarios returns each target's scenario IDs that are absent from
// the catalog, keyed by target index. An empty scenario list selects the
// entire catalog and is valid.
func UnknownScenarios(targets []Target, scenarios []catalog.Scenario) map[int][]string {
	known := make(map[string]bool, len(scenarios))
	for _, scenario := range scenarios {
		known[scenario.ID] = true
	}
	unknown := make(map[int][]string)
	for i, target := range targets {
		for _, id := range target.Scenarios {
			if !known[id] {
				unknown[i] = append(unknown[i], id)
			}
		}
	}
	return unknown
}

// LoadConfig reads and validates a watch config file.
func LoadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	// Strict decoding: a mistyped key must fail the load instead of being
	// silently ignored (a typo like `statefile` for `stateFile` would disable
	// persistence and make a restart re-open settled incidents).
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.Prometheus == "" {
		return c, fmt.Errorf("%s: prometheus URL is required", path)
	}
	if len(c.Targets) == 0 {
		return c, fmt.Errorf("%s: at least one target is required", path)
	}
	for i, t := range c.Targets {
		if len(t.Params) == 0 {
			return c, fmt.Errorf("%s: target %d has no params", path, i)
		}
	}
	if c.Interval != "" {
		if _, err := time.ParseDuration(c.Interval); err != nil {
			return c, fmt.Errorf("%s: interval: %w", path, err)
		}
	}
	if c.PrometheusAuth != nil {
		if err := c.PrometheusAuth.Check(); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	if c.GitOps != nil {
		if !strings.Contains(c.GitOps.Repo, "/") {
			return c, fmt.Errorf("%s: gitops.repo must be owner/repo, got %q", path, c.GitOps.Repo)
		}
		if c.GitOps.Path == "" {
			c.GitOps.Path = "meshmedic/{{.namespace}}/{{.scenario}}.yaml"
		}
	}
	return c, nil
}

// ConfigFromFlags builds a watch config without a file, from a Prometheus URL
// and one or more `key=value,key=value` target strings. It exists so that
// pointing MeshMedic at a Prometheus you already run takes one command and no
// YAML: the difference between reading the README and running the tool.
//
// Only the report-printing path is reachable this way. Opening pull requests
// needs a config file, deliberately: writing to a repository is not something
// to enable from a flag someone pasted out of a README.
func ConfigFromFlags(prometheus, interval string, targets []string) (Config, error) {
	var c Config
	if prometheus == "" {
		return c, fmt.Errorf("--prometheus is required")
	}
	if len(targets) == 0 {
		return c, fmt.Errorf("at least one --target is required, e.g. --target service=payments,namespace=demo")
	}
	c.Prometheus = prometheus
	c.Interval = interval
	if interval != "" {
		if _, err := time.ParseDuration(interval); err != nil {
			return c, fmt.Errorf("--interval: %w", err)
		}
	}
	for _, spec := range targets {
		params, err := ParseTargetSpec(spec)
		if err != nil {
			return c, err
		}
		c.Targets = append(c.Targets, Target{Params: params})
	}
	return c, nil
}

// ParseTargetSpec turns `service=payments,namespace=demo` into template
// parameters. An empty key, a missing `=`, or a duplicate key is an error
// rather than a silent surprise during an incident.
func ParseTargetSpec(spec string) (map[string]string, error) {
	params := map[string]string{}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("bad --target %q: want key=value pairs separated by commas", spec)
		}
		if _, dup := params[k]; dup {
			return nil, fmt.Errorf("bad --target %q: key %q appears twice", spec, k)
		}
		params[k] = strings.TrimSpace(v)
	}
	if len(params) == 0 {
		return nil, fmt.Errorf("bad --target %q: no key=value pairs", spec)
	}
	return params, nil
}

// IntervalDuration returns the evaluation interval, defaulting to 30s.
func (c Config) IntervalDuration() time.Duration {
	if c.Interval == "" {
		return defaultInterval
	}
	// Parse errors are impossible here: LoadConfig rejects them.
	d, _ := time.ParseDuration(c.Interval)
	return d
}
