package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// RenderJSON is the deterministic JSON output: indented with two spaces, a
// trailing newline, no maps.
func RenderJSON(r *Result) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderMarkdown is the short human-readable summary.
func RenderMarkdown(r *Result) []byte {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("# crossrun\n\n")
	if r.Run2 != "" {
		w("Run 1 %s, run 2 %s, gap %.2f h (%s).\n\n", r.Run1, r.Run2, r.GapHours, okWord(r.GapOK, "at least 12 h", "UNDER 12 h"))
	} else {
		w("Run 1 %s only; no run 2 yet.\n\n", r.Run1)
	}
	for _, x := range r.Warnings {
		w("- WARNING: %s\n", x)
	}
	if len(r.Warnings) > 0 {
		w("\n")
	}

	w("## Decision\n\n")
	w("%s\n\n", r.Metrics)
	w("Region **%s**: %s (%s).\n\n", r.Region.Region, r.Region.Outcome, r.Region.Condition)
	w("| row | matched | failed clauses |\n|---|---|---|\n")
	for _, t := range r.Trace {
		w("| %s | %v | %s |\n", t.Region, t.Matched, strings.Join(t.Failed, ", "))
	}
	if !r.SComputed {
		w("\nS is not computed (no static check results); it is 0 here.\n")
	}
	w("\nIf every unresolved, unexplained or awaiting cell counted (%d cells): %s, region %s.\n\n",
		r.IfAllCounted.Cells, r.IfAllCounted.Metrics, r.IfAllCounted.Region.Region)

	w("## Futility (section 7)\n\n")
	f := r.Futility
	w("N_max %d, P_max %d (%d cells neither credited nor excluded, %d new publishers among them). Reachable regions: %s.\n",
		f.NMax, f.PMax, f.PendingCells, f.PendingPublishers, strings.Join(f.Reachable, ", "))
	if f.StopDrop {
		w("\n**N_max <= 1 or P_max <= 1: only region E is reachable. Stop, Drop.**\n")
	}
	if f.PrevalenceCertificatesOff {
		w("\nRegion A is unreachable: prevalence certificates are skipped.\n")
	}
	u := r.Undecided
	w("\nUndecided cells: %d of %d (%s the 15%% stop).\n\n", u.Undecided, u.Cells, okWord(!u.Over, "not over", "OVER"))

	w("## ambient-np prevalence\n\n%d counted cell(s) in %d chart(s): %s.\n\n", r.Prevalence.Cells, len(r.Prevalence.Charts), joinOrNone(r.Prevalence.Charts))

	w("## Cells\n\n")
	counts := map[string]int{}
	for _, c := range r.Cells {
		counts[c.Status]++
	}
	w("%d cells: counted %d, excluded %d, unexplained %d, unresolved %d, awaiting run 2 %d, not reproduced %d.\n\n",
		len(r.Cells), counts[StCounted], counts[StExcluded], counts[StUnexplained], counts[StUnresolved], counts[StAwaitRun2], counts[StNotReproduce])
	w("| chart | world | key | publisher | run 1 | run 2 | status | credit | type | exclusion |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range r.Cells {
		w("| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", c.Chart, c.World, c.Key, c.Publisher, c.Run1, c.Run2, c.Status, c.Credit, c.Type, strings.Join(c.Reasons, ", "))
	}

	w("\n## World-universal (rule 5)\n\n")
	if len(r.Universal) == 0 {
		w("No class or cause with a denominator.\n")
	} else {
		w("| kind | name | world | differing / having | universal |\n|---|---|---|---|---|\n")
		for _, x := range r.Universal {
			state := fmt.Sprintf("%v", x.Universal)
			if !x.Computed {
				state = "not computed"
			}
			w("| %s | %s | %s | %d / %d | %s |\n", x.Kind, x.Name, x.World, x.Numerator, x.Denominator, state)
		}
	}

	w("\n## C-optout evidence (podMesh)\n\n")
	w("Mechanised: the records show a non-client pod of the chart in that world with the world's opt-out metadata. Not mechanised: that every failing caller is such a pod; the ledger row asserts it.\n\n")
	if len(r.OptOut) == 0 {
		w("None.\n")
	}
	for _, e := range r.OptOut {
		w("- %s %s: %d pod(s) with effective opt-out, ledger asserts it: %v\n", e.Chart, e.World, e.Effective, e.Asserted)
		seen := map[string]int{}
		var order []string
		for _, p := range e.Pods {
			if p.Effective {
				line := fmt.Sprintf("%s (%s) %s", p.Pod, p.Owner, strings.Join(p.Metadata, "; "))
				if seen[line] == 0 {
					order = append(order, line)
				}
				seen[line]++
			}
		}
		for _, line := range order {
			w("  - %s, in %d record(s)\n", line, seen[line])
		}
	}

	if len(r.Static) > 0 {
		w("\n## Static checks (S)\n\n| key | check | flags every finding | worlds | qualifies |\n|---|---|---|---|---|\n")
		for _, s := range r.Static {
			var ws []string
			for _, x := range s.Worlds {
				ws = append(ws, fmt.Sprintf("%s %d of %d same flagged", x.World, x.FlaggedSame, x.SameCharts))
			}
			w("| %s | %s | %v | %s | %v |\n", s.Key, s.Check, s.FlagsAll, strings.Join(ws, "; "), s.Qualifies)
		}
	}
	if len(r.Unresolved)+len(r.Unexplained) > 0 {
		w("\n## Open cells\n\n")
		for _, c := range r.Unexplained {
			w("- unexplained: %s\n", c)
		}
		for _, c := range r.Unresolved {
			w("- unresolved: %s\n", c)
		}
	}
	return []byte(b.String())
}

func okWord(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func joinOrNone(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return strings.Join(l, ", ")
}
