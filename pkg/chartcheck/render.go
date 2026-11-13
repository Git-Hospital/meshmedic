package chartcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ErrHelmMissing is returned by Render when no helm binary is on the PATH.
var ErrHelmMissing = errors.New("helm was not found in PATH")

// RenderArgs is what is passed through to helm template.
type RenderArgs struct {
	// Chart is a chart directory or a .tgz archive.
	Chart       string
	ValuesFiles []string
	Set         []string
	SetString   []string
	SetJSON     []string
	Namespace   string
	ReleaseName string
	KubeVersion string
	// SkipSchemaValidation passes --skip-schema-validation, for a chart whose
	// own values schema rejects its defaults (Istio's gateway chart 1.24.0).
	SkipSchemaValidation bool
}

var (
	// A Kubernetes namespace is a DNS label.
	namespaceRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// Helm's release name: lowercase alphanumerics, '-' and '.', starting and
	// ending with an alphanumeric, at most 53 characters.
	releaseRE     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	kubeVersionRE = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){0,2}([-+][0-9A-Za-z.+-]*)?$`)
)

// Validate refuses what helm would read as something other than what it is,
// before helm is started. The values that cannot be constrained by shape (a
// file path, a --set string) are not checked here: Argv attaches each to its
// own flag with "=", where it cannot be read as another flag.
func (a RenderArgs) Validate() error {
	switch {
	case a.Chart == "":
		return errors.New("no chart given")
	case strings.HasPrefix(a.Chart, "-"):
		return fmt.Errorf("the chart %q would be read as a flag; names that start with \"-\" are refused (write a path as ./%s)", a.Chart, a.Chart)
	case len(a.ReleaseName) > 53 || !releaseRE.MatchString(a.ReleaseName):
		return fmt.Errorf("the release name %q is not valid: use at most 53 lowercase letters, digits, '-' and '.', starting and ending with a letter or digit", a.ReleaseName)
	case len(a.Namespace) > 63 || !namespaceRE.MatchString(a.Namespace):
		return fmt.Errorf("the namespace %q is not valid: use at most 63 lowercase letters, digits and '-', starting and ending with a letter or digit", a.Namespace)
	case a.KubeVersion != "" && !kubeVersionRE.MatchString(a.KubeVersion):
		return fmt.Errorf("the kube-version %q is not valid: use a version such as v1.33.0", a.KubeVersion)
	}
	return nil
}

// Argv returns the arguments for the helm binary. No shell is involved: the
// vector is handed to the process as it is. Every option and its value are
// one element joined by "=", so a value that starts with "-" cannot be read as
// a flag, and the release name and chart come after "--", so nothing in them
// can either.
func (a RenderArgs) Argv() []string {
	argv := []string{"template", "--namespace=" + a.Namespace}
	if a.KubeVersion != "" {
		argv = append(argv, "--kube-version="+a.KubeVersion)
	}
	for _, f := range a.ValuesFiles {
		argv = append(argv, "--values="+f)
	}
	for _, v := range a.Set {
		argv = append(argv, "--set="+v)
	}
	for _, v := range a.SetString {
		argv = append(argv, "--set-string="+v)
	}
	for _, v := range a.SetJSON {
		argv = append(argv, "--set-json="+v)
	}
	if a.SkipSchemaValidation {
		argv = append(argv, "--skip-schema-validation")
	}
	// CRDs under crds/ are not rendered unless asked for; their conversion
	// webhooks are one of the things MM-CHART-007 reads.
	return append(argv, "--include-crds", "--", a.ReleaseName, a.Chart)
}

// ErrHelmTimeout is returned by Render when helm did not finish in time.
var ErrHelmTimeout = errors.New("helm template timed out")

// Render runs helm template and returns the rendered manifests. The helm
// binary is found through PATH; if there is none the error wraps
// ErrHelmMissing. The arguments are validated first, and ctx bounds the run.
func Render(ctx context.Context, a RenderArgs) ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	path, err := exec.LookPath("helm")
	if err != nil {
		return nil, ErrHelmMissing
	}
	cmd := exec.CommandContext(ctx, path, a.Argv()...)
	// Do not wait forever for output pipes held by a child of a killed helm.
	cmd.WaitDelay = 2 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrHelmTimeout
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("helm template failed: %s", msg)
	}
	return out.Bytes(), nil
}
