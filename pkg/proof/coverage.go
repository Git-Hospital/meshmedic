package proof

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Git-Hospital/meshmedic/pkg/catalog"
)

// LoadUnprovable reads the declarations in dir's UNPROVABLE.yaml: entry id to
// the reason it cannot be proven here. A missing file means none are declared,
// which is a legitimate state: every gap is then of the ambiguous kind. A file
// that does not parse is an error, because reading it as "nothing declared"
// would turn every declared entry into a silently missing one.
func LoadUnprovable(dir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, unprovableFile))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Unprovable []struct {
			Entry  string `yaml:"entry"`
			Reason string `yaml:"reason"`
		} `yaml:"unprovable"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", unprovableFile, err)
	}
	out := map[string]string{}
	for _, u := range doc.Unprovable {
		out[u.Entry] = strings.TrimSpace(u.Reason)
	}
	return out, nil
}

// CheckCoverage holds the inventory to its promise: N proven, M declared
// unprovable, zero silently missing. It returns one line per problem, sorted,
// and nothing when the inventory is whole:
//
//   - a catalog entry with neither a proof nor a declaration;
//   - a proof whose entry, expect.quiet or expect.allowFiring names an id the
//     catalog does not have. A quiet id that does not exist is the quiet kind
//     of damage: the "neighbour stays quiet" assertion passes because nothing
//     by that name can fire;
//   - a declaration for an id the catalog does not have;
//   - an entry that has a proof and a declaration. It is one or the other, and
//     the inventory would count it twice.
func CheckCoverage(scenarios []catalog.Scenario, specs []Spec, unprovable map[string]string) []string {
	known := make(map[string]bool, len(scenarios))
	for _, s := range scenarios {
		known[s.ID] = true
	}
	proven := make(map[string]bool, len(specs))
	var problems []string
	for _, sp := range specs {
		proven[sp.Entry] = true
		if !known[sp.Entry] {
			problems = append(problems, fmt.Sprintf("proof %q: entry is not in the catalog", sp.Entry))
		}
		for _, id := range sp.Expect.Quiet {
			if !known[id] {
				problems = append(problems, fmt.Sprintf("proof %q: expect.quiet names %q, which is not in the catalog", sp.Entry, id))
			}
		}
		for _, id := range sp.Expect.AllowFiring {
			if !known[id] {
				problems = append(problems, fmt.Sprintf("proof %q: expect.allowFiring names %q, which is not in the catalog", sp.Entry, id))
			}
		}
	}
	for id := range unprovable {
		if !known[id] {
			problems = append(problems, fmt.Sprintf("%s declares %q, which is not in the catalog", unprovableFile, id))
		} else if proven[id] {
			problems = append(problems, fmt.Sprintf("entry %q has both a proof and a declaration in %s", id, unprovableFile))
		}
	}
	for _, s := range scenarios {
		if _, declared := unprovable[s.ID]; !proven[s.ID] && !declared {
			problems = append(problems, fmt.Sprintf("catalog entry %q has neither a proof nor a declaration in %s", s.ID, unprovableFile))
		}
	}
	sort.Strings(problems)
	return problems
}
