package proof

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Git-Hospital/meshmedic/pkg/catalog"
)

// The roadmap promises "N proven, M declared unprovable, zero silently
// missing". Only `meshmedic-prove list` printed that sentence; nothing failed
// when a catalog entry arrived with neither a proof nor a declaration, or when
// a proof named a neighbour that does not exist.
func TestShippedCatalogIsFullyCovered(t *testing.T) {
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
		t.Fatalf("declarations: %v", err)
	}
	if len(unprovable) == 0 {
		t.Fatal("no declarations read from ../../proof/UNPROVABLE.yaml; the file ships with some, so the loader read the wrong place")
	}
	for _, p := range CheckCoverage(scenarios, specs, unprovable) {
		t.Error(p)
	}
}

func TestCheckCoverage(t *testing.T) {
	scenarios := []catalog.Scenario{{ID: "proven"}, {ID: "declared"}, {ID: "neighbour"}}
	whole := func() ([]Spec, map[string]string) {
		return []Spec{
			{Entry: "proven", Expect: Expect{Quiet: []string{"neighbour"}, AllowFiring: []string{"declared"}}},
			{Entry: "neighbour"},
		}, map[string]string{"declared": "the testbed cannot produce it"}
	}

	specs, unprovable := whole()
	if got := CheckCoverage(scenarios, specs, unprovable); len(got) != 0 {
		t.Fatalf("a whole inventory reports problems: %q", got)
	}

	for name, tc := range map[string]struct {
		break_ func(specs []Spec, unprovable map[string]string) ([]Spec, map[string]string)
		want   string
	}{
		"entry with neither": {
			func(s []Spec, u map[string]string) ([]Spec, map[string]string) { return s[:1], u },
			`catalog entry "neighbour" has neither a proof nor a declaration`,
		},
		"declaration removed": {
			func(s []Spec, u map[string]string) ([]Spec, map[string]string) { return s, map[string]string{} },
			`catalog entry "declared" has neither a proof nor a declaration`,
		},
		"proof for an unknown entry": {
			func(s []Spec, u map[string]string) ([]Spec, map[string]string) {
				return append(s, Spec{Entry: "typo"}), u
			},
			`proof "typo": entry is not in the catalog`,
		},
		"quiet names an unknown id": {
			func(s []Spec, u map[string]string) ([]Spec, map[string]string) {
				s[0].Expect.Quiet = []string{"neighbor"}
				return s, u
			},
			`proof "proven": expect.quiet names "neighbor"`,
		},
		"allowFiring names an unknown id": {
			func(s []Spec, u map[string]string) ([]Spec, map[string]string) {
				s[0].Expect.AllowFiring = []string{"gone"}
				return s, u
			},
			`proof "proven": expect.allowFiring names "gone"`,
		},
		"proven and declared unprovable": {
			func(s []Spec, u map[string]string) ([]Spec, map[string]string) {
				u["proven"] = "but it has a proof"
				return s, u
			},
			`entry "proven" has both a proof and a declaration`,
		},
		"declaration for an unknown id": {
			func(s []Spec, u map[string]string) ([]Spec, map[string]string) {
				u["retired"] = "no longer in the catalog"
				return s, u
			},
			`UNPROVABLE.yaml declares "retired"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			specs, unprovable := tc.break_(whole())
			got := CheckCoverage(scenarios, specs, unprovable)
			if len(got) != 1 || !strings.Contains(got[0], tc.want) {
				t.Fatalf("got %q, want one problem containing %q", got, tc.want)
			}
		})
	}
}

func TestLoadUnprovable(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadUnprovable(dir)
	if err != nil || len(got) != 0 {
		t.Fatalf("missing file: got %v, %v; want no declarations and no error", got, err)
	}

	write := func(doc string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, unprovableFile), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("unprovable:\n  - entry: a\n    reason: >\n      cannot be made\n      to fire here\n")
	got, err = LoadUnprovable(dir)
	if err != nil || got["a"] != "cannot be made to fire here" {
		t.Fatalf("got %q, %v; want the reason of a, trimmed", got, err)
	}

	// Read as "nothing declared", a broken file would turn every declared
	// entry into a silently missing one.
	write("unprovable: [")
	if _, err := LoadUnprovable(dir); err == nil {
		t.Fatal("a file that does not parse must be an error")
	}
}
