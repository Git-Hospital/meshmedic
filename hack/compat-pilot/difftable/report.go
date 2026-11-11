package main

import (
	"encoding/json"
	"io"
	"sort"
	"strings"
)

// reportSchema is the version of the JSON form, documented in
// hack/compat-exp/DIFFTABLE-JSON.md. A change to a field name or meaning
// raises it.
const reportSchema = 1

// Report is the machine-readable form of everything the table shows. Every
// list is sorted and no map is used, so one input gives one byte sequence.
type Report struct {
	Schema       int           `json:"schema"`
	Oracle       string        `json:"oracle"`
	Worlds       []string      `json:"worlds"`
	RunsRequired int           `json:"runsRequired"`
	Charts       []ChartReport `json:"charts"`
}

// ChartReport is one chart, or one fix entry, across the compared worlds.
type ChartReport struct {
	Chart        string `json:"chart"`
	ChartVersion string `json:"chartVersion"`
	FixOf        string `json:"fixOf,omitempty"`
	Environment  string `json:"environment,omitempty"`
	// OracleInstall counts the oracle world's installs from obs["helm:install"]
	// of its records, so a "same" verdict over installs that failed everywhere
	// is visible.
	OracleInstall OracleInstall `json:"oracleInstall"`
	Worlds        []WorldReport `json:"worlds"`
	Certificate   *CertReport   `json:"certificate,omitempty"`
}

// WorldReport is one chart in one mesh world, held against the oracle.
type WorldReport struct {
	World string `json:"world"`
	// Verdict is same, differs, flaky or undecided.
	Verdict           string       `json:"verdict"`
	UndecidedReason   string       `json:"undecidedReason,omitempty"`
	Findings          int          `json:"findings"`
	FlakyKeys         int          `json:"flakyKeys"`
	RunsUsed          []int        `json:"runsUsed"`
	RunsLeftOut       []RunLeftOut `json:"runsLeftOut"`
	OracleRunsUsed    []int        `json:"oracleRunsUsed"`
	OracleRunsLeftOut []RunLeftOut `json:"oracleRunsLeftOut"`
	AnalyzeCodes      []string     `json:"analyzeCodes"`
	Keys              []KeyReport  `json:"keys"`
}

// RunLeftOut is a run that was not used, with the reason: pull-failure or
// capacity.
type RunLeftOut struct {
	Run    int    `json:"run"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// KeyReport is one observation key in one world.
type KeyReport struct {
	Key   string `json:"key"`
	Class string `json:"class"`
	// Verdict is same, differs, flaky, left out or undecided.
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
	// OracleValue is the value every oracle run agreed on; absent when they
	// did not agree, or when the world is undecided for lack of oracle runs.
	OracleValue *string  `json:"oracleValue,omitempty"`
	WorldValues []string `json:"worldValues"`
	Misses      int      `json:"misses"`
}

// CertReport is the certificate of a fix entry.
type CertReport struct {
	Fix       string   `json:"fix"`
	Base      string   `json:"base"`
	Certified bool     `json:"certified"`
	Cleared   []string `json:"cleared"`
	Problems  []string `json:"problems"`
	// CellWorlds are the worlds the fix had to clear (all compared worlds
	// unless -cell-worlds was given). Ignored lists every cell of the ignore
	// list that the certificate did not count, with its reason.
	CellWorlds []string     `json:"cellWorlds"`
	Ignored    []IgnoreCell `json:"ignored"`
	// WorldsNotCompared lists the worlds with no verdict that the ignore file
	// let the certificate pass (key "*"); they are not in Ignored.
	WorldsNotCompared []IgnoreCell `json:"worldsNotCompared"`
	// IgnoredChanged lists ignored cells whose value under the fix differs
	// from the base's ("ignored cell changed under the fix"); any entry fails
	// the certificate.
	IgnoredChanged []IgnoredChange `json:"ignoredChanged"`
	// IgnoredNotComparable lists ignored cells (also in Ignored) whose base
	// value is flaky or undecided: "ignored, not comparable (base flaky or
	// undecided)". They do not fail the certificate.
	IgnoredNotComparable []IgnoreCell `json:"ignoredNotComparable"`
}

func verdictWord(c Comparison) string {
	return strings.SplitN(c.Verdict(), " ", 2)[0]
}

func leftOut(ds []DroppedRun, world string) []RunLeftOut {
	out := []RunLeftOut{}
	for _, d := range ds {
		if d.World == world {
			out = append(out, RunLeftOut{Run: d.Run, Reason: d.Reason, Detail: d.Detail})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Run < out[j].Run })
	return out
}

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

func (a *analysis) report() Report {
	r := Report{Schema: reportSchema, Oracle: oracleWorld, Worlds: append([]string{}, meshWorlds...),
		RunsRequired: a.minRuns, Charts: []ChartReport{}}
	certs := map[string]Certificate{}
	for _, c := range a.certs {
		certs[c.Fix] = c
	}
	for _, chart := range a.charts {
		cr := ChartReport{Chart: chart, Worlds: []WorldReport{}, Environment: environment(a.byChart[chart]),
			OracleInstall: oracleInstall(a.byChart[chart][oracleWorld])}
		for _, rs := range a.byChart[chart] {
			if len(rs) > 0 {
				cr.ChartVersion, cr.FixOf = rs[0].ChartVersion, rs[0].FixOf
				break
			}
		}
		for _, c := range a.results[chart] {
			wr := WorldReport{
				World: c.World, Verdict: verdictWord(c), UndecidedReason: c.Undecided,
				Findings: len(c.Findings), FlakyKeys: len(c.Flaky),
				RunsUsed: nonNil(c.RunsUsed), RunsLeftOut: leftOut(c.DroppedRuns, c.World),
				OracleRunsUsed: nonNil(c.OracleRunsUsed), OracleRunsLeftOut: leftOut(c.DroppedRuns, oracleWorld),
				AnalyzeCodes: nonNil(c.Analyze), Keys: []KeyReport{},
			}
			for _, k := range c.Keys {
				wr.Keys = append(wr.Keys, KeyReport{Key: k.Key, Class: k.Class, Verdict: k.Verdict, Reason: k.Reason,
					OracleValue: k.Oracle, WorldValues: nonNil(k.Seen), Misses: k.Misses})
			}
			cr.Worlds = append(cr.Worlds, wr)
		}
		if c, ok := certs[chart]; ok {
			cr.Certificate = &CertReport{Fix: c.Fix, Base: c.Base, Certified: c.Certified(),
				Cleared: nonNil(c.Cleared), Problems: nonNil(c.Problems),
				CellWorlds: nonNil(c.CellWorlds), Ignored: nonNil(c.Ignored), WorldsNotCompared: nonNil(c.NotCompared), IgnoredChanged: nonNil(c.IgnoredChanged), IgnoredNotComparable: nonNil(c.IgnoredNotComparable)}
		}
		r.Charts = append(r.Charts, cr)
	}
	return r
}

// writeJSON writes the report, indented, ending in a newline. HTML escaping is
// off so a value reads the way the record has it.
func (a *analysis) writeJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(a.report())
}
