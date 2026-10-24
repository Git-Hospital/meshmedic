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
//	go run ./hack/compat-pilot/difftable [-runs 3] RESULTS_DIR
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

var meshWorlds = []string{"W1", "W2", "W3"}

func main() {
	minRuns := flag.Int("runs", 3, "runs per world needed for a verdict")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: difftable [-runs 3] RESULTS_DIR")
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
	for c := range byChart {
		charts = append(charts, c)
	}
	sort.Strings(charts)

	results := map[string][]Comparison{}
	oracles := map[string]Oracle{}
	for _, chart := range charts {
		for _, world := range meshWorlds {
			c, o := Compare(chart, world, byChart[chart]["W0"], byChart[chart][world], minRuns)
			results[chart] = append(results[chart], c)
			if o.Values != nil {
				oracles[chart] = o
			}
		}
	}

	p := func(format string, a ...any) { fmt.Fprintf(w, format, a...) }
	p("# Compat pilot\n\n")
	p("A finding is an observation that held in every W0 run and differed in every run of a mesh world (%d runs each).\n\n", minRuns)
	p("| chart | version | W1 sidecar+init | W2 sidecar+cni | W3 ambient |\n|---|---|---|---|---|\n")
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
			byChart[chart]["W0"], byChart[base]["W0"], minRuns))
	}
	if len(certs) > 0 {
		p("\n## Certificates\n\n")
		p("A fix is certified when every mesh world behaves like its W0, 3 of 3 with nothing flaky, and its W0 behaves like the base chart's W0.\n\n")
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
			p("Unstable in W0, so not compared: %s\n\n", code(o.Unstable))
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
				p("- `%s`: W0 `%s`, %s `%s` (%d/%d)\n", d.Key, d.Oracle, c.World, d.Seen[0], d.Misses, len(d.Seen))
			}
			for _, d := range c.Flaky {
				p("- flaky `%s`: W0 `%s`, %s %s (%d/%d)\n", d.Key, d.Oracle, c.World, code(d.Seen), d.Misses, len(d.Seen))
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
	for _, world := range append([]string{"W0"}, meshWorlds...) {
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
