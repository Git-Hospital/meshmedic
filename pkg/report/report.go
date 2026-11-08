// Package report renders an incident into the markdown document that
// becomes the pull request body. The CLI prints the same document, so what
// an operator sees in the terminal is exactly what reviewers will read.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Git-Hospital/meshmedic/pkg/detect"
	"github.com/Git-Hospital/meshmedic/pkg/prom"
)

// Markdown renders one incident and its proposed patch.
func Markdown(inc detect.Incident, patch string) string {
	var b strings.Builder
	s := inc.Scenario

	// The report shows the threshold actually compared against. When it came
	// from the learned baseline rather than the static catalog value, say so,
	// so a reviewer knows the alert is relative to this target's own normal.
	threshold := inc.Threshold
	if threshold == 0 && !inc.BaselineRelative {
		threshold = s.Signal.Threshold
	}
	thresholdNote := ""
	if inc.BaselineRelative {
		thresholdNote = fmt.Sprintf(" (%gx the learned baseline)", s.Signal.BaselineMultiplier)
	}

	fmt.Fprintf(&b, "## Incident: %s\n\n", s.Title)
	fmt.Fprintf(&b, "Scenario `%s` (severity %s) fired for %s.\n\n", s.ID, s.Severity, formatParams(inc.Params))
	// signal.for is optional in the catalog; without it there is no hold time to name.
	holdFor := ""
	if s.Signal.For != "" {
		holdFor = " for " + s.Signal.For
	}
	fmt.Fprintf(&b, "The signal has held at **%.4g** (threshold %s %.4g%s%s) since %s.\n\n",
		inc.Value, s.Signal.Comparison, threshold, thresholdNote, holdFor,
		inc.Since.UTC().Format(time.RFC3339))

	fmt.Fprintf(&b, "### Diagnosis\n\n%s\n", strings.TrimSpace(s.Description))

	if len(inc.Evidence) > 0 {
		fmt.Fprintf(&b, "\n### Evidence\n\n| query | value |\n| --- | --- |\n")
		for _, e := range inc.Evidence {
			if e.Err != nil {
				fmt.Fprintf(&b, "| %s | unavailable (%s) |\n", cell(e.Name), cell(e.Err.Error()))
				continue
			}
			for _, s := range rankedSamples(e.Samples) {
				fmt.Fprintf(&b, "| %s | %.4g |\n", cell(sampleName(e.Name, s.Labels)), s.Value)
			}
		}
	}

	if len(inc.ObjectEvidence) > 0 {
		fmt.Fprintf(&b, "\n### Configuration evidence\n\n")
		for _, o := range inc.ObjectEvidence {
			if o.Err != nil {
				fmt.Fprintf(&b, "- %s (%s): unavailable (%s)\n", o.Name, o.Ref, oneLine(o.Err.Error()))
				continue
			}
			paths := make([]string, 0, len(o.Fields))
			for p := range o.Fields {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			for _, p := range paths {
				fmt.Fprintf(&b, "- %s (%s) %s: %s\n", o.Name, code(o.Ref), code(p), code(o.Fields[p]))
			}
		}
	}

	if len(inc.LogEvidence) > 0 {
		fmt.Fprintf(&b, "\n### Log evidence\n\n")
		for _, le := range inc.LogEvidence {
			if le.Err != nil {
				fmt.Fprintf(&b, "- %s: unavailable (%s)\n", le.Name, oneLine(le.Err.Error()))
				continue
			}
			deps := make([]string, 0, len(le.Matches))
			for dep := range le.Matches {
				deps = append(deps, dep)
			}
			sort.Strings(deps)
			hit := false
			for _, dep := range deps {
				if len(le.Matches[dep]) == 0 {
					continue
				}
				hit = true
				body := strings.Join(le.Matches[dep], "\n")
				f := fence(body)
				fmt.Fprintf(&b, "**%s**, %s:\n\n%s\n%s\n%s\n", le.Name, code(dep), f, body, f)
			}
			if !hit {
				fmt.Fprintf(&b, "- %s: no matching log lines in the namespace\n", le.Name)
			}
		}
	}

	if len(inc.RolloutEvidence) > 0 {
		fmt.Fprintf(&b, "\n### Recent rollouts\n\n")
		for _, re := range inc.RolloutEvidence {
			if re.Err != nil {
				fmt.Fprintf(&b, "- %s: unavailable (%s)\n", re.Name, oneLine(re.Err.Error()))
				continue
			}
			if len(re.Rollouts) == 0 {
				fmt.Fprintf(&b, "- %s: no rollouts in the window\n", re.Name)
				continue
			}
			for _, r := range re.Rollouts {
				f := fence(r.Diff)
				fmt.Fprintf(&b, "**%s**: %s rolled %ds ago, template diff (previous to current):\n\n%sdiff\n%s\n%s\n",
					re.Name, code(r.Deployment), r.AgeSeconds, f, r.Diff, f)
			}
		}
	}

	f := fence(patch)
	fmt.Fprintf(&b, "\n### Proposed patch (%s)\n\n%syaml\n%s%s\n", s.Remediation.Target.Kind, f, patch, f)
	fmt.Fprintf(&b, "\n### Rollback\n\n%s\n", strings.TrimSpace(s.Rollback))

	if s.Guardrails.RequiresApproval {
		fmt.Fprintf(&b, "\nThis scenario requires human approval; nothing was applied to the cluster.\n")
	}
	return b.String()
}

// Resolved renders the closing report for a recovered incident: what fired,
// when it opened, when it cleared, and how long it was open. It is the other
// half of Markdown, so an operator watching the terminal sees the incident
// open and then close with its own MTTR.
func Resolved(res detect.Resolution) string {
	s := res.Scenario
	var b strings.Builder
	fmt.Fprintf(&b, "## Resolved: %s\n\n", s.Title)
	fmt.Fprintf(&b, "Scenario `%s` for %s has recovered.\n\n", s.ID, formatParams(res.Params))
	fmt.Fprintf(&b, "The signal is back to **%.4g** (threshold %s %.4g). Open from %s to %s, a duration of %s.\n",
		res.Value, s.Signal.Comparison, res.Threshold,
		res.Since.UTC().Format(time.RFC3339),
		res.ResolvedAt.UTC().Format(time.RFC3339),
		formatDuration(res.Duration()))
	return b.String()
}

// formatDuration renders an incident's open interval at second resolution;
// sub-second precision is noise for an MTTR line.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "under 1s"
	}
	return d.Round(time.Second).String()
}

// sampleName renders one evidence row's identity: the query name plus the
// sample's labels in PromQL selector form, so a per-workload breakdown reads
// like errors-by-workload{destination_workload="payments-v2"}.
func sampleName(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, labels[k]))
	}
	return name + "{" + strings.Join(parts, ", ") + "}"
}

// rankedSamples orders a breakdown biggest value first, so the offending
// workload is the first row a reviewer reads. Ties break on labels to keep
// the report deterministic.
func rankedSamples(samples []prom.Sample) []prom.Sample {
	out := make([]prom.Sample, len(samples))
	copy(out, samples)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		return sampleName("", out[i].Labels) < sampleName("", out[j].Labels)
	})
	return out
}

func formatParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("`%s=%s`", k, params[k]))
	}
	return strings.Join(parts, " ")
}

// Text from the cluster (log lines, template diffs, label values, field values, error strings)
// goes into a pull request body that a person is about to approve. These helpers keep that text
// from closing a code block, breaking a table or starting new markdown (#19).

// fence returns a code fence longer than the longest run of backticks in text. A CommonMark
// fence closes only at a run of backticks at least as long as the one that opened it, so a
// log line holding three backticks cannot end the block early.
func fence(text string) string {
	return strings.Repeat("`", max(3, longestBacktickRun(text)+1))
}

// code renders text as one inline code span on one line, delimited by more backticks than
// any run inside it, padded with a space when the text starts or ends with a backtick.
func code(text string) string {
	text = oneLine(text)
	d := strings.Repeat("`", longestBacktickRun(text)+1)
	if strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`") {
		text = " " + text + " "
	}
	return d + text + d
}

// cell makes text safe inside a table cell: one line, and a literal | escaped so it does not
// start a new column.
func cell(text string) string {
	return strings.ReplaceAll(oneLine(text), "|", `\|`)
}

// oneLine replaces line breaks with spaces, so text cannot end a list item or a table row.
func oneLine(text string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(text)
}

func longestBacktickRun(text string) int {
	longest, run := 0, 0
	for _, r := range text {
		if r != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	return longest
}
