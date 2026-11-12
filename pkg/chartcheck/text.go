package chartcheck

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// WriteJSON writes the report as indented JSON followed by a newline.
func WriteJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

const textWidth = 100

// WriteText writes the report for a person in a terminal.
func WriteText(w io.Writer, r *Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "meshmedic chart check (rules written against Istio %s)\n", r.IstioVersion)
	fmt.Fprintf(&b, "input: %s\n", r.Input)
	fmt.Fprintf(&b, "read %d objects and %d pod templates; namespace %s; modes: %s\n", r.Examined.Objects, r.Examined.PodTemplates, r.Namespace, joinModes(r.Modes))

	for _, f := range r.Findings {
		fmt.Fprintf(&b, "\n%s  %s  %s\n", f.ID, f.Mode, f.Reason)
		field(&b, "resource", fmt.Sprintf("%s %s/%s", f.Resource.Kind, f.Resource.Namespace, f.Resource.Name))
		field(&b, "field", fmt.Sprintf("%s/%s %s = %s", f.Trigger.Kind, f.Trigger.Name, f.Trigger.Path, f.Trigger.Value))
		field(&b, "effect", f.Effect)
		if len(f.Docs) == 0 {
			field(&b, "istio", "no documentation anchor. "+f.DocsNote)
		} else {
			field(&b, "istio", fmt.Sprintf("%q", f.Docs[0].Quote))
			field(&b, "", f.Docs[0].URL+" ("+f.Docs[0].Heading+")")
			// More passages of the same page and heading add nothing to a
			// line that only names where to look; the JSON keeps every quote.
			seen := map[string]bool{f.Docs[0].URL + f.Docs[0].Heading: true}
			for _, d := range f.Docs[1:] {
				if k := d.URL + d.Heading; !seen[k] {
					seen[k] = true
					field(&b, "also", d.URL+" ("+d.Heading+")")
				}
			}
		}
		field(&b, "fix", f.Fix)
		field(&b, "confirm", f.Confirm)
		b.WriteString("  evidence:\n")
		for _, e := range f.Evidence {
			b.WriteString(wrap("    - ", "      ", fmt.Sprintf("%s/%s %s = %s", e.Kind, e.Name, e.Path, e.Value)))
		}
	}

	b.WriteString("\n")
	if r.Candidates == 0 {
		fmt.Fprintf(&b, "No candidates found in %d objects.\n", r.Examined.Objects)
	} else {
		fmt.Fprintf(&b, "%d %s found.\n", r.Candidates, plural(r.Candidates, "candidate", "candidates"))
	}
	b.WriteString(wrap("", "", Notice))
	b.WriteString("\nchecks that ran:\n")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "  %s  %-26s %-8s %d\n", c.ID, c.Check, c.Mode, c.Findings)
	}
	b.WriteString("\nwhat these checks cannot see:\n")
	for _, s := range r.BlindSpots {
		b.WriteString(wrap("  - ", "    ", s))
	}
	_, err := io.WriteString(w, b.String())
	return err
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
	return strings.Join(s, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
