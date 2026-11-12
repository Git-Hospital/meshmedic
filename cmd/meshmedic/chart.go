package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Git-Hospital/meshmedic/pkg/chartcheck"
)

// Exit codes of `meshmedic chart check`, the same three the other commands
// use: 0 is the good answer, 1 is the finding, 2 is "could not run".
const (
	chartExitClean      = 0
	chartExitCandidates = 1
	chartExitError      = 2
)

const chartUsage = `usage:
  meshmedic chart check CHART [--values FILE] [--set k=v] [--namespace NS] ...   (CHART: a directory or .tgz; needs helm)
  meshmedic chart check --manifests FILE|-                                       (already rendered YAML; no helm needed)

flags:
  --mesh sidecar|ambient|both   which Istio data-plane mode to check (default both)
  --format text|json            report format (default text)
  --verbose                     text format: add evidence, the checks that ran and the full limits
  -f, --values FILE             helm values file (repeatable)
  --set k=v, --set-string k=v, --set-json k=v   helm value overrides (repeatable)
  --namespace NS                release namespace (default "default")
  --release-name NAME           helm release name (default "release")
  --kube-version VERSION        Kubernetes version helm renders for
  --skip-schema-validation      pass --skip-schema-validation to helm (a chart whose schema rejects its defaults)
  --timeout DURATION            how long helm template may run (default 5m)
  --native-sidecars=BOOL        assume sidecar proxies are native sidecars (default true;
                                use false for clusters older than Kubernetes 1.33)

exit codes: 0 no candidates (notes do not count), 1 at least one candidate,
            2 usage or render error, or no candidates but part of the input could not be read
            or had a duplicate key (incomplete)`

func runChart(args []string) {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(os.Stderr, chartUsage)
		os.Exit(chartExitError)
	}
	os.Exit(chartCheck(args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// chartCheck is runChart without the process: it returns the exit code, which
// is what the tests assert.
func chartCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "meshmedic chart check: "+format+"\n", a...)
		return chartExitError
	}

	fs := flag.NewFlagSet("chart check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, chartUsage) }
	manifests := fs.String("manifests", "", "rendered manifests: a file, or - for stdin")
	mesh := fs.String("mesh", "both", "sidecar, ambient or both")
	format := fs.String("format", "text", "text or json")
	namespace := fs.String("namespace", "default", "release namespace")
	release := fs.String("release-name", "release", "helm release name")
	kubeVersion := fs.String("kube-version", "", "Kubernetes version for helm template")
	verbose := fs.Bool("verbose", false, "text format: add evidence, the checks that ran and the full limits")
	skipSchema := fs.Bool("skip-schema-validation", false, "pass --skip-schema-validation to helm")
	timeout := fs.Duration("timeout", 5*time.Minute, "how long helm template may run")
	native := fs.Bool("native-sidecars", true, "assume sidecar proxies are native sidecars")
	var values, sets, setStrings, setJSONs multiFlag
	fs.Var(&values, "values", "helm values file (repeatable)")
	fs.Var(&values, "f", "shorthand for --values")
	fs.Var(&sets, "set", "helm --set (repeatable)")
	fs.Var(&setStrings, "set-string", "helm --set-string (repeatable)")
	fs.Var(&setJSONs, "set-json", "helm --set-json (repeatable)")

	// Flags may come before or after the chart argument.
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return chartExitClean
			}
			return chartExitError
		}
		if fs.NArg() == 0 {
			break
		}
		// After "--" everything is a positional argument, flag-like or not.
		if n := fs.NArg(); len(rest) > n && rest[len(rest)-n-1] == "--" {
			positional = append(positional, fs.Args()...)
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	var modes []chartcheck.Mode
	switch *mesh {
	case "both":
		modes = []chartcheck.Mode{chartcheck.Sidecar, chartcheck.Ambient}
	case "sidecar":
		modes = []chartcheck.Mode{chartcheck.Sidecar}
	case "ambient":
		modes = []chartcheck.Mode{chartcheck.Ambient}
	default:
		return fail("--mesh must be sidecar, ambient or both, not %q", *mesh)
	}
	if *format != "text" && *format != "json" {
		return fail("--format must be text or json, not %q", *format)
	}
	if len(positional) > 1 {
		return fail("one chart at a time, got %d: %s", len(positional), strings.Join(positional, " "))
	}

	var (
		data  []byte
		input string
		err   error
	)
	switch {
	case *manifests != "" && len(positional) == 1:
		return fail("give a chart or --manifests, not both")
	case *manifests != "":
		if len(values)+len(sets)+len(setStrings)+len(setJSONs) > 0 || *release != "release" || *kubeVersion != "" || *skipSchema {
			return fail("--values, --set, --set-string, --set-json, --release-name, --kube-version and --skip-schema-validation configure helm and have no effect with --manifests")
		}
		if *manifests == "-" {
			input = "manifests (stdin)"
			data, err = io.ReadAll(stdin)
		} else {
			input = "manifests " + *manifests
			data, err = os.ReadFile(*manifests)
		}
		if err != nil {
			return fail("reading the manifests: %v", err)
		}
	case len(positional) == 1:
		input = "chart " + positional[0]
		ra := chartcheck.RenderArgs{
			Chart:                positional[0],
			ValuesFiles:          values,
			Set:                  sets,
			SetString:            setStrings,
			SetJSON:              setJSONs,
			Namespace:            *namespace,
			ReleaseName:          *release,
			KubeVersion:          *kubeVersion,
			SkipSchemaValidation: *skipSchema,
		}
		if err := ra.Validate(); err != nil {
			return fail("%v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		data, err = chartcheck.Render(ctx, ra)
		if errors.Is(err, chartcheck.ErrHelmTimeout) {
			return fail("helm template timed out after %s; raise the limit with --timeout, or render the chart yourself and pass it with --manifests", *timeout)
		}
		if errors.Is(err, chartcheck.ErrHelmMissing) {
			return fail("helm was not found in PATH, so the chart cannot be rendered. Install helm, or render the chart yourself and pass the output with --manifests FILE (or --manifests - to read stdin), for example: helm template my-release ./chart | meshmedic chart check --manifests -")
		}
		if err != nil {
			return fail("%v", err)
		}
	default:
		fmt.Fprintln(stderr, chartUsage)
		return chartExitError
	}

	m, err := chartcheck.Parse(strings.NewReader(string(data)), *namespace)
	if err != nil {
		return fail("reading the rendered manifests: %v", err)
	}
	rep, err := chartcheck.Analyze(m, chartcheck.Options{Modes: modes, NativeSidecars: *native})
	if err != nil {
		return fail("%v", err)
	}
	rep.Input = input

	if *format == "json" {
		err = chartcheck.WriteJSON(stdout, rep)
	} else {
		err = chartcheck.WriteText(stdout, rep, *verbose)
	}
	if err != nil {
		return fail("writing the report: %v", err)
	}
	if rep.Candidates > 0 {
		return chartExitCandidates
	}
	if rep.Incomplete {
		// Some of the input could not be read: that is never a clean result.
		fmt.Fprintf(stderr, "meshmedic chart check: incomplete: %d document(s) could not be read and %d had a duplicate key, and no candidate was found in the rest\n", len(rep.Unread), len(rep.DuplicateKeys))
		return chartExitError
	}
	return chartExitClean
}
