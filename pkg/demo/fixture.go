// Package demo replays a recorded Prometheus through MeshMedic's real
// detection path, so a reader with no mesh and no Prometheus can see what the
// tool does.
//
// Nothing here decides anything. A fixture holds only what Prometheus would
// have answered (and, for configuration reads, what the cluster would have
// returned) on a sequence of cycles. The catalog entries, the query
// evaluation, the hold and clear logic, the four cycle states, the patch
// rendering and the report all come from the same packages `meshmedic watch`
// uses. The fixture is served by a loopback HTTP server and read through
// pkg/prom's own client, so the only thing replaced is the far end of the wire.
package demo

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

//go:embed fixtures/*.json
var fixtureFS embed.FS

// DefaultName is the scenario `meshmedic demo` runs without --scenario.
const DefaultName = "canary-latency-rollback"

// Series is one labeled sample of an evidence answer.
type Series struct {
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

// Step is one detection cycle: when it runs, and what Prometheus answers on it.
type Step struct {
	At time.Time `json:"at"`
	// Telemetry is false when Prometheus holds no mesh series at all: every
	// query, the coverage probe included, comes back empty.
	Telemetry bool `json:"telemetry"`
	// Signal is the value of the scenario's signal query on this cycle. Absent
	// means the query returns an empty vector.
	Signal *float64 `json:"signal"`
}

// Fixture is one replayable scenario.
type Fixture struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	// Source names the file in this repository the numbers come from.
	Source string `json:"source"`
	// Recorded lists what is taken from Source as it stands. Constructed lists
	// what is made up to fill the gaps. No fixture holds a raw recorded
	// Prometheus response: the proof reports keep the incident report, not the
	// query results, so every response body is rebuilt.
	Recorded    []string `json:"recorded"`
	Constructed []string `json:"constructed"`

	Target         map[string]string         `json:"target"`
	Scenarios      []string                  `json:"scenarios"`
	CoverageSeries float64                   `json:"coverageSeries"`
	Evidence       map[string][]Series       `json:"evidence"`
	Objects        map[string]map[string]any `json:"objects"`
	Steps          []Step                    `json:"steps"`
}

// Load returns the named fixture.
func Load(name string) (Fixture, error) {
	data, err := fixtureFS.ReadFile("fixtures/" + name + ".json")
	if err != nil {
		return Fixture{}, fmt.Errorf("unknown demo scenario %q", name)
	}
	var f Fixture
	if err := json.Unmarshal(data, &f); err != nil {
		return Fixture{}, fmt.Errorf("demo fixture %s: %w", name, err)
	}
	if f.Name != name || len(f.Steps) == 0 || len(f.Scenarios) == 0 {
		return Fixture{}, fmt.Errorf("demo fixture %s is malformed", name)
	}
	return f, nil
}

// Names lists the available scenarios, the default first.
func Names() []string {
	entries, err := fixtureFS.ReadDir("fixtures")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if n != DefaultName+".json" {
			names = append(names, n[:len(n)-len(".json")])
		}
	}
	sort.Strings(names)
	return append([]string{DefaultName}, names...)
}
