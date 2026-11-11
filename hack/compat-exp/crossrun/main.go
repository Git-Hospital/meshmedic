// Command crossrun turns two difftable -json reports, the ledger and the cause
// list into the numbers the decision rule of docs/compat-experiment.md reads:
// cells, exclusions, credit, N_new, P_new, D, P_D, S, the region and the
// futility bounds. It never writes the ledger and never decides a judgment
// field. See README.md and INTERPRETATIONS.md.
//
//	crossrun -inputs inputs.json [-json FILE] [-md FILE]
//
// FILE "-" is stdout. With neither flag the markdown goes to stdout. Exit 0 on
// success, 1 when the ledger and the reports disagree (the error names the
// row) or run 2 started under 12 h after run 1 ended (no output is written),
// 2 on a usage or read error.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func write(path string, data []byte, stdout *os.File) error {
	if path == "-" {
		_, err := stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("crossrun", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inputs := fs.String("inputs", "", "the manifest naming the input files (see README.md)")
	jsonOut := fs.String("json", "", "write the JSON result to FILE (- for stdout)")
	mdOut := fs.String("md", "", "write the markdown summary to FILE (- for stdout)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *inputs == "" {
		fmt.Fprintln(stderr, "usage: crossrun -inputs inputs.json [-json FILE] [-md FILE]")
		return 2
	}
	in, err := LoadInputs(*inputs)
	if err != nil {
		fmt.Fprintln(stderr, "crossrun:", err)
		return 2
	}
	res, err := Analyze(in)
	if err != nil {
		fmt.Fprintln(stderr, "crossrun:", err)
		return 1
	}
	if *jsonOut == "" && *mdOut == "" {
		*mdOut = "-"
	}
	if *jsonOut != "" {
		data, err := RenderJSON(res)
		if err == nil {
			err = write(*jsonOut, data, stdout)
		}
		if err != nil {
			fmt.Fprintln(stderr, "crossrun:", err)
			return 2
		}
	}
	if *mdOut != "" {
		if err := write(*mdOut, RenderMarkdown(res), stdout); err != nil {
			fmt.Fprintln(stderr, "crossrun:", err)
			return 2
		}
	}
	return 0
}
