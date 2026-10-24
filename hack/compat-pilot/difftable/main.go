// Command difftable turns the records observe.sh writes into the pilot's
// table: for every chart, does each mesh world behave the way W0 does, and if
// not, which observation differs, in how many runs.
//
// It is pilot tooling, not the engine ADR 0001 proposes. It lives here so the
// pilot's verdicts come from code that has tests rather than from someone
// reading JSON, and so the engine can start from something measured.
//
// Usage:
//
//	go run ./hack/compat-pilot/difftable [-runs 3] [-oracle W0] [-worlds W1,W2,W3] RESULTS_DIR
//
// The first pilot holds every mesh world against W0. The second tests Istio
// features that do not exist without a mesh, so it holds the ambient worlds
// against W1, sidecar mode, where those features are documented to work.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// oracleWorld and meshWorlds are set once from the flags, before anything is
// compared.
var (
	oracleWorld = "W0"
	meshWorlds  = []string{"W1", "W2", "W3"}
)

var worldLabels = map[string]string{
	"W0": "no mesh",
	"W1": "sidecar+init",
	"W2": "sidecar+cni",
	"W3": "ambient",
	"W4": "ambient+waypoint",
}

func main() {
	minRuns := flag.Int("runs", 3, "runs per world needed for a verdict")
	oracle := flag.String("oracle", "W0", "the world every other world is held against")
	worlds := flag.String("worlds", "W1,W2,W3", "the worlds compared with the oracle, comma separated")
	flag.Parse()
	oracleWorld, meshWorlds = *oracle, strings.Split(*worlds, ",")
	if flag.NArg() != 1 || *minRuns < 1 || !validWorlds(oracleWorld, meshWorlds) {
		fmt.Fprintln(os.Stderr, "usage: difftable [-runs 3] [-oracle W0] [-worlds W1,W2,W3] RESULTS_DIR")
		os.Exit(2)
	}
	records, err := load(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if len(records) == 0 {
		fmt.Fprintln(os.Stderr, "no records under", flag.Arg(0))
		os.Exit(3)
	}
	if err := render(os.Stdout, records, *minRuns); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// validWorlds refuses an unknown world and an oracle compared with itself.
func validWorlds(oracle string, worlds []string) bool {
	if _, ok := worldLabels[oracle]; !ok {
		return false
	}
	for _, w := range worlds {
		if _, ok := worldLabels[w]; !ok || w == oracle {
			return false
		}
	}
	return len(worlds) > 0
}

func load(dir string) ([]Record, error) {
	var out []Record
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var r Record
		if err := json.Unmarshal(b, &r); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if r.Chart == "" || r.World == "" {
			return fmt.Errorf("%s: not an observe.sh record", path)
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

func render(w io.Writer, records []Record, minRuns int) error {
	byChart := map[string]map[string][]Record{}
	for _, r := range records {
		if byChart[r.Chart] == nil {
			byChart[r.Chart] = map[string][]Record{}
		}
		byChart[r.Chart][r.World] = append(byChart[r.Chart][r.World], r)
	}
	charts := make([]string, 0, len(byChart))
	for c, worlds := range byChart {
		charts = append(charts, c)
		// One chart, one version, one base: records merged from runs of
		// different pins would be compared as if they were the same chart.
		var first *Record
		for _, rs := range worlds {
			for i := range rs {
				if first == nil {
					first = &rs[i]
				} else if rs[i].ChartVersion != first.ChartVersion || rs[i].FixOf != first.FixOf {
					return fmt.Errorf("%s: records disagree on version or fixOf (%s %q, %s %q)",
						c, first.ChartVersion, first.FixOf, rs[i].ChartVersion, rs[i].FixOf)
				}
			}
		}
	}
	sort.Strings(charts)

	results := map[string][]Comparison{}
	oracles := map[string]Oracle{}
	for _, chart := range charts {
		for _, world := range meshWorlds {
			c, o := Compare(chart, world, byChart[chart][oracleWorld], byChart[chart][world], minRuns)
			results[chart] = append(results[chart], c)
			if o.Values != nil {
				oracles[chart] = o
			}
		}
	}

	p := func(format string, a ...any) { fmt.Fprintf(w, format, a...) }
	p("# Compat pilot\n\n")
	p("A finding is an observation that held in every %s run and differed in every run of another world (%d runs each).\n\n", oracleWorld, minRuns)
	p("| chart | version |")
	for _, world := range meshWorlds {
		p(" %s %s |", world, worldLabels[world])
	}
	p("\n|---|---|%s\n", strings.Repeat("---|", len(meshWorlds)))
	for _, chart := range charts {
		version := ""
		for _, rs := range byChart[chart] {
			if len(rs) > 0 {
				version = rs[0].ChartVersion
				break
			}
		}
		cells := make([]string, 0, len(meshWorlds))
		for _, c := range results[chart] {
			cells = append(cells, c.Verdict())
		}
		p("| %s | %s | %s |\n", chart, version, strings.Join(cells, " | "))
	}

	var certs []Certificate
	for _, chart := range charts {
		base := ""
		for _, rs := range byChart[chart] {
			if len(rs) > 0 {
				base = rs[0].FixOf
				break
			}
		}
		if base == "" {
			continue
		}
		byWorld := func(chart string) map[string]Comparison {
			m := map[string]Comparison{}
			for _, c := range results[chart] {
				m[c.World] = c
			}
			return m
		}
		certs = append(certs, Certify(chart, base, byWorld(chart), byWorld(base),
			byChart[chart][oracleWorld], byChart[base][oracleWorld], minRuns))
	}
	if len(certs) > 0 {
		p("\n## Certificates\n\n")
		p("A fix is certified when every world behaves like its %[1]s, 3 of 3 with nothing flaky, and its %[1]s behaves like the base chart's %[1]s.\n\n", oracleWorld)
		p("| fix | for | clears | verdict |\n|---|---|---|---|\n")
		for _, c := range certs {
			verdict := "certified"
			if !c.Certified() {
				verdict = "not certified: " + strings.Join(c.Problems, "; ")
			}
			p("| %s | %s | %s | %s |\n", c.Fix, c.Base, strings.Join(c.Cleared, ", "), verdict)
		}
	}

	for _, chart := range charts {
		p("\n## %s\n\n", chart)
		if o, ok := oracles[chart]; ok && len(o.Unstable) > 0 {
			p("Unstable in %s, so not compared: %s\n\n", oracleWorld, code(o.Unstable))
		}
		for _, c := range results[chart] {
			p("### %s: %s\n\n", c.World, c.Verdict())
			for _, d := range c.Dropped {
				p("- left out: %s\n", d)
			}
			if len(c.Dropped) > 0 {
				p("\n")
			}
			if c.Undecided != "" {
				p("%s.\n\n", c.Undecided)
				continue
			}
			for _, d := range c.Findings {
				p("- `%s`: %s `%s`, %s `%s` (%d/%d)\n", d.Key, oracleWorld, d.Oracle, c.World, d.Seen[0], d.Misses, len(d.Seen))
			}
			for _, d := range c.Flaky {
				p("- flaky `%s`: %s `%s`, %s %s (%d/%d)\n", d.Key, oracleWorld, d.Oracle, c.World, code(d.Seen), d.Misses, len(d.Seen))
			}
			if len(c.Analyze) > 0 {
				p("- istioctl analyze: %s\n", code(c.Analyze))
			}
			p("\n")
		}
		if env := environment(byChart[chart]); env != "" {
			p("Environment: %s\n", env)
		}
	}
	return nil
}

func code(xs []string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "`" + x + "`"
	}
	return strings.Join(out, ", ")
}

// environment lists the versions each world ran with, so a row in the table
// cannot be quoted without them.
func environment(worlds map[string][]Record) string {
	var parts []string
	for _, world := range append([]string{oracleWorld}, meshWorlds...) {
		rs := worlds[world]
		if len(rs) == 0 {
			continue
		}
		e := rs[0].Environment
		s := fmt.Sprintf("%s kubernetes %s, istio %s", world, e["kubernetes"], e["istio"])
		if ns := e["native_sidecars"]; ns != "" {
			s += ", native sidecars " + ns
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}
