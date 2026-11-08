package promql

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// FuzzExtract feeds the tokenizer arbitrary queries. Catalog queries are rendered from templates
// and evidence and anomalyWatch queries come from operators, so a bad query must give an error,
// never a panic or a hang, and what Extract returns must keep its documented shape.
func FuzzExtract(f *testing.F) {
	for _, q := range catalogQueries(f) {
		f.Add(q.query)
	}
	for _, q := range []string{
		``, `{`, `}`, `"`, `'unterminated`, "`raw`", `[5m:]`, `:`, `{__name__="up"}`,
		`sum by (le) (rate(x_bucket{a="b"}[5m]))`, `a / on(b) group_left(c) d`, `x offset 5m`,
		`{a=~"b\\"c"}`, `histogram_quantile(0.99, sum by (le) (rate(x[1m])))`,
	} {
		f.Add(q)
	}
	f.Fuzz(func(t *testing.T, query string) {
		done := make(chan struct{})
		var refs Refs
		var err error
		go func() {
			defer close(done)
			refs, err = Extract(query)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Extract did not return for %q", query)
		}
		if err != nil {
			return
		}
		for name, list := range map[string][]string{"Metrics": refs.Metrics, "Labels": refs.Labels} {
			if !slices.IsSorted(list) || len(slices.Compact(slices.Clone(list))) != len(list) {
				t.Errorf("%s not sorted and unique: %q for %q", name, list, query)
			}
		}
		// A __name__ matcher can name any metric, including one called "sum".
		if !strings.Contains(query, "__name__") {
			for _, m := range refs.Metrics {
				if keywords[m] || aggregators[m] {
					t.Errorf("keyword or aggregator %q reported as a metric for %q", m, query)
				}
			}
		}
		for _, s := range refs.Selectors {
			if s.Metric != "" && !slices.Contains(refs.Metrics, s.Metric) {
				t.Errorf("selector metric %q not in Metrics %q for %q", s.Metric, refs.Metrics, query)
			}
		}
	})
}
