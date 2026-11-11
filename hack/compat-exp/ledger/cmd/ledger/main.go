// Command ledger checks hack/compat-exp/ledger.json.
//
//	ledger check [-ledger hack/compat-exp/ledger.json] [-causes hack/compat-exp/causes.json]
//
// Exit 0 when the ledger is valid, 1 when it is not (one problem per line,
// each naming its row), 2 on a usage or read error.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(os.Stderr, "usage: ledger check [-ledger FILE] [-causes FILE]")
		return 2
	}
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	lpath := fs.String("ledger", "hack/compat-exp/ledger.json", "the ledger file")
	cpath := fs.String("causes", "hack/compat-exp/causes.json", "the cause list")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return 2
	}
	causes, err := ledger.LoadCauses(*cpath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	f, err := ledger.Load(*lpath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ps := ledger.Check(f, causes)
	for _, p := range ps {
		fmt.Println(p)
	}
	if len(ps) > 0 {
		fmt.Printf("%d problem(s) in %s\n", len(ps), *lpath)
		return 1
	}
	fmt.Printf("%s: %d row(s), %d new key(s), ok\n", *lpath, len(f.Rows), len(f.NewKeys))
	return 0
}
