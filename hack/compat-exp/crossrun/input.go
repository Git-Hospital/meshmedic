package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
	// OracleInstall is difftable's count of the oracle (W0) installs that
	// worked (obs["helm:install"] == "ok"): half of the evidence for "W0 is
	// 3 of 3" in the replacement rule (sample.go).
	OracleInstall OracleInstall `json:"oracleInstall"`
}

// OracleInstall mirrors difftable's field of the same name.
type OracleInstall struct {
	Runs   int    `json:"runs"`
	Ok     int    `json:"ok"`
	Failed int    `json:"failed"`
	Status string `json:"status"`
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
	// TestPodMesh holds the pods that existed after `helm test` and not in the
	// first snapshot (hack/compat-exp/RECORD.md), in the shape of PodMesh. The
	// c-optout check looks for the named callers here as well as in PodMesh.
	TestPodMesh       []PodMesh `json:"testPodMesh"`
	TestPodMeshStatus string    `json:"testPodMeshStatus"`
}

// callerPods is every pod a c-optout caller can name: podMesh (the snapshot
// before helm test) and testPodMesh (the pods helm test created).
func (r Record) callerPods() []PodMesh {
	all := make([]PodMesh, 0, len(r.PodMesh)+len(r.TestPodMesh))
	all = append(all, r.PodMesh...)
	return append(all, r.TestPodMesh...)
}

// StaticResult is one row of the static-check input file, which
// hack/compat-exp/static.sh writes: one row per chart, world and check.
// A check that does not apply to the world has Applicable false (and Flagged
// false, which then means nothing).
type StaticResult struct {
	Chart      string `json:"chart"`
	World      string `json:"world"`
	Check      string `json:"check"`
	Applicable *bool  `json:"applicable"`
	Flagged    bool   `json:"flagged"`
}

// FrameChart is one chart or reserve of the frozen chart list
// (charts-exp.json, written by select.sh).
type FrameChart struct {
	ID        string `json:"id"`
	Publisher string `json:"publisher"`
	Qualified *bool  `json:"qualified"`
}

// Manifest names the input files. Paths are relative to the manifest.
type Manifest struct {
	Runs []ManifestRun `json:"runs"`
	// Reruns are the separately dispatched observe runs, each restricted to one
	// chart, that serve as the rerun of that chart's W0 job (sample.go).
	Reruns        []ManifestRun  `json:"reruns"`
	Certification []ManifestCert `json:"certification"`
	Recheck       *ManifestCert  `json:"recheck"`
	Causes        string         `json:"causes"`
	Ledger        string         `json:"ledger"`
	Frame         string         `json:"frame"`
	Static        string         `json:"static"`
	// Replacements is the optional data file of section 4 replacements.
	Replacements string `json:"replacements"`
	// Provenance is what analyze.sh records about the freeze, the runs and the
	// files read; crossrun carries it into the result unread.
	Provenance json.RawMessage `json:"provenance"`
	// TestMode marks a run made with analyze.sh's test overrides.
	TestMode bool `json:"testMode"`
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

// RunInput is a loaded sample run (or a rerun of one chart).
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
	Runs []RunInput
	// Reruns are the loaded reruns, in manifest order.
	Reruns        []RunInput
	Certification []CertInput
	Recheck       *CertInput
	Causes        *ledger.Causes
	Ledger        *ledger.File
	Publisher     map[string]string
	Qualified     map[string]bool
	FrameOrder    []string
	// Reserves are the frame's reserves, in list order; the primary charts are
	// the other entries of FrameOrder.
	Reserves     []string
	Replacements []Replacement
	// RerunRefs are the rerun pairs of the replacements file's `reruns` list.
	RerunRefs   []RerunRef
	Provenance  json.RawMessage
	TestMode    bool
	Static      []StaticResult
	StaticGiven bool
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

// RecordLayout is the layout of a records directory, the one the workflow's report job
// uploads under results/: one subdirectory per observe artifact, named
// exp-<run id>-<chart>-<world>, each holding only regular files named
// <chart>.<world>.run<N>.json (N 1 to 3) whose own chart, world and run fields say the
// same. Anything else is an error: a flat directory, a link, an extra file, a record
// that belongs to another chart or world, a directory of another run, and no record at
// all.
var (
	expDirRe    = regexp.MustCompile(`^exp-([0-9]{1,20})-([a-z0-9][a-z0-9-]*)-(W[0-9])$`)
	recordFile  = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)\.(W[0-9])\.run([1-3])\.json$`)
	errNoRecord = "no records"
)

func loadRecords(dir, runID string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		m := expDirRe.FindStringSubmatch(e.Name())
		sub := filepath.Join(dir, e.Name())
		if m == nil || !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s: %q is not a directory named exp-<run id>-<chart>-<world>; the records directory holds one such directory per observe artifact (a flat directory of records is not the layout of the report artifact)", dir, e.Name())
		}
		if m[1] != runID {
			return nil, fmt.Errorf("%s: the directory %q belongs to run %s, not run %s", dir, e.Name(), m[1], runID)
		}
		files, err := os.ReadDir(sub)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			fm := recordFile.FindStringSubmatch(f.Name())
			path := filepath.Join(sub, f.Name())
			if fm == nil || !f.Type().IsRegular() {
				return nil, fmt.Errorf("%s: unexpected entry %q (only regular files <chart>.<world>.run<1-3>.json are records)", sub, f.Name())
			}
			if fm[1] != m[2] || fm[2] != m[3] {
				return nil, fmt.Errorf("%s: the file name says chart %s world %s, the directory says chart %s world %s", path, fm[1], fm[2], m[2], m[3])
			}
			var r Record
			if err := readJSON(path, &r, false); err != nil {
				return nil, err
			}
			if r.Chart != m[2] || r.World != m[3] || fmt.Sprint(r.Run) != fm[3] {
				return nil, fmt.Errorf("%s: the record says chart %q world %q run %d, its directory and file name say chart %s world %s run %s", path, r.Chart, r.World, r.Run, m[2], m[3], fm[3])
			}
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: %s found (expected exp-%s-<chart>-<world>/<chart>.<world>.run<N>.json)", dir, errNoRecord, runID)
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
	loadRun := func(what string, i int, mr ManifestRun) (RunInput, error) {
		if mr.ID == "" || mr.Report == "" {
			return RunInput{}, fmt.Errorf("%s: %s[%d] needs id and report", manifestPath, what, i)
		}
		rep, err := loadReport(rel(mr.Report))
		if err != nil {
			return RunInput{}, err
		}
		ri := RunInput{ID: mr.ID, Report: rep}
		if ri.Started, err = parseTime(manifestPath, fmt.Sprintf("%s[%d].started", what, i), mr.Started); err != nil {
			return ri, err
		}
		if ri.Ended, err = parseTime(manifestPath, fmt.Sprintf("%s[%d].ended", what, i), mr.Ended); err != nil {
			return ri, err
		}
		if mr.Records != "" {
			if ri.Records, err = loadRecords(rel(mr.Records), mr.ID); err != nil {
				return ri, err
			}
			ri.HasRecords = true
		}
		return ri, nil
	}
	for i, mr := range m.Runs {
		ri, err := loadRun("runs", i, mr)
		if err != nil {
			return nil, err
		}
		in.Runs = append(in.Runs, ri)
	}
	for i, mr := range m.Reruns {
		ri, err := loadRun("reruns", i, mr)
		if err != nil {
			return nil, err
		}
		in.Reruns = append(in.Reruns, ri)
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
	in.Provenance, in.TestMode = m.Provenance, m.TestMode
	if m.Replacements != "" {
		if in.Replacements, in.RerunRefs, err = LoadReplacements(rel(m.Replacements)); err != nil {
			return nil, err
		}
	}
	if m.Static != "" {
		data, err := os.ReadFile(rel(m.Static))
		if err != nil {
			return nil, err
		}
		if err := strictDecode(data, &in.Static); err != nil {
			return nil, fmt.Errorf("%s: %v (a list of chart, world, check, applicable, flagged, as static.sh writes it)", m.Static, err)
		}
		for i, s := range in.Static {
			if s.Chart == "" || s.World == "" || s.Check == "" || s.Applicable == nil {
				return nil, fmt.Errorf("%s: entry %d needs chart, world, check and applicable", m.Static, i+1)
			}
		}
		in.StaticGiven = true
	}
	return in, nil
}

// loadFrame reads the frozen chart list select.sh writes (charts-exp.json):
// `charts`, then `reserves`, each entry with id, publisher and qualified.
func loadFrame(path string, in *Inputs) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc struct {
		Charts   []FrameChart `json:"charts"`
		Reserves []FrameChart `json:"reserves"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %v (the frame is charts-exp.json: an object with charts and reserves)", path, err)
	}
	if len(doc.Charts) == 0 {
		return fmt.Errorf("%s: no charts (the frame is charts-exp.json: an object with charts and reserves)", path)
	}
	list := append(append([]FrameChart{}, doc.Charts...), doc.Reserves...)
	in.Publisher = map[string]string{}
	in.Qualified = map[string]bool{}
	for i, c := range list {
		if c.ID == "" || c.Publisher == "" || c.Qualified == nil {
			return fmt.Errorf("%s: entry %d needs id, publisher and qualified (select.sh writes all three)", path, i+1)
		}
		if _, dup := in.Publisher[c.ID]; dup {
			return fmt.Errorf("%s: chart %q listed twice", path, c.ID)
		}
		in.Publisher[c.ID] = c.Publisher
		in.Qualified[c.ID] = *c.Qualified
		in.FrameOrder = append(in.FrameOrder, c.ID)
		if i >= len(doc.Charts) {
			in.Reserves = append(in.Reserves, c.ID)
		}
	}
	return nil
}
