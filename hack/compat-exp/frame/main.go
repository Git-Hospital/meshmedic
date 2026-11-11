// Command frame turns Artifact Hub's star ranking into the compat
// experiment's candidate list, mechanically (docs/compat-experiment.md,
// section 4 and work item 2).
//
//	frame fetch  -out DIR                 the only command that uses the network
//	frame build  -in DIR -out FILE        offline: stored responses to candidates.json
//	frame charts -in FILE -out FILE       candidates.json to the observe.sh chart shape
//
// Standard library only.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "frame:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "fetch":
		fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
		out := fs.String("out", "", "directory for the raw responses (must be empty or absent)")
		pause := fs.Duration("pause", time.Second, "pause between requests")
		timeout := fs.Duration("timeout", 30*time.Second, "timeout per request")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *out == "" {
			return fmt.Errorf("fetch: -out is required")
		}
		return fetchAll(newHTTPDoer(*timeout), *out, *pause, os.Stderr)
	case "build":
		fs := flag.NewFlagSet("build", flag.ContinueOnError)
		in := fs.String("in", "", "directory written by fetch")
		out := fs.String("out", "", "candidates.json to write")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *in == "" || *out == "" {
			return fmt.Errorf("build: -in and -out are required")
		}
		return buildFile(*in, *out)
	case "charts":
		fs := flag.NewFlagSet("charts", flag.ContinueOnError)
		in := fs.String("in", "", "candidates.json")
		out := fs.String("out", "", "charts file to write")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *in == "" || *out == "" {
			return fmt.Errorf("charts: -in and -out are required")
		}
		return chartsFile(*in, *out)
	}
	return usage()
}

func usage() error {
	return fmt.Errorf("usage: frame fetch -out DIR | frame build -in DIR -out FILE | frame charts -in FILE -out FILE")
}
