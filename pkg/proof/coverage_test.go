package proof

import (
	"sort"
	"testing"

	"github.com/Git-Hospital/meshmedic/pkg/catalog"
)

// checkCoverage returns one message per broken promise: a catalog entry with
// neither a proof nor a declaration, or a reference to an id the catalog does
// not have. The inventory "N proven, M declared unprovable, zero silently
// missing" is only true while this returns nothing.
func checkCoverage(ids map[string]bool, specs []Spec, unprovable map[string]string) []string {
	var out []string
	proven := map[string]bool{}
	for _, s := range specs {
		proven[s.Entry] = true
		if !ids[s.Entry] {
			out = append(out, "proof names entry missing from the catalog: "+s.Entry)
		}
		for _, q := range s.Expect.Quiet {
			if !ids[q] {
				out = append(out, s.Entry+": expect.quiet names missing entry "+q)
			}
		}
		for _, a := range s.Expect.AllowFiring {
			if !ids[a] {
				out = append(out, s.Entry+": expect.allowFiring names missing entry "+a)
			}
		}
	}
	for id := range unprovable {
		if !ids[id] {
			out = append(out, "UNPROVABLE.yaml names entry missing from the catalog: "+id)
		}
	}
	for id := range ids {
		_, declared := unprovable[id]
		if !proven[id] && !declared {
			out = append(out, "catalog entry has neither a proof nor an UNPROVABLE declaration: "+id)
		}
	}
	sort.Strings(out)
	return out
}

func TestCheckCoverage(t *testing.T) {
	ids := map[string]bool{"a": true, "b": true}
	spec := func(entry string, quiet, allow []string) Spec {
		return Spec{Entry: entry, Expect: Expect{Quiet: quiet, AllowFiring: allow}}
	}
	cases := []struct {
		name  string
		specs []Spec
		unp   map[string]string
		want  int
	}{
		{"all covered", []Spec{spec("a", []string{"b"}, nil)}, map[string]string{"b": "r"}, 0},
		{"entry with nothing", []Spec{spec("a", nil, nil)}, map[string]string{}, 1},
		{"proof for removed entry", []Spec{spec("a", nil, nil), spec("b", nil, nil), spec("gone", nil, nil)}, nil, 1},
		{"quiet names missing", []Spec{spec("a", []string{"gone"}, nil), spec("b", nil, nil)}, nil, 1},
		{"allowFiring names missing", []Spec{spec("a", nil, []string{"gone"}), spec("b", nil, nil)}, nil, 1},
		{"declaration for removed entry", []Spec{spec("a", nil, nil), spec("b", nil, nil)}, map[string]string{"gone": "r"}, 1},
	}
	for _, c := range cases {
		if got := checkCoverage(ids, c.specs, c.unp); len(got) != c.want {
			t.Errorf("%s: got %d problems %v, want %d", c.name, len(got), got, c.want)
		}
	}
}

// The shipped inventory must be complete. This turns the roadmap's "zero
// silently unverified" from a sentence into an invariant.
func TestShippedCatalogHasProofOrDeclaration(t *testing.T) {
	scenarios, err := catalog.LoadDir("../../catalog")
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	specs, err := LoadDir("../../proof")
	if err != nil {
		t.Fatalf("proofs: %v", err)
	}
	unprovable, err := LoadUnprovable("../../proof")
	if err != nil {
		t.Fatalf("unprovable: %v", err)
	}
	ids := map[string]bool{}
	for _, s := range scenarios {
		ids[s.ID] = true
	}
	for _, p := range checkCoverage(ids, specs, unprovable) {
		t.Error(p)
	}
}
