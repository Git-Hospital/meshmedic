package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Git-Hospital/meshmedic/pkg/demo"
)

// runDemo is `meshmedic demo`: the real detection path against a replayed
// Prometheus, for a reader with no mesh. It returns the exit code instead of
// exiting so it can be tested.
//
// Exit codes follow the other commands: 0 when the replay ran, 1 when it could
// not (an unknown scenario name as in `render`, a catalog that does not load,
// an output directory that cannot be written), 2 for a flag the command does
// not understand. A blind scenario still exits 0: the demo worked, and what it
// shows is `check` refusing to call a blind target healthy, which is `check`'s
// exit 1, not the demo's.
func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("catalog", defaultCatalogDir(), "catalog directory; empty uses the catalog embedded in this binary (or $MESHMEDIC_CATALOG)")
	name := fs.String("scenario", demo.DefaultName, "which replay to run (see --list)")
	list := fs.Bool("list", false, "list the replays and exit")
	verbose := fs.Bool("verbose", false, "also show every PromQL query and the response the replay served")
	out := fs.String("out", "", "write the dossier files into this directory (nothing is written without it)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "demo takes no positional arguments, got %q\n", fs.Arg(0))
		return 2
	}

	if *list {
		if err := demo.List(stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}

	fx, err := demo.Load(*name)
	if err != nil {
		fmt.Fprintf(stderr, "%v, run `meshmedic demo --list` to list\n", err)
		return 1
	}
	scenarios, lock, lockPath, err := loadCatalog(*dir)
	if err != nil {
		fmt.Fprintln(stderr, "catalog invalid:", err)
		return 1
	}
	res, err := demo.Run(context.Background(), fx, scenarios, unlockedFrom(lock, scenarios, lockPath))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	var wrote []string
	if *out != "" {
		wrote, err = writeDossiers(*out, res)
		if err != nil {
			fmt.Fprintln(stderr, "writing --out:", err)
			return 1
		}
	}
	demo.Render(stdout, res, *verbose, wrote)
	return 0
}

// writeDossiers writes what the replay produced: for each incident the report
// and the rendered patch, which in GitOps mode are the pull request body and
// the committed file, and for each resolution its closing report. `watch`
// itself writes none of these locally.
func writeDossiers(dir string, res demo.Result) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var wrote []string
	write := func(name, content string) error {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
		wrote = append(wrote, p)
		return nil
	}
	for _, sr := range res.Steps {
		for i, inc := range sr.Incidents {
			id := inc.Scenario.ID
			if err := write(id+"-report.md", sr.Dossiers[i]); err != nil {
				return wrote, err
			}
			if err := write(id+"-patch.yaml", sr.Patches[i]); err != nil {
				return wrote, err
			}
		}
		for i, r := range sr.Resolutions {
			if err := write(r.Scenario.ID+"-resolved.md", sr.Resolved[i]+"\n"); err != nil {
				return wrote, err
			}
		}
	}
	return wrote, nil
}
