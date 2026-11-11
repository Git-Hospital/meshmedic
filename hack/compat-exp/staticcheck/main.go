// Command staticcheck runs the compat experiment's static checks over the
// output of `helm template` for one world, and says for each check whether
// it flags the chart and which fields made it do so.
//
// The checks are the ones causes.json names, one per cause key that has one
// (docs/compat-experiment.md, section 1, metric S). Each check's rule is one
// or two sentences, stored next to its id in causes.json; causes_test.go
// holds the two equal. They read only what a rendered manifest shows, so a
// requirement that lives in an image or in application code is out of reach.
//
// Usage:
//
//	helm template ... | go run ./hack/compat-exp/staticcheck -world W3
//	go run ./hack/compat-exp/staticcheck -world W1 [-namespace pilot] [-native-sidecars=true] rendered.yaml
//	go run ./hack/compat-exp/staticcheck -list
//
// The output is JSON: checks in the order of causes.json, flags sorted by
// subject and reason, evidence as kind, name, path and the value seen.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
)

type checkResult struct {
	ID      string    `json:"id"`
	Key     string    `json:"key"`
	Applies bool      `json:"applies"`
	Flagged bool      `json:"flagged"`
	Flags   []finding `json:"flags"`
}

type report struct {
	World          string        `json:"world"`
	Namespace      string        `json:"namespace"`
	NativeSidecars bool          `json:"nativeSidecars"`
	Objects        int           `json:"objects"`
	Checks         []checkResult `json:"checks"`
}

func run(r io.Reader, o options, namespace string) (report, error) {
	m, err := parseManifest(r, namespace)
	if err != nil {
		return report{}, err
	}
	rep := report{World: o.World, Namespace: namespace, NativeSidecars: o.NativeSidecars, Objects: len(m.Objects)}
	for _, c := range checks {
		res := checkResult{ID: c.ID, Key: c.Key, Applies: applies(c, o.World), Flags: []finding{}}
		if res.Applies {
			fl := c.Run(m, o)
			sort.SliceStable(fl, func(i, j int) bool {
				if fl[i].Subject != fl[j].Subject {
					return fl[i].Subject < fl[j].Subject
				}
				return fl[i].Reason < fl[j].Reason
			})
			res.Flags = append(res.Flags, fl...)
		}
		res.Flagged = len(res.Flags) > 0
		rep.Checks = append(rep.Checks, res)
	}
	return rep, nil
}

func main() {
	world := flag.String("world", "", "W1 (sidecar) or W3 (ambient)")
	namespace := flag.String("namespace", "pilot", "release namespace, for objects that name none (observe.sh installs into pilot)")
	list := flag.Bool("list", false, "print the checks (id, key, worlds, rule) and exit")
	native := flag.Bool("native-sidecars", true, "W1 proxies are native sidecars unless a pod opts out (the pinned Istio 1.31.3 on Kubernetes 1.36)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: staticcheck -world W1|W3 [-namespace NS] [-native-sidecars=BOOL] [FILE] | staticcheck -list")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *list {
		type entry struct {
			ID     string   `json:"id"`
			Key    string   `json:"key"`
			Worlds []string `json:"worlds"`
			Rule   string   `json:"rule"`
		}
		var out []entry
		for _, c := range checks {
			out = append(out, entry{c.ID, c.Key, c.Worlds, c.Rule})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}
	if *world != "W1" && *world != "W3" {
		flag.Usage()
		os.Exit(2)
	}
	in := io.Reader(os.Stdin)
	switch flag.NArg() {
	case 0:
	case 1:
		f, err := os.Open(flag.Arg(0))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer f.Close()
		in = f
	default:
		flag.Usage()
		os.Exit(2)
	}
	rep, err := run(in, options{World: *world, NativeSidecars: *native}, *namespace)
	if err != nil {
		fmt.Fprintln(os.Stderr, "staticcheck:", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
