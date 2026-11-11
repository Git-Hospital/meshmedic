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
//	go run ./hack/compat-pilot/difftable [-runs 3] [-oracle W0] [-worlds W1,W2,W3] [-json] [-cell-worlds W3] [-ignore FILE] RESULTS_DIR
//
// The first pilot holds every mesh world against W0. The second tests Istio
// features that do not exist without a mesh, so it holds the ambient worlds
// against W1, sidecar mode, where those features are documented to work.
//
// By default a fix is certified only when no mesh world differs from its
// oracle. -cell-worlds limits that to the worlds of the cell being certified
// (the others are still checked for differences the base chart does not have)
// and -ignore names cells that remain and do not count; every ignored cell is
// printed with its reason, and an entry that matches no cell is an error.
//
// With -json the table is replaced by a machine-readable report, documented in
// hack/compat-exp/DIFFTABLE-JSON.md.
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
	asJSON := flag.Bool("json", false, "write the machine-readable report instead of the table")
	cell := flag.String("cell-worlds", "", "certify only these worlds (a subset of -worlds); the other worlds are checked for new differences")
	ignoreFile := flag.String("ignore", "", "a JSON list of {chart, world, key, reason} cells that certification does not count")
	flag.Parse()
	oracleWorld, meshWorlds = *oracle, strings.Split(*worlds, ",")
	if flag.NArg() != 1 || *minRuns < 1 || !validWorlds(oracleWorld, meshWorlds) {
		fmt.Fprintln(os.Stderr, "usage: difftable [-runs 3] [-oracle W0] [-worlds W1,W2,W3] [-json] [-cell-worlds W3] [-ignore FILE] RESULTS_DIR")
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
	opts := CertifyOpts{}
	if *cell != "" {
		opts.CellWorlds = strings.Split(*cell, ",")
		if !validCellWorlds(opts.CellWorlds, meshWorlds) {
			fmt.Fprintln(os.Stderr, "-cell-worlds must name worlds from -worlds, each once")
			os.Exit(2)
		}
	}
	if *ignoreFile != "" {
		if opts.Ignore, err = loadIgnore(*ignoreFile); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	a, err := analyze(records, *minRuns, opts)
	if err == nil && *asJSON {
		err = a.writeJSON(os.Stdout)
	} else if err == nil {
		err = a.table(os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// validWorlds refuses an unknown world, a world named twice and an oracle
// compared with itself.
func validWorlds(oracle string, worlds []string) bool {
	if _, ok := worldLabels[oracle]; !ok {
		return false
	}
	seen := map[string]bool{oracle: true}
	for _, w := range worlds {
		if _, ok := worldLabels[w]; !ok || seen[w] {
			return false
		}
		seen[w] = true
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

// analysis is everything the table and the JSON report are made from.
type analysis struct {
	byChart map[string]map[string][]Record
	charts  []string
	results map[string][]Comparison
	oracles map[string]Oracle
	certs   []Certificate
	minRuns int
	limited bool // -cell-worlds or -ignore was given
}

func render(w io.Writer, records []Record, minRuns int) error {
	a, err := analyze(records, minRuns, CertifyOpts{})
	if err != nil {
		return err
	}
	return a.table(w)
}

func renderJSON(w io.Writer, records []Record, minRuns int) error {
	a, err := analyze(records, minRuns, CertifyOpts{})
	if err != nil {
		return err
	}
	return a.writeJSON(w)
}

func analyze(records []Record, minRuns int, opts CertifyOpts) (*analysis, error) {
	if err := checkIgnore(opts.Ignore); err != nil {
		return nil, err
	}
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
					return nil, fmt.Errorf("%s: records disagree on version or fixOf (%s %q, %s %q)",
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
		certs = append(certs, CertifyWith(chart, base, byWorld(chart), byWorld(base),
			byChart[chart][oracleWorld], byChart[base][oracleWorld], minRuns, opts))
	}
	// A list that names a cell nothing differs on is out of date, and an out
	// of date list could hide a cell the next time it appears.
	used := map[IgnoreCell]bool{}
	for _, c := range certs {
		for _, e := range append(append([]IgnoreCell{}, c.Ignored...), c.NotCompared...) {
			used[e] = true
		}
	}
	fixOnly := map[IgnoreCell]bool{}
	for _, c := range certs {
		for _, e := range c.FixOnly {
			fixOnly[e] = true
		}
	}
	for _, e := range opts.Ignore {
		if !used[e] && fixOnly[e] {
			return nil, fmt.Errorf("ignore entry %s %s %q matches a cell that differs only with the fix; the ignore list may hold only cells of the base chart (rule 5)", e.Chart, e.World, e.Key)
		}
		if !used[e] {
			return nil, fmt.Errorf("ignore entry %s %s %q matches no cell of any fix entry (stale list?)", e.Chart, e.World, e.Key)
		}
	}
	return &analysis{byChart: byChart, charts: charts, results: results, oracles: oracles, certs: certs, minRuns: minRuns,
		limited: len(opts.CellWorlds) > 0 || len(opts.Ignore) > 0}, nil
}

func (a *analysis) table(w io.Writer) error {
	byChart, charts, results, oracles, certs, minRuns := a.byChart, a.charts, a.results, a.oracles, a.certs, a.minRuns
	p := func(format string, a ...any) { fmt.Fprintf(w, format, a...) }
	p("# Compat pilot\n\n")
	p("A finding is an observation that held in every %s run and differed in every run of another world (%d runs each).\n\n", oracleWorld, minRuns)
	p("| chart | version | %s installs |", oracleWorld)
	for _, world := range meshWorlds {
		p(" %s %s |", world, worldLabels[world])
	}
	p("\n|---|---|---|%s\n", strings.Repeat("---|", len(meshWorlds)))
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
		p("| %s | %s | %s | %s |\n", chart, version, oracleInstall(byChart[chart][oracleWorld]).cell(), strings.Join(cells, " | "))
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
		if a.limited {
			for _, c := range certs {
				p("\n`%s`: cell worlds: %s; the other worlds are checked for differences `%s` does not have.\n", c.Fix, strings.Join(c.CellWorlds, ", "), c.Base)
				if len(c.NotCompared) > 0 {
					p("\nWorlds NOT COMPARED for `%s` (no verdict, listed in the ignore file), so nothing was certified there:\n\n", c.Fix)
				}
				for _, e := range c.NotCompared {
					p("- `%s` did not compare %s (entries of %s): %s\n", c.Fix, e.World, e.Chart, e.Reason)
				}
				if len(c.IgnoredChanged) > 0 {
					p("\nIgnored cell changed under the fix, so `%s` is not certified:\n\n", c.Fix)
				}
				for _, ch := range c.IgnoredChanged {
					p("- ignored cell changed under the fix: %s `%s`, %s `%s`, `%s` `%s`\n", ch.World, ch.Key, c.Base, strings.Join(ch.BaseValues, "|"), c.Fix, strings.Join(ch.FixValues, "|"))
				}
				if len(c.Ignored) > 0 {
					p("\nIgnored cells of `%s`, not counted as remaining findings:\n\n", c.Fix)
				}
				for _, e := range c.Ignored {
					p("- `%s` ignores %s `%s` of %s: %s\n", c.Fix, e.World, e.Key, e.Chart, e.Reason)
				}
			}
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

// validCellWorlds wants a non-empty subset of the compared worlds, each once.
func validCellWorlds(cell, all []string) bool {
	in := map[string]bool{}
	for _, w := range all {
		in[w] = true
	}
	seen := map[string]bool{}
	for _, w := range cell {
		if !in[w] || seen[w] {
			return false
		}
		seen[w] = true
	}
	return len(cell) > 0
}

// loadIgnore reads the ignore list: a JSON array of cells, nothing else.
func loadIgnore(path string) ([]IgnoreCell, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var out []IgnoreCell
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%s: more than one JSON value", path)
	}
	return out, nil
}

// checkIgnore refuses an entry that cannot be read as a cell: a missing field,
// the oracle or an unknown world, or the same cell twice.
func checkIgnore(list []IgnoreCell) error {
	seen := map[[3]string]bool{}
	for _, e := range list {
		id := fmt.Sprintf("ignore entry %s %s %q", e.Chart, e.World, e.Key)
		switch {
		case e.Chart == "" || e.World == "" || e.Key == "":
			return fmt.Errorf("%s: chart, world and key are required", id)
		case e.Reason == "":
			return fmt.Errorf("%s: a reason is required", id)
		case !contains(meshWorlds, e.World):
			return fmt.Errorf("%s: %s is not one of the compared worlds %s", id, e.World, strings.Join(meshWorlds, ","))
		case seen[[3]string{e.Chart, e.World, e.Key}]:
			return fmt.Errorf("%s: listed twice", id)
		}
		seen[[3]string{e.Chart, e.World, e.Key}] = true
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
