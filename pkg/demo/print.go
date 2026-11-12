package demo

import (
	"fmt"
	"io"
	"strings"

	"github.com/Git-Hospital/meshmedic/pkg/detect"
)

// The two commands to run against a real setup, as the README gives them.
const (
	NextCheck = "meshmedic check --prometheus http://localhost:9090 --target service=payments,namespace=demo"
	NextWatch = "meshmedic watch --prometheus http://localhost:9090 --target service=payments,namespace=demo"
)

const replayNotice = "This is a REPLAY of data held inside this binary. Nothing is being watched and nothing is changed."

// Banner is the first line of every screen the command prints.
func Banner(w io.Writer) {
	fmt.Fprintln(w, replayNotice)
}

// NextSteps ends every screen the command prints.
func NextSteps(w io.Writer) {
	fmt.Fprintln(w, "Next, on a real setup (a Prometheus that scrapes Istio metrics):")
	fmt.Fprintln(w, "  "+NextCheck+"   # exits 1 if it cannot see the target")
	fmt.Fprintln(w, "  "+NextWatch)
}

// List prints the available scenarios.
func List(w io.Writer) error {
	Banner(w)
	fmt.Fprintln(w)
	for _, n := range Names() {
		fx, err := Load(n)
		if err != nil {
			return err
		}
		mark := "  "
		if n == DefaultName {
			mark = "* "
		}
		fmt.Fprintf(w, "%s%-32s %s\n", mark, n, fx.Title)
	}
	fmt.Fprintln(w, "\nRun one with: meshmedic demo --scenario NAME   (* is the default)")
	fmt.Fprintln(w)
	NextSteps(w)
	return nil
}

// Render prints a replay: what it is, one row per cycle with the dossier and
// the resolution where the real code produced them, and where the data came from.
func Render(w io.Writer, res Result, verbose bool, wrote []string) {
	fx := res.Fixture
	fmt.Fprintf(w, "meshmedic demo: %s\n", fx.Name)
	Banner(w)
	fmt.Fprintf(w, "The real detector ran %d cycles against a Prometheus served from this process on loopback.\n", len(res.Steps))
	if res.Catalog != "" {
		fmt.Fprintf(w, "Catalog used: %s (not the one built into this binary).\n", res.Catalog)
	}
	fmt.Fprintln(w)

	for i, sr := range res.Steps {
		for _, ev := range sr.Cycle.Evaluations {
			fmt.Fprintf(w, "cycle %d  %s  %s: %s\n", i+1, sr.At.UTC().Format("15:04:05Z"), ev.Scenario, describe(res, sr, ev))
		}
		if !sr.Cycle.Healthy() {
			fmt.Fprintf(w, "         `meshmedic check` would exit 1 here: %d of %d targets could not be observed.\n",
				sr.Cycle.Unobserved, sr.Cycle.Observed+sr.Cycle.Unobserved)
		}
		if verbose {
			fmt.Fprintf(w, "         cycle summary: %s\n", sr.Cycle.Line())
			for _, rq := range sr.Requests {
				fmt.Fprintf(w, "         query (%s): %s\n         response: %s\n", rq.Role, rq.PromQL, rq.Body)
			}
		}
		for _, d := range sr.Dossiers {
			fmt.Fprintf(w, "\n%s\n", strings.TrimRight(d, "\n"))
		}
		for _, r := range sr.Resolved {
			fmt.Fprintf(w, "\n%s\n", strings.TrimRight(r, "\n"))
		}
	}

	fmt.Fprintln(w)
	for _, p := range wrote {
		fmt.Fprintf(w, "Wrote %s\n", p)
	}
	Footer(w, fx, wrote, res.OutRequested)
}

// Footer says what the data is, and what the command did not do.
func Footer(w io.Writer, fx Fixture, wrote []string, outRequested bool) {
	fmt.Fprintf(w, "Data: CONSTRUCTED to match %s, not recorded.\n", fx.Source)
	fmt.Fprintln(w, "  (The proof reports keep the incident report, not the raw Prometheus responses.)")
	if len(fx.Recorded) > 0 {
		fmt.Fprintln(w, "  Taken from that file:")
		for _, r := range fx.Recorded {
			fmt.Fprintf(w, "    - %s\n", r)
		}
	} else {
		fmt.Fprintln(w, "  Nothing in it is taken from a recorded run.")
	}
	fmt.Fprintln(w, "  Made up for this replay:")
	for _, c := range fx.Constructed {
		fmt.Fprintf(w, "    - %s\n", c)
	}
	fmt.Fprintln(w, "  The detection, thresholds, patch and report are the real code. No pull request was opened,")
	fmt.Fprintln(w, "  no token was read, nothing beyond loopback was contacted.")
	if len(wrote) == 0 {
		if outRequested {
			fmt.Fprintln(w, "  --out was given, but this scenario produces no dossier files, so none were written.")
		} else {
			fmt.Fprintln(w, "  No file was written (add --out DIR to write the dossier).")
		}
	}
	fmt.Fprintln(w)
	NextSteps(w)
}

func describe(res Result, sr StepResult, ev detect.Evaluation) string {
	var s = res.Scenarios[0]
	for _, c := range res.Scenarios {
		if c.ID == ev.Scenario {
			s = c
		}
	}
	signal := fmt.Sprintf("signal %.4g %s %.4g", ev.Value, s.Signal.Comparison, s.Signal.Threshold)
	switch ev.Outcome {
	case detect.OutcomeFiring:
		for _, inc := range sr.Incidents {
			if inc.Scenario.ID == ev.Scenario {
				return fmt.Sprintf("FIRING, held for %s: incident reported (%s)", s.Signal.For, signal)
			}
		}
		if sr.Cycle.Waiting > 0 {
			return fmt.Sprintf("in breach, waiting out the %s hold (%s)", s.Signal.For, signal)
		}
		return fmt.Sprintf("still in breach, already reported, no second report (%s)", signal)
	case detect.OutcomeClear:
		if len(sr.Resolutions) > 0 {
			return fmt.Sprintf("clear, a real answer: the signal is %.4g, back under %.4g", ev.Value, s.Signal.Threshold)
		}
		return fmt.Sprintf("clear, a real answer: the signal is %.4g, not %s %.4g", ev.Value, s.Signal.Comparison, s.Signal.Threshold)
	case detect.OutcomeBlind:
		return "BLIND, not clear: " + ev.Reason
	case detect.OutcomeUnlocked:
		return "UNLOCKED, does not run: " + ev.Reason
	}
	return string(ev.Outcome)
}
