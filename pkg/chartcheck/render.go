package chartcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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
}

// Argv returns the arguments for the helm binary. No shell is involved: the
// vector is handed to the process as it is, so a value with spaces, quotes or
// shell syntax in it is one argument.
func (a RenderArgs) Argv() []string {
	argv := []string{"template", a.ReleaseName, a.Chart, "--namespace", a.Namespace}
	if a.KubeVersion != "" {
		argv = append(argv, "--kube-version", a.KubeVersion)
	}
	for _, f := range a.ValuesFiles {
		argv = append(argv, "--values", f)
	}
	for _, v := range a.Set {
		argv = append(argv, "--set", v)
	}
	for _, v := range a.SetString {
		argv = append(argv, "--set-string", v)
	}
	for _, v := range a.SetJSON {
		argv = append(argv, "--set-json", v)
	}
	// CRDs under crds/ are not rendered unless asked for; their conversion
	// webhooks are one of the things MM-CHART-007 reads.
	return append(argv, "--include-crds")
}

// Render runs helm template and returns the rendered manifests. The helm
// binary is found through PATH; if there is none the error wraps
// ErrHelmMissing.
func Render(ctx context.Context, a RenderArgs) ([]byte, error) {
	path, err := exec.LookPath("helm")
	if err != nil {
		return nil, ErrHelmMissing
	}
	cmd := exec.CommandContext(ctx, path, a.Argv()...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("helm template failed: %s", msg)
	}
	return out.Bytes(), nil
}
