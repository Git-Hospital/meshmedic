package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

// Report is the part of difftable's -json report (hack/compat-exp/DIFFTABLE-JSON.md)
// that crossrun reads. Unknown fields are ignored: the report may grow.
type Report struct {
	Schema       int           `json:"schema"`
	Oracle       string        `json:"oracle"`
	Worlds       []string      `json:"worlds"`
	RunsRequired int           `json:"runsRequired"`
	Charts       []ChartReport `json:"charts"`
}

// ChartReport is one chart or fix entry of a report.
type ChartReport struct {
	Chart        string        `json:"chart"`
	ChartVersion string        `json:"chartVersion"`
	FixOf        string        `json:"fixOf"`
	Worlds       []WorldReport `json:"worlds"`
	Certificate  *CertReport   `json:"certificate"`
}

// WorldReport is one compared world of a chart.
type WorldReport struct {
	World             string      `json:"world"`
	Verdict           string      `json:"verdict"`
	UndecidedReason   string      `json:"undecidedReason"`
	RunsUsed          []int       `json:"runsUsed"`
	RunsLeftOut       []LeftOut   `json:"runsLeftOut"`
	OracleRunsUsed    []int       `json:"oracleRunsUsed"`
	OracleRunsLeftOut []LeftOut   `json:"oracleRunsLeftOut"`
	Keys              []KeyReport `json:"keys"`
}

// LeftOut is a run difftable did not use.
type LeftOut struct {
	Run    int    `json:"run"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// KeyReport is one observation key in one world.
type KeyReport struct {
	Key         string   `json:"key"`
	Class       string   `json:"class"`
	Verdict     string   `json:"verdict"`
	Reason      string   `json:"reason"`
	OracleValue *string  `json:"oracleValue"`
	WorldValues []string `json:"worldValues"`
	Misses      int      `json:"misses"`
}

// CertReport is the certificate of a fix entry.
type CertReport struct {
	Fix               string       `json:"fix"`
	Base              string       `json:"base"`
	Certified         bool         `json:"certified"`
	Cleared           []string     `json:"cleared"`
	Problems          []string     `json:"problems"`
	CellWorlds        []string     `json:"cellWorlds"`
	Ignored           []IgnoreCell `json:"ignored"`
	WorldsNotCompared []IgnoreCell `json:"worldsNotCompared"`
}

// IgnoreCell is an ignore-list entry.
type IgnoreCell struct {
	Chart  string `json:"chart"`
	World  string `json:"world"`
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// PodMesh is the podMesh entry of an observation record (hack/compat-exp/RECORD.md).
type PodMesh struct {
	Pod          string `json:"pod"`
	Client       bool   `json:"client"`
	Owner        string `json:"owner"`
	MeshMetadata []struct {
		Kind  string `json:"kind"`
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"meshMetadata"`
	IstioProxy struct {
		Container     bool `json:"container"`
		InitContainer bool `json:"initContainer"`
		Native        bool `json:"native"`
	} `json:"istioProxy"`
	AmbientRedirection string `json:"ambientRedirection"`
}

// Record is the part of an observation record crossrun reads.
type Record struct {
	World   string    `json:"world"`
	Chart   string    `json:"chart"`
	Run     int       `json:"run"`
	PodMesh []PodMesh `json:"podMesh"`
}

// StaticResult is one row of the static-check input file.
type StaticResult struct {
	Chart   string `json:"chart"`
	World   string `json:"world"`
	Check   string `json:"check"`
	Flagged bool   `json:"flagged"`
}

// FrameChart is one chart of the frozen chart list.
type FrameChart struct {
	ID        string `json:"id"`
	Publisher string `json:"publisher"`
	Qualified *bool  `json:"qualified"`
}

// Manifest names the input files. Paths are relative to the manifest.
type Manifest struct {
	Runs          []ManifestRun  `json:"runs"`
	Certification []ManifestCert `json:"certification"`
	Recheck       *ManifestCert  `json:"recheck"`
	Causes        string         `json:"causes"`
	Ledger        string         `json:"ledger"`
	Frame         string         `json:"frame"`
	Static        string         `json:"static"`
}

// ManifestRun is run 1 or run 2 of the sample.
type ManifestRun struct {
	ID      string `json:"id"`
	Report  string `json:"report"`
	Records string `json:"records"`
	Started string `json:"started"`
	Ended   string `json:"ended"`
}

// ManifestCert is a certification run or the recheck run.
type ManifestCert struct {
	ID     string `json:"id"`
	Report string `json:"report"`
	Istio  string `json:"istio"`
}

// RunInput is a loaded sample run.
type RunInput struct {
	ID      string
	Started time.Time
	Ended   time.Time
	Report  *Report
	Records []Record
	// HasRecords is true when a records directory was given.
	HasRecords bool
}

// CertInput is a loaded certification or recheck run.
type CertInput struct {
	ID     string
	Istio  string
	Report *Report
}

// Inputs is everything Analyze reads.
type Inputs struct {
	Runs          []RunInput
	Certification []CertInput
	Recheck       *CertInput
	Causes        *ledger.Causes
	Ledger        *ledger.File
	Publisher     map[string]string
	Qualified     map[string]bool
	FrameOrder    []string
	Static        []StaticResult
	StaticGiven   bool
}

func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing data after the JSON document")
	}
	return nil
}

func readJSON(path string, v any, strict bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strict {
		err = strictDecode(data, v)
	} else {
		err = json.Unmarshal(data, v)
	}
	if err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	return nil
}

func loadReport(path string) (*Report, error) {
	var r Report
	if err := readJSON(path, &r, false); err != nil {
		return nil, err
	}
	if r.Schema != 1 {
		return nil, fmt.Errorf("%s: report schema %d, crossrun reads schema 1", path, r.Schema)
	}
	return &r, nil
}

func loadRecords(dir string) ([]Record, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var out []Record
	for _, m := range matches {
		var r Record
		if err := readJSON(m, &r, false); err != nil {
			return nil, err
		}
		if r.Chart == "" || r.World == "" {
			return nil, fmt.Errorf("%s: record has no chart or world", m)
		}
		out = append(out, r)
	}
	return out, nil
}

func parseTime(path, field, v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return t, fmt.Errorf("%s: %s %q is not an RFC 3339 time", path, field, v)
	}
	return t, nil
}

// LoadInputs reads a manifest and every file it names.
func LoadInputs(manifestPath string) (*Inputs, error) {
	var m Manifest
	if err := readJSON(manifestPath, &m, true); err != nil {
		return nil, err
	}
	base := filepath.Dir(manifestPath)
	rel := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	in := &Inputs{}
	if len(m.Runs) < 1 || len(m.Runs) > 2 {
		return nil, fmt.Errorf("%s: runs must hold run 1, and run 2 once it exists", manifestPath)
	}
	for i, mr := range m.Runs {
		if mr.ID == "" || mr.Report == "" {
			return nil, fmt.Errorf("%s: runs[%d] needs id and report", manifestPath, i)
		}
		rep, err := loadReport(rel(mr.Report))
		if err != nil {
			return nil, err
		}
		ri := RunInput{ID: mr.ID, Report: rep}
		if ri.Started, err = parseTime(manifestPath, fmt.Sprintf("runs[%d].started", i), mr.Started); err != nil {
			return nil, err
		}
		if ri.Ended, err = parseTime(manifestPath, fmt.Sprintf("runs[%d].ended", i), mr.Ended); err != nil {
			return nil, err
		}
		if mr.Records != "" {
			if ri.Records, err = loadRecords(rel(mr.Records)); err != nil {
				return nil, err
			}
			ri.HasRecords = true
		}
		in.Runs = append(in.Runs, ri)
	}
	for i, mc := range m.Certification {
		if mc.ID == "" || mc.Report == "" {
			return nil, fmt.Errorf("%s: certification[%d] needs id and report", manifestPath, i)
		}
		rep, err := loadReport(rel(mc.Report))
		if err != nil {
			return nil, err
		}
		in.Certification = append(in.Certification, CertInput{ID: mc.ID, Istio: mc.Istio, Report: rep})
	}
	if m.Recheck != nil {
		if m.Recheck.ID == "" || m.Recheck.Report == "" {
			return nil, fmt.Errorf("%s: recheck needs id and report", manifestPath)
		}
		rep, err := loadReport(rel(m.Recheck.Report))
		if err != nil {
			return nil, err
		}
		in.Recheck = &CertInput{ID: m.Recheck.ID, Istio: m.Recheck.Istio, Report: rep}
	}
	if m.Causes == "" || m.Ledger == "" || m.Frame == "" {
		return nil, fmt.Errorf("%s: causes, ledger and frame are required", manifestPath)
	}
	var err error
	if in.Causes, err = ledger.LoadCauses(rel(m.Causes)); err != nil {
		return nil, err
	}
	if in.Ledger, err = ledger.Load(rel(m.Ledger)); err != nil {
		return nil, err
	}
	if ps := ledger.Check(in.Ledger, in.Causes); len(ps) > 0 {
		msgs := make([]string, len(ps))
		for i, p := range ps {
			msgs[i] = p.Error()
		}
		return nil, fmt.Errorf("ledger fails its schema check (run ledger check): %s", strings.Join(msgs, "; "))
	}
	if err := loadFrame(rel(m.Frame), in); err != nil {
		return nil, err
	}
	if m.Static != "" {
		var wrap struct {
			Results []StaticResult `json:"results"`
		}
		data, err := os.ReadFile(rel(m.Static))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &in.Static); err != nil {
			if err2 := json.Unmarshal(data, &wrap); err2 != nil {
				return nil, fmt.Errorf("%s: %v", m.Static, err)
			}
			in.Static = wrap.Results
		}
		in.StaticGiven = true
	}
	return in, nil
}

func loadFrame(path string, in *Inputs) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var list []FrameChart
	if err := json.Unmarshal(data, &list); err != nil {
		var wrap struct {
			Charts []FrameChart `json:"charts"`
		}
		if err2 := json.Unmarshal(data, &wrap); err2 != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
		list = wrap.Charts
	}
	in.Publisher = map[string]string{}
	in.Qualified = map[string]bool{}
	for i, c := range list {
		if c.ID == "" || c.Publisher == "" {
			return fmt.Errorf("%s: entry %d needs id and publisher", path, i+1)
		}
		if _, dup := in.Publisher[c.ID]; dup {
			return fmt.Errorf("%s: chart %q listed twice", path, c.ID)
		}
		in.Publisher[c.ID] = c.Publisher
		in.Qualified[c.ID] = c.Qualified == nil || *c.Qualified
		in.FrameOrder = append(in.FrameOrder, c.ID)
	}
	return nil
}
