package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

// AmbientNP is pilot 1's cause key. It is reported as prevalence and left out
// of N_new, P_new, D, P_D and S (section 1).
const AmbientNP = "ambient-np"

// GapHours is the least time between the end of run 1 and the start of run 2.
const GapHours = 12

// UniversalPercent and StaticPercent and UndecidedPercent are the thresholds
// of rule 5, section 1 (S) and section 7.
const (
	UniversalPercent = 90
	StaticPercent    = 10
	UndecidedPercent = 15
)

// universalClasses are the observation classes rule 5 names: E1 (apiserver),
// E2 (late), helm:test and ports.
var universalClasses = []string{"apiserver", "late", "helm:test", "port"}

// ambientWorlds are the worlds whose data plane is ambient; the others are
// sidecar worlds. It decides which opt-out metadata is effective.
var ambientWorlds = map[string]bool{"W3": true}

// Cell statuses.
const (
	StCounted      = "counted"
	StExcluded     = "excluded"
	StUnexplained  = "unexplained"
	StUnresolved   = "unresolved"
	StNotReproduce = "not-reproduced"
	StAwaitRun2    = "awaiting-run-2"
)

// Mechanical and asserted exclusion reasons in the order rule 5 lists them;
// a cell's primary reason is the first that applies.
var reasonOrder = []string{
	ledger.ReasonPullFailure, ledger.ReasonCapacity, ledger.ReasonFlaky, ledger.ReasonUndecided,
	ledger.ReasonCOptout, ledger.ReasonWorldUniversal,
}

// CellResult is one cell: a (chart, world, observation key) that is a finding
// in a run, or that a run left undecided with every used value differing.
type CellResult struct {
	Chart     string   `json:"chart"`
	World     string   `json:"world"`
	Key       string   `json:"key"`
	Class     string   `json:"class"`
	Publisher string   `json:"publisher"`
	Run1      string   `json:"run1"`
	Run2      string   `json:"run2,omitempty"`
	Holds     bool     `json:"holdsInBothRuns"`
	Reasons   []string `json:"exclusionReasons,omitempty"`
	Status    string   `json:"status"`
	Credit    string   `json:"credit,omitempty"`
	Type      string   `json:"type,omitempty"`
	Detail    string   `json:"detail,omitempty"`
}

func (c CellResult) cell() ledger.Cell {
	return ledger.Cell{Chart: c.Chart, World: c.World, Key: c.Key}
}

// UniversalRow is one world-universal computation, with its denominator.
type UniversalRow struct {
	Kind        string   `json:"kind"` // class or cause
	Name        string   `json:"name"`
	World       string   `json:"world"`
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Universal   bool     `json:"universal"`
	Computed    bool     `json:"computed"`
	Note        string   `json:"note,omitempty"`
	Differing   []string `json:"differingCharts,omitempty"`
}

// OptOutPod is one pod of a record with its mesh opt-out metadata.
type OptOutPod struct {
	Run       string   `json:"run"`
	Pod       string   `json:"pod"`
	Owner     string   `json:"owner"`
	Client    bool     `json:"client"`
	Metadata  []string `json:"metadata"`
	Effective bool     `json:"effectiveOptOut"`
	Proxy     bool     `json:"istioProxy"`
	Redirect  string   `json:"ambientRedirection,omitempty"`
}

// OptOutEvidence is the podMesh evidence for a (chart, world).
type OptOutEvidence struct {
	Chart     string      `json:"chart"`
	World     string      `json:"world"`
	Asserted  bool        `json:"assertedByLedger"`
	Effective int         `json:"podsWithEffectiveOptOut"`
	Pods      []OptOutPod `json:"pods"`
}

// UndecidedWorld is a chart's world a run could not decide.
type UndecidedWorld struct {
	Run    string `json:"run"`
	Chart  string `json:"chart"`
	World  string `json:"world"`
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

// Prevalence is the ambient-np report.
type Prevalence struct {
	Cells  int      `json:"countedCells"`
	Charts []string `json:"charts"`
}

// StaticKey is the S test of one new key.
type StaticKey struct {
	Key        string        `json:"key"`
	Check      string        `json:"check"`
	FlagsAll   bool          `json:"flagsEveryCountedFinding"`
	Worlds     []StaticWorld `json:"worlds"`
	Qualifies  bool          `json:"qualifies"`
	Unflagged  []ledger.Cell `json:"unflaggedFindings,omitempty"`
	CountedFor []ledger.Cell `json:"countedCells"`
	// NoCheck: the key has no static check (staticCheck null in causes.json),
	// so it cannot be in S.
	NoCheck bool `json:"noStaticCheck,omitempty"`
	// NoCheckReason says why: NoCheckNull for a frozen key whose staticCheck is
	// null, NoCheckAfterFreeze for a key admitted after the freeze.
	NoCheckReason string `json:"noStaticCheckReason,omitempty"`
	// NotApplicable lists counted findings in a world the check does not apply
	// to. A check that cannot look at a world cannot flag a finding there, so
	// the key does not qualify (and the cell is not reported as "not flagged").
	NotApplicable []ledger.Cell `json:"notApplicableFindings,omitempty"`
}

// StaticWorld is the 10% test in one world.
type StaticWorld struct {
	World       string `json:"world"`
	SameCharts  int    `json:"chartsSame"`
	FlaggedSame int    `json:"flaggedOfThose"`
	Ok          bool   `json:"atMostTenPercent"`
	// NotApplicable: the check does not apply to this world, so there is no
	// 10% test to run (and no "same" chart is counted as not flagged).
	NotApplicable bool `json:"notApplicable,omitempty"`
}

// Futility is section 7's bound test.
type Futility struct {
	NMax                      int      `json:"nMax"`
	PMax                      int      `json:"pMax"`
	PendingCells              int      `json:"cellsNeitherCreditedNorExcluded"`
	PendingPublishers         int      `json:"newPublishersAmongThem"`
	Reachable                 []string `json:"reachableRegions"`
	OnlyE                     bool     `json:"onlyRegionEReachable"`
	StopDrop                  bool     `json:"stopDrop"`
	PrevalenceCertificatesOff bool     `json:"prevalenceCertificatesSkipped"`
}

// UndecidedShare is the 15% stop.
type UndecidedShare struct {
	Undecided   int    `json:"undecidedCells"`
	Cells       int    `json:"cells"`
	Over        bool   `json:"overFifteenPercent"`
	NoteAfterRR string `json:"note"`
}

// Hypothetical is the region if all open cells counted.
type Hypothetical struct {
	Cells   int       `json:"openCellsCounted"`
	Metrics Metrics   `json:"metrics"`
	Region  RegionRow `json:"region"`
}

// Result is crossrun's whole output.
type Result struct {
	Schema   int     `json:"schema"`
	Run1     string  `json:"run1"`
	Run2     string  `json:"run2,omitempty"`
	GapHours float64 `json:"gapHours,omitempty"`
	GapOK    bool    `json:"gapOk"`
	// Final is false for run 1 only: that output serves the futility check
	// after run 1 and declares no region.
	Final          bool             `json:"final"`
	Warnings       []string         `json:"warnings"`
	Cells          []CellResult     `json:"cells"`
	Universal      []UniversalRow   `json:"worldUniversal"`
	OptOut         []OptOutEvidence `json:"optOutEvidence"`
	UndecidedWorld []UndecidedWorld `json:"undecidedWorlds"`
	Prevalence     Prevalence       `json:"ambientNpPrevalence"`
	Metrics        Metrics          `json:"metrics"`
	SComputed      bool             `json:"sComputed"`
	Static         []StaticKey      `json:"staticChecks"`
	Region         RegionRow        `json:"region"`
	Trace          []RowTrace       `json:"firstMatchTrace"`
	Futility       Futility         `json:"futility"`
	Undecided      UndecidedShare   `json:"undecidedShare"`
	Unresolved     []ledger.Cell    `json:"unresolved"`
	Unexplained    []ledger.Cell    `json:"unexplained"`
	IfAllCounted   Hypothetical     `json:"ifAllCounted"`
}

type cellState struct {
	kind     string // finding, flaky, pull-failure, capacity, undecided
	evidence bool   // undecided key whose used values all differ from the oracle
}

type chartWorld struct{ chart, world string }

type runView struct {
	states  map[ledger.Cell]cellState
	present map[chartWorld]bool
	verdict map[chartWorld]string
	classes map[chartWorld]map[string]bool
	charts  map[string]bool
	keyCls  map[ledger.Cell]string
}

func classID(k KeyReport) string {
	if k.Class == "helm" {
		return k.Key
	}
	return k.Class
}

func allDiffer(k KeyReport) bool {
	if k.OracleValue == nil || len(k.WorldValues) == 0 {
		return false
	}
	for _, v := range k.WorldValues {
		if v == *k.OracleValue {
			return false
		}
	}
	return true
}

func worldKind(w WorldReport) string {
	has := func(reason string) bool {
		for _, l := range append(append([]LeftOut{}, w.RunsLeftOut...), w.OracleRunsLeftOut...) {
			if l.Reason == reason {
				return true
			}
		}
		return false
	}
	switch {
	case has("pull-failure"):
		return ledger.ReasonPullFailure
	case has("capacity"):
		return ledger.ReasonCapacity
	}
	return ledger.ReasonUndecided
}

func buildView(name string, rep *Report, in *Inputs) (*runView, []UndecidedWorld, error) {
	v := &runView{
		states: map[ledger.Cell]cellState{}, present: map[chartWorld]bool{}, verdict: map[chartWorld]string{},
		classes: map[chartWorld]map[string]bool{}, charts: map[string]bool{}, keyCls: map[ledger.Cell]string{},
	}
	var uw []UndecidedWorld
	for _, ch := range rep.Charts {
		if ch.FixOf != "" {
			continue
		}
		if _, ok := in.Publisher[ch.Chart]; !ok {
			return nil, nil, fmt.Errorf("run %s: chart %q is not in the frame (no publisher known)", name, ch.Chart)
		}
		if !in.Qualified[ch.Chart] {
			return nil, nil, fmt.Errorf("run %s: chart %q is in the frame as not qualified", name, ch.Chart)
		}
		v.charts[ch.Chart] = true
		for _, w := range ch.Worlds {
			cw := chartWorld{ch.Chart, w.World}
			v.present[cw] = true
			v.verdict[cw] = w.Verdict
			v.classes[cw] = map[string]bool{}
			if w.Verdict == "undecided" {
				uw = append(uw, UndecidedWorld{Run: name, Chart: ch.Chart, World: w.World, Kind: worldKind(w), Reason: w.UndecidedReason})
			}
			for _, k := range w.Keys {
				cell := ledger.Cell{Chart: ch.Chart, World: w.World, Key: k.Key}
				v.classes[cw][classID(k)] = true
				v.keyCls[cell] = classID(k)
				switch k.Verdict {
				case "differs":
					if len(w.RunsUsed) < rep.RunsRequired || len(w.OracleRunsUsed) < rep.RunsRequired ||
						k.OracleValue == nil || k.Misses != len(k.WorldValues) || len(k.WorldValues) < rep.RunsRequired {
						return nil, nil, fmt.Errorf("run %s: %s is reported as differs without %d of %d agreeing oracle runs and %d differing runs",
							name, cell, rep.RunsRequired, rep.RunsRequired, rep.RunsRequired)
					}
					v.states[cell] = cellState{kind: "finding"}
				case "flaky":
					v.states[cell] = cellState{kind: "flaky"}
				case "undecided":
					v.states[cell] = cellState{kind: worldKind(w), evidence: allDiffer(k)}
				}
			}
		}
	}
	return v, uw, nil
}

// lookup returns a run's state for a cell: its recorded state, "same" when the
// chart's world was compared without one, "missing" when the run has no such
// chart or world.
func (v *runView) lookup(c ledger.Cell) string {
	if st, ok := v.states[c]; ok {
		return st.kind
	}
	if v.present[chartWorld{c.Chart, c.World}] {
		return "same"
	}
	return "missing"
}

func (v *runView) isCell(c ledger.Cell) bool {
	st, ok := v.states[c]
	return ok && (st.kind == "finding" || st.kind == "flaky" || st.evidence)
}

type analysis struct {
	in       *Inputs
	views    []*runView
	rowOf    map[ledger.Cell]*ledger.Row
	cells    []*CellResult
	byCell   map[ledger.Cell]*CellResult
	gapOK    bool
	errs     []string
	universe []UniversalRow
}

func (a *analysis) errf(format string, args ...any) {
	a.errs = append(a.errs, fmt.Sprintf(format, args...))
}

func rowLabel(i int, r ledger.Row) string {
	return fmt.Sprintf("ledger row %d (%s)", i+1, r.Cell())
}

// Analyze joins the runs, the ledger and the cause list into the result. It
// returns an error naming every ledger row that disagrees with the reports.
func Analyze(in *Inputs) (*Result, error) {
	a := &analysis{in: in, rowOf: map[ledger.Cell]*ledger.Row{}, byCell: map[ledger.Cell]*CellResult{}}
	res := &Result{Schema: 1, Warnings: []string{}}
	if len(in.Runs) < 1 {
		return nil, fmt.Errorf("no run given")
	}
	res.Run1 = in.Runs[0].ID
	if len(in.Runs) == 2 {
		res.Run2 = in.Runs[1].ID
	}
	for _, r := range in.Runs {
		if r.Report.RunsRequired < 3 {
			return nil, fmt.Errorf("run %s: report requires %d runs per world, rule 1 needs 3 of 3", r.ID, r.Report.RunsRequired)
		}
		if r.Ended.Before(r.Started) {
			return nil, fmt.Errorf("run %s: ended before it started", r.ID)
		}
	}
	if len(in.Runs) == 2 {
		gap := in.Runs[1].Started.Sub(in.Runs[0].Ended)
		res.GapHours = gap.Hours()
		// A short gap is an operator slip, not a result: no region, no futility
		// verdict (INTERPRETATIONS.md 2).
		if gap < GapHours*time.Hour {
			return nil, fmt.Errorf("run 2 (%s) started at %s, only %.2f h after run 1 (%s) ended at %s; rule 1 needs at least %d h. "+
				"This is not a result (no region, no futility verdict): run the sample again at least %d h after run 1 ended",
				in.Runs[1].ID, in.Runs[1].Started.Format(time.RFC3339), gap.Hours(), in.Runs[0].ID, in.Runs[0].Ended.Format(time.RFC3339), GapHours, GapHours)
		}
		a.gapOK = true
	}
	res.Final = len(in.Runs) == 2
	res.GapOK = a.gapOK

	var uworlds []UndecidedWorld
	for i, r := range in.Runs {
		v, uw, err := buildView(fmt.Sprintf("%d (%s)", i+1, r.ID), r.Report, in)
		if err != nil {
			return nil, err
		}
		a.views = append(a.views, v)
		for j := range uw {
			uw[j].Run = r.ID
		}
		uworlds = append(uworlds, uw...)
	}
	for i := range in.Ledger.Rows {
		r := &in.Ledger.Rows[i]
		a.rowOf[r.Cell()] = r
	}

	a.enumerate()
	a.universal(res)
	a.assignStatus()
	a.checkRows()
	if len(a.errs) == 0 {
		a.finish(res, uworlds)
	}
	if len(a.errs) > 0 {
		sort.Strings(a.errs)
		return nil, fmt.Errorf("ledger and reports disagree:\n  %s", strings.Join(dedupe(a.errs), "\n  "))
	}
	return res, nil
}

func reasonRank(r string) int {
	for i, x := range reasonOrder {
		if x == r {
			return i
		}
	}
	return len(reasonOrder)
}

func (a *analysis) enumerate() {
	seen := map[ledger.Cell]bool{}
	var list []ledger.Cell
	for _, v := range a.views {
		for c := range v.states {
			if v.isCell(c) && !seen[c] {
				seen[c] = true
				list = append(list, c)
			}
		}
	}
	sort.Slice(list, func(i, j int) bool { return cellLess(list[i], list[j]) })
	for _, c := range list {
		cr := &CellResult{Chart: c.Chart, World: c.World, Key: c.Key, Publisher: a.in.Publisher[c.Chart]}
		for _, v := range a.views {
			if cl, ok := v.keyCls[c]; ok {
				cr.Class = cl
			}
		}
		states := make([]string, len(a.views))
		for i, v := range a.views {
			states[i] = v.lookup(c)
		}
		cr.Run1 = states[0]
		if len(states) == 2 {
			cr.Run2 = states[1]
		}
		reasons := map[string]bool{}
		allFinding, anyFinding := true, false
		for _, s := range states {
			switch s {
			case "finding":
				anyFinding = true
			case "flaky":
				reasons[ledger.ReasonFlaky] = true
				allFinding = false
			case ledger.ReasonPullFailure, ledger.ReasonCapacity, ledger.ReasonUndecided:
				reasons[s] = true
				allFinding = false
			case "missing":
				reasons[ledger.ReasonUndecided] = true
				allFinding = false
			default:
				allFinding = false
			}
		}
		cr.Holds = allFinding && anyFinding && len(states) == 2 && a.gapOK
		for r := range reasons {
			cr.Reasons = append(cr.Reasons, r)
		}
		sort.Slice(cr.Reasons, func(i, j int) bool { return reasonRank(cr.Reasons[i]) < reasonRank(cr.Reasons[j]) })
		switch {
		case len(cr.Reasons) > 0:
			cr.Status = StExcluded
		case allFinding && len(states) == 1:
			cr.Status = StAwaitRun2
		case allFinding:
			cr.Status = StUnresolved // refined in assignStatus
		default:
			cr.Status = StNotReproduce
			cr.Detail = fmt.Sprintf("differs in one run only (run 1 %s, run 2 %s)", cr.Run1, cr.Run2)
		}
		a.cells = append(a.cells, cr)
		a.byCell[c] = cr
	}
}

func cellLess(a, b ledger.Cell) bool {
	if a.Chart != b.Chart {
		return a.Chart < b.Chart
	}
	if a.World != b.World {
		return a.World < b.World
	}
	return a.Key < b.Key
}

// candidate reports whether the cell would hold under rule 1 if the gap and the
// second run are set aside: a finding in every run given.
func (a *analysis) candidate(cr *CellResult) bool {
	for _, v := range a.views {
		if v.lookup(cr.cell()) != "finding" {
			return false
		}
	}
	return true
}

// compared reports whether every run compared the chart in the world.
func (a *analysis) compared(chart, world string) bool {
	for _, v := range a.views {
		if !v.present[chartWorld{chart, world}] {
			return false
		}
	}
	return true
}

func (a *analysis) qualifiedCharts() []string {
	var out []string
	for _, id := range a.in.FrameOrder {
		if !a.in.Qualified[id] {
			continue
		}
		ok := true
		for _, v := range a.views {
			if !v.charts[id] {
				ok = false
			}
		}
		if ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (a *analysis) universal(res *Result) {
	charts := a.qualifiedCharts()
	worldSet := map[string]bool{}
	for _, v := range a.views {
		for cw := range v.present {
			worldSet[cw.world] = true
		}
	}
	var worlds []string
	for w := range worldSet {
		worlds = append(worlds, w)
	}
	sort.Strings(worlds)

	heldClass := map[chartWorld]map[string]bool{}
	for _, cr := range a.cells {
		if a.candidate(cr) {
			cw := chartWorld{cr.Chart, cr.World}
			if heldClass[cw] == nil {
				heldClass[cw] = map[string]bool{}
			}
			heldClass[cw][cr.classKey()] = true
		}
	}
	universalClass := map[chartWorld]bool{} // keyed by {class, world}
	for _, w := range worlds {
		for _, cls := range universalClasses {
			row := UniversalRow{Kind: "class", Name: cls, World: w, Computed: true}
			for _, ch := range charts {
				cw := chartWorld{ch, w}
				has := false
				for _, v := range a.views {
					if v.classes[cw][cls] {
						has = true
					}
				}
				if !has {
					continue
				}
				row.Denominator++
				if heldClass[cw][cls] {
					row.Numerator++
					row.Differing = append(row.Differing, ch)
				}
			}
			if row.Denominator == 0 {
				continue
			}
			row.Universal = row.Numerator*100 >= UniversalPercent*row.Denominator
			universalClass[chartWorld{cls, w}] = row.Universal
			a.universe = append(a.universe, row)
		}
	}
	// Cause keys: denominator = every qualified chart compared in the world;
	// numerator = those with a held cell credited to the key (INTERPRETATIONS.md).
	keys := append([]string{}, a.in.Causes.Keys...)
	for _, nk := range a.in.Ledger.NewKeys {
		keys = append(keys, nk.Key)
	}
	credited := map[string]map[chartWorld]bool{}
	for i := range a.in.Ledger.Rows {
		r := &a.in.Ledger.Rows[i]
		cr := a.byCell[r.Cell()]
		if cr == nil || !a.candidate(cr) {
			continue
		}
		k, err := a.credit(i, r)
		if err != nil {
			a.errf("%v", err)
			continue
		}
		cr.Credit = k
		if k == "" {
			continue
		}
		if credited[k] == nil {
			credited[k] = map[chartWorld]bool{}
		}
		credited[k][chartWorld{r.Chart, r.World}] = true
	}
	universalCause := map[chartWorld]bool{}
	for _, k := range keys {
		for _, w := range worlds {
			row := UniversalRow{Kind: "cause", Name: k, World: w, Computed: true}
			for _, ch := range charts {
				if !a.compared(ch, w) {
					continue
				}
				row.Denominator++
				if credited[k][chartWorld{ch, w}] {
					row.Numerator++
					row.Differing = append(row.Differing, ch)
				}
			}
			if row.Numerator == 0 {
				continue
			}
			row.Universal = row.Numerator*100 >= UniversalPercent*row.Denominator
			universalCause[chartWorld{k, w}] = row.Universal
			a.universe = append(a.universe, row)
		}
	}
	sort.SliceStable(a.universe, func(i, j int) bool {
		x, y := a.universe[i], a.universe[j]
		if x.Kind != y.Kind {
			return x.Kind < y.Kind
		}
		if x.World != y.World {
			return x.World < y.World
		}
		return x.Name < y.Name
	})
	res.Universal = a.universe
	// mark cells
	for _, cr := range a.cells {
		if !a.candidate(cr) {
			continue
		}
		if universalClass[chartWorld{cr.classKey(), cr.World}] || (cr.Credit != "" && universalCause[chartWorld{cr.Credit, cr.World}]) {
			cr.Reasons = append(cr.Reasons, ledger.ReasonWorldUniversal)
		}
	}
}

// classKey is the class id used by the world-universal rule for the cell.
func (c *CellResult) classKey() string {
	if c.Class == "helm" {
		return c.Key
	}
	return c.Class
}

// Why a key has no static check.
const (
	NoCheckNull        = "no static check: staticCheck is null in causes.json"
	NoCheckAfterFreeze = "no static check: admitted after the freeze"
)

// checkID is the id of the key's static check. ok is false, with the reason,
// for a frozen key whose causes.json entry has staticCheck null and for a key
// outside causes.json: a key admitted after the freeze has no check in the
// frozen instrument, so it never earns S and static results are not required
// for it.
func (a *analysis) checkID(key string) (id string, ok bool, why string) {
	if !a.in.Causes.Has(key) {
		return "", false, NoCheckAfterFreeze
	}
	id, ok = a.in.Causes.Static[key]
	if !ok {
		return "", false, NoCheckNull
	}
	return id, true, ""
}

// staticRow finds the static result for a chart, world and check. applicable
// is the file's flag and, for a frozen key, also the key's `worlds` in
// causes.json.
func (a *analysis) staticRow(chart, world, key, check string) (flagged, applicable, found bool) {
	for i := range a.in.Static {
		s := &a.in.Static[i]
		if s.Chart == chart && s.World == world && s.Check == check {
			applicable = *s.Applicable
			if a.in.Causes.Has(key) && !contains(a.in.Causes.Worlds[key], world) {
				applicable = false
			}
			return s.Flagged && applicable, applicable, true
		}
	}
	return false, false, false
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// credit applies rule 3 to one ledger row: the cause itself, or, when the row
// holds parts, the key whose part alone returns the cell to the oracle's value,
// else the first key of the parts in cause-list order.
func (a *analysis) credit(i int, r *ledger.Row) (string, error) {
	if len(r.Parts) == 0 {
		return r.Cause, nil
	}
	var returning []string
	for _, p := range r.Parts {
		returns := true
		for _, id := range p.Runs {
			ci := a.certRun(id)
			if ci == nil {
				return "", fmt.Errorf("%s: part %s names run %s, which is not a certification run in the manifest", rowLabel(i, *r), p.Cause, id)
			}
			ch := findChart(ci.Report, p.Entry)
			if ch == nil {
				return "", fmt.Errorf("%s: part %s: fix entry %q is not in run %s", rowLabel(i, *r), p.Cause, p.Entry, id)
			}
			if ch.FixOf != r.Chart {
				return "", fmt.Errorf("%s: part %s: fix entry %q repairs %q in run %s, not %q (fixOf unknown for this chart)",
					rowLabel(i, *r), p.Cause, p.Entry, ch.FixOf, id, r.Chart)
			}
			kr := findKey(ch, r.World, r.Key)
			if kr == nil || kr.Verdict != "same" {
				returns = false
			}
		}
		if returns {
			returning = append(returning, p.Cause)
		}
	}
	pick := returning
	if len(returning) != 1 {
		pick = nil
		for _, p := range r.Parts {
			pick = append(pick, p.Cause)
		}
	}
	sort.SliceStable(pick, func(x, y int) bool {
		return a.in.Causes.Order(a.in.Ledger, pick[x]) < a.in.Causes.Order(a.in.Ledger, pick[y])
	})
	return pick[0], nil
}

func (a *analysis) certRun(id string) *CertInput {
	for i := range a.in.Certification {
		if a.in.Certification[i].ID == id {
			return &a.in.Certification[i]
		}
	}
	return nil
}

func findChart(rep *Report, id string) *ChartReport {
	for i := range rep.Charts {
		if rep.Charts[i].Chart == id {
			return &rep.Charts[i]
		}
	}
	return nil
}

func findKey(ch *ChartReport, world, key string) *KeyReport {
	for _, w := range ch.Worlds {
		if w.World != world {
			continue
		}
		for i := range w.Keys {
			if w.Keys[i].Key == key {
				return &w.Keys[i]
			}
		}
	}
	return nil
}

func (a *analysis) assignStatus() {
	for _, cr := range a.cells {
		if cr.Status == StNotReproduce {
			continue
		}
		row := a.rowOf[cr.cell()]
		if cr.Status == StExcluded {
			continue
		}
		if cr.Status == StAwaitRun2 {
			if len(cr.Reasons) > 0 {
				cr.Status = StExcluded
			}
			continue
		}
		// the cell holds in both runs
		if row != nil && row.Status == ledger.StatusExcluded && row.Exclusion != nil &&
			row.Exclusion.Reason == ledger.ReasonCOptout {
			cr.Reasons = append(cr.Reasons, ledger.ReasonCOptout)
		}
		sort.SliceStable(cr.Reasons, func(i, j int) bool { return reasonRank(cr.Reasons[i]) < reasonRank(cr.Reasons[j]) })
		if len(cr.Reasons) > 0 {
			cr.Status = StExcluded
			continue
		}
		switch {
		case row == nil:
			cr.Status = StUnresolved
			cr.Detail = "no ledger row"
		case row.Status == ledger.StatusCounted:
			cr.Status = StCounted
		case row.Status == ledger.StatusUnexplain:
			cr.Status = StUnexplained
		default:
			// excluded rows with a reason crossrun cannot confirm stay open here;
			// checkRows stops with an error naming the row
			cr.Status = StUnresolved
		}
	}
	// the world-universal mark was added to held cells before this point
	for _, cr := range a.cells {
		if cr.Status == StUnresolved || cr.Status == StCounted || cr.Status == StUnexplained {
			if contains(cr.Reasons, ledger.ReasonWorldUniversal) {
				cr.Status = StExcluded
			}
		}
	}
}

func dedupe(l []string) []string {
	var out []string
	for i, s := range l {
		if i == 0 || s != l[i-1] {
			out = append(out, s)
		}
	}
	return out
}
