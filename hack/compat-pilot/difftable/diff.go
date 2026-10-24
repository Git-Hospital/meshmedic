package main

import (
	"fmt"
	"sort"
	"strings"
)

// Record is one run of one chart in one world, as observe.sh writes it. Only
// the fields the comparison needs are decoded; the rest stays in the file as
// evidence.
type Record struct {
	World        string            `json:"world"`
	Chart        string            `json:"chart"`
	ChartVersion string            `json:"chartVersion"`
	Run          int               `json:"run"`
	Environment  map[string]string `json:"environment"`
	Obs          map[string]string `json:"obs"`
	Analyze      []struct {
		Code string `json:"code"`
	} `json:"analyze"`
}

// absent is the value of a key a run did not produce. A hook Job that W0
// deletes on success and W3 leaves hanging must compare as a difference, so a
// missing key is a value, not a reason to skip.
const absent = "absent"

// Difference is one observation whose value in a mesh world is not the value
// W0 produced.
type Difference struct {
	Key    string
	Oracle string   // the value every W0 run agreed on
	Seen   []string // the value in each run of the mesh world, in run order
	Misses int      // runs whose value differs from Oracle
}

// Consistent reports whether every run of the mesh world differed. Only a
// consistent difference is a finding; one that comes and goes is reported,
// but as flaky, because three runs that disagree with each other are not
// evidence of anything about the chart.
func (d Difference) Consistent() bool { return d.Misses == len(d.Seen) }

// Comparison is one chart in one mesh world, against W0.
type Comparison struct {
	Chart, World string
	Runs         int
	Findings     []Difference // consistent differences
	Flaky        []Difference
	Analyze      []string // istioctl analyze codes seen in any run
	Undecided    string   // why no verdict was possible, if none was
}

// Verdict is the one-cell summary for the table.
func (c Comparison) Verdict() string {
	switch {
	case c.Undecided != "":
		return "undecided"
	case len(c.Findings) > 0:
		return fmt.Sprintf("differs (%d)", len(c.Findings))
	case len(c.Flaky) > 0:
		return fmt.Sprintf("flaky (%d)", len(c.Flaky))
	default:
		return "same"
	}
}

// Oracle is what W0 says about a chart: the value of every key that all W0
// runs agreed on, and the keys they did not agree on.
type Oracle struct {
	Values   map[string]string
	Unstable []string
	Runs     int
}

// ignored reports whether a key carries no information in this comparison.
// An HTTP probe of a port that W0 did not answer over HTTP measures the probe:
// a sniffing sidecar turns the request into a 502 on a MySQL port, and that
// says nothing about MySQL.
func ignored(key, oracle string) bool {
	return strings.HasSuffix(key, ":http") && oracle == "none"
}

func value(r Record, key string) string {
	if v, ok := r.Obs[key]; ok {
		return v
	}
	return absent
}

// keys is the union of observation keys over a set of runs, sorted so the
// report is stable.
func keys(runs ...[]Record) []string {
	set := map[string]bool{}
	for _, rs := range runs {
		for _, r := range rs {
			for k := range r.Obs {
				set[k] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// BuildOracle keeps only what W0 agreed on with itself. A chart that is flaky
// without a mesh cannot be blamed on the mesh for being flaky with one.
func BuildOracle(w0 []Record, universe []string) Oracle {
	o := Oracle{Values: map[string]string{}, Runs: len(w0)}
	for _, k := range universe {
		first := value(w0[0], k)
		stable := true
		for _, r := range w0[1:] {
			if value(r, k) != first {
				stable = false
				break
			}
		}
		if stable {
			o.Values[k] = first
		} else {
			o.Unstable = append(o.Unstable, k)
		}
	}
	return o
}

// Compare holds one mesh world against W0. minRuns is the bar for a verdict:
// fewer runs than that on either side means the comparison is undecided, not
// that the chart is compatible.
func Compare(chart, world string, w0, wn []Record, minRuns int) (Comparison, Oracle) {
	c := Comparison{Chart: chart, World: world, Runs: len(wn)}
	if len(w0) < minRuns {
		c.Undecided = fmt.Sprintf("W0 has %d of %d runs", len(w0), minRuns)
		return c, Oracle{}
	}
	if len(wn) < minRuns {
		c.Undecided = fmt.Sprintf("%s has %d of %d runs", world, len(wn), minRuns)
		return c, Oracle{}
	}
	sort.Slice(wn, func(i, j int) bool { return wn[i].Run < wn[j].Run })

	universe := keys(w0, wn)
	oracle := BuildOracle(w0, universe)
	for _, k := range universe {
		want, ok := oracle.Values[k]
		if !ok || ignored(k, want) {
			continue
		}
		d := Difference{Key: k, Oracle: want}
		for _, r := range wn {
			got := value(r, k)
			d.Seen = append(d.Seen, got)
			if got != want {
				d.Misses++
			}
		}
		switch {
		case d.Misses == 0:
		case d.Consistent():
			c.Findings = append(c.Findings, d)
		default:
			c.Flaky = append(c.Flaky, d)
		}
	}

	codes := map[string]bool{}
	for _, r := range wn {
		for _, a := range r.Analyze {
			codes[a.Code] = true
		}
	}
	for code := range codes {
		c.Analyze = append(c.Analyze, code)
	}
	sort.Strings(c.Analyze)
	return c, oracle
}
