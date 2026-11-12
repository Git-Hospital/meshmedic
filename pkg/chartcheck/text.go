package chartcheck

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// WriteJSON writes the report as indented JSON followed by a newline.
func WriteJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

const textWidth = 100

// shortLimits is the one-paragraph form of BlindSpots for the default report.
const shortLimits = "Only rendered manifests are read. Namespace enrolment, PeerAuthentication, policies from outside the chart and the CNI are assumed, not read, and nothing is run. Run with --verbose for the full list."

// WriteText writes the report for a person in a terminal. The findings are
// grouped by rule: a rule's explanation is printed once and the affected
// resources are listed under it. verbose adds every finding's evidence, the
// list of checks that ran and the full limits.
func WriteText(w io.Writer, r *Report, verbose bool) error {
	var b strings.Builder
	groups := groupFindings(r.Findings)

	fmt.Fprintf(&b, "meshmedic chart check, rules written against Istio %s\n", r.IstioVersion)
	b.WriteString(summaryLine(r, groups))
	native := "sidecar proxies assumed native"
	if !r.NativeSidecars {
		native = "sidecar proxies assumed classic"
	}
	b.WriteString(wrap("", "  ", fmt.Sprintf("read %s: %d objects, %d pod templates, namespace %s, modes %s; %s", r.Input, r.Examined.Objects, r.Examined.PodTemplates, r.Namespace, joinModes(r.Modes), native)))

	if len(r.Unread) > 0 {
		b.WriteString(wrap("", "  ", fmt.Sprintf("INCOMPLETE: %d %s could not be read, so the result covers only the rest and is not a clean result, whatever it finds.", len(r.Unread), plural(len(r.Unread), "document", "documents"))))
		for _, u := range r.Unread {
			b.WriteString(wrap("  - ", "    ", issueString(u)))
		}
	}
	if len(r.DuplicateKeys) > 0 {
		b.WriteString(wrap("", "  ", fmt.Sprintf("%d %s read with a duplicate mapping key:", len(r.DuplicateKeys), plural(len(r.DuplicateKeys), "document was", "documents were"))))
		for _, u := range r.DuplicateKeys {
			b.WriteString(wrap("  - ", "    ", issueString(u)))
		}
	}

	for _, g := range groups {
		f := g[0]
		kind := "candidate"
		if f.Level == LevelNote {
			kind = "NOTE, weaker than a candidate"
		}
		fmt.Fprintf(&b, "\n%s  %s  %s (%s)\n", f.ID, f.Mode, f.Title, kind)
		field(&b, "effect", f.Effect)
		if len(f.Docs) == 0 {
			field(&b, "istio", "no documentation anchor. "+f.DocsNote)
		} else {
			field(&b, "istio", fmt.Sprintf("%q", f.Docs[0].Quote))
			field(&b, "", f.Docs[0].URL)
		}
		field(&b, "fix", f.Fix)
		field(&b, "confirm", f.Confirm)
		writeAffected(&b, g, verbose)
	}

	b.WriteString("\n")
	b.WriteString(wrap("", "", Notice))
	if verbose {
		b.WriteString("\nchecks that ran:\n")
		for _, c := range r.Checks {
			fmt.Fprintf(&b, "  %s  %-26s %-8s %d\n", c.ID, c.Check, c.Mode, c.Findings)
		}
		b.WriteString("\nwhat these checks cannot see:\n")
		for _, s := range r.BlindSpots {
			b.WriteString(wrap("  - ", "    ", s))
		}
	} else {
		b.WriteString(wrap("limits: ", "  ", shortLimits))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// groupFindings groups findings that share a rule, a mode and a level; the
// input is already sorted, so each group is contiguous.
func groupFindings(fs []Finding) [][]Finding {
	var out [][]Finding
	for _, f := range fs {
		if n := len(out); n > 0 && out[n-1][0].ID == f.ID && out[n-1][0].Mode == f.Mode && out[n-1][0].Level == f.Level {
			out[n-1] = append(out[n-1], f)
			continue
		}
		out = append(out, []Finding{f})
	}
	return out
}

func summaryLine(r *Report, groups [][]Finding) string {
	var b strings.Builder
	switch {
	case r.Candidates == 0 && r.Notes == 0 && r.Incomplete:
		fmt.Fprintf(&b, "No candidates in the %d objects that could be read, but the read is incomplete (%d unread): this is not a clean result.\n", r.Examined.Objects, len(r.Unread))
	case r.Candidates == 0 && r.Notes == 0:
		fmt.Fprintf(&b, "No candidates in %d objects.\n", r.Examined.Objects)
	default:
		fmt.Fprintf(&b, "%d %s and %d %s", r.Candidates, plural(r.Candidates, "candidate", "candidates"), r.Notes, plural(r.Notes, "note", "notes"))
		var parts []string
		for _, g := range groups {
			p := fmt.Sprintf("%s %s x%d", g[0].ID, g[0].Mode, len(g))
			if g[0].Level == LevelNote {
				p += " (note)"
			}
			parts = append(parts, p)
		}
		b.WriteString(": " + strings.Join(parts, ", ") + "\n")
		if r.Incomplete {
			fmt.Fprintf(&b, "The read is incomplete (%d unread).\n", len(r.Unread))
		}
	}
	return b.String()
}

// writeAffected lists the resources of one group. When every resource is hit
// through the same field of the same object (one NetworkPolicy selecting two
// StatefulSets), that is said once.
func writeAffected(b *strings.Builder, g []Finding, verbose bool) {
	same := len(g) > 1
	for _, f := range g[1:] {
		if f.Trigger != g[0].Trigger {
			same = false
		}
	}
	b.WriteString("  affected:\n")
	if same {
		var names []string
		for _, f := range g {
			names = append(names, resourceString(f.Resource))
		}
		sort.Strings(names)
		b.WriteString(wrap("    ", "    ", fmt.Sprintf("all %d are hit through %s", len(g), triggerString(g[0].Trigger))))
		for _, n := range names {
			b.WriteString("    - " + n + "\n")
		}
		if verbose {
			writeEvidence(b, g[0])
		}
		return
	}
	for _, f := range g {
		b.WriteString(wrap("    - ", "      ", fmt.Sprintf("%s: %s; %s", resourceString(f.Resource), f.Reason, triggerString(f.Trigger))))
		if verbose {
			writeEvidence(b, f)
		}
	}
}

func writeEvidence(b *strings.Builder, f Finding) {
	for _, e := range f.Evidence {
		b.WriteString(wrap("        evidence: ", "                  ", fmt.Sprintf("%s/%s %s = %s", e.Kind, e.Name, e.Path, e.Value)))
	}
}

func triggerString(e Evidence) string {
	return fmt.Sprintf("%s/%s %s = %s", e.Kind, e.Name, e.Path, e.Value)
}

func field(b *strings.Builder, label, text string) {
	prefix := fmt.Sprintf("  %-9s ", label)
	if label != "" {
		prefix = fmt.Sprintf("  %-9s ", label+":")
	}
	b.WriteString(wrap(prefix, strings.Repeat(" ", len(prefix)), text))
}

// wrap breaks text at word boundaries so a line stays near textWidth. A word
// longer than the line (a URL) is kept whole.
func wrap(first, rest, text string) string {
	var b strings.Builder
	line := first
	empty := true
	for _, w := range strings.Fields(text) {
		if !empty && len(line)+1+len(w) > textWidth {
			b.WriteString(line + "\n")
			line = rest
			empty = true
		}
		if !empty {
			line += " "
		}
		line += w
		empty = false
	}
	b.WriteString(line + "\n")
	return b.String()
}

func joinModes(ms []Mode) string {
	s := make([]string, len(ms))
	for i, m := range ms {
		s[i] = string(m)
	}
	return strings.Join(s, "+")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func resourceString(r Resource) string {
	if r.Namespace == "" {
		return r.Kind + " " + r.Name
	}
	return r.Kind + " " + r.Namespace + "/" + r.Name
}

func issueString(u DocIssue) string {
	var where []string
	where = append(where, fmt.Sprintf("line %d", u.Line))
	if u.Source != "" {
		where = append(where, u.Source)
	}
	if u.Kind != "" || u.Name != "" {
		where = append(where, strings.Trim(u.Kind+"/"+u.Name, "/"))
	}
	return fmt.Sprintf("document %d (%s): %s", u.Index, strings.Join(where, ", "), u.Reason)
}
