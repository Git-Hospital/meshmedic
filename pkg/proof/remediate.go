package proof

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Git-Hospital/meshmedic/pkg/catalog"
	"github.com/Git-Hospital/meshmedic/pkg/detect"
	"github.com/Git-Hospital/meshmedic/pkg/remediate"
)

// A remediation proof asks the question an operator actually has.
//
// Every spec in proof/ shows that the right entry fired and named the culprit.
// None of them show that merging what it proposed would have helped, and
// "opens the fix as a pull request" is the sentence this project leads with.
// It is the largest unverified claim in the product.
//
// The shape that makes it a real test is applying the patch while the fault is
// still in place. Removing the fault and watching the signal fall proves the
// fault caused it, which the resolution half of the ordinary proof already
// covers. Leaving the fault and applying the fix asks: if I merge this, does
// the incident stop?
//
// It is a separate command rather than another step in the ordinary proof
// because the two would collide. The resolution half watches for the
// firing-to-clear edge after the fault is reset; a patch that works produces
// that edge earlier, so the ordinary proof would end up timing the remediation
// and calling it the reset. Two claims, two runs, neither borrowing the
// other's evidence.
//
// The rule this harness holds to is the one the rest of the package does: it
// never credits a patch for something the environment did, and never blames a
// patch, or an entry, for something the observer could not see. A harness that
// reports "remediated" wrongly is worse than none, so every way a reading can
// fail to be a measurement (a blind tick, a signal with no series, a NaN, a
// suspended machine, an interrupt) ends the run as BLIND rather than as either
// verdict.

// cleanupTimeout bounds each undo phase, as the ordinary proof bounds its
// reset. Without it one hung kubectl after an interrupt holds the process
// forever, and the signal handler swallows every further Ctrl-C.
const cleanupTimeout = 3 * time.Minute

// RemediationResult is what applying an entry's own patch proved.
type RemediationResult struct {
	Entry string `json:"entry"`

	// Skipped explains why no remediation was attempted because there is
	// nothing to prove, and is the whole result when it is set. Most catalog
	// entries are report-only: they produce a dossier and propose nothing, so
	// saying so is not a gap.
	Skipped string `json:"skipped,omitempty"`
	// Unchecked is the other silence, and it is a gap. The entry proposes a
	// patch, and its spec declares no remediation check, so nothing has shown
	// that the patch works. Folding this into Skipped once made the summary
	// say that these entries propose no patch, which is false.
	Unchecked string `json:"unchecked,omitempty"`

	Fired    bool `json:"fired"`
	Applied  bool `json:"applied"`
	Cleared  bool `json:"cleared"`
	Reverted bool `json:"reverted"`
	// Patch is the YAML actually applied, kept so a passing proof is auditable
	// rather than merely green.
	Patch string `json:"patch,omitempty"`

	// AtFire is the signal when the incident was reported and AtEnd is where
	// it finished. Threshold is what both are measured against.
	AtFire    float64 `json:"at_fire"`
	AtEnd     float64 `json:"at_end"`
	Threshold float64 `json:"threshold"`
	// ClearedAfter is how long after the patch the clear that held began, and
	// Sustain is how long it was required to hold.
	ClearedAfter time.Duration `json:"cleared_after"`
	Sustain      time.Duration `json:"sustain"`
	Duration     time.Duration `json:"duration"`

	Blind       bool     `json:"blind"`
	BlindReason string   `json:"blind_reason,omitempty"`
	Failures    []string `json:"failures,omitempty"`
}

// Passed reports whether the entry's own remedy stopped its own incident.
func (r RemediationResult) Passed() bool {
	return r.Skipped == "" && r.Unchecked == "" && !r.Blind && r.Cleared && len(r.Failures) == 0
}

// Verdict is the word for the summary table.
func (r RemediationResult) Verdict() string {
	switch {
	case r.Skipped != "":
		return "skipped"
	case r.Unchecked != "":
		return "unchecked"
	case r.Blind:
		return "BLIND"
	case r.Passed():
		return "pass"
	default:
		return "FAIL"
	}
}

// void marks the run as saying nothing about the patch. The reason goes first
// in the failures so it is the line an operator reads.
func (r *RemediationResult) void(reason, consequence string) {
	r.Blind, r.BlindReason = true, reason
	r.Failures = append([]string{"BLIND: " + reason + ". " + consequence}, r.Failures...)
}

// RemediationPlan is what a remediation run will do to the cluster, worked out
// without touching it. The confirmation prints it and Remediate executes it,
// so the patch an operator acknowledged is byte for byte the patch applied.
type RemediationPlan struct {
	Entry string
	// Skipped and Unchecked mirror the result fields of the same name. When
	// either is set nothing below is filled and nothing will run.
	Skipped   string
	Unchecked string
	// Err is a spec or entry that cannot be run as written.
	Err error

	// Apply is the argv that receives Patch on stdin.
	Apply  []string
	Patch  string
	Revert []Command
}

// PlanRemediation decides what a remediation run would do for one spec.
//
// Order matters. Report-only comes before "no check declared", because a
// report-only entry has nothing to check and calling that a missing check
// would turn a design choice into a gap.
func PlanRemediation(scenarios []catalog.Scenario, s Spec) RemediationPlan {
	plan := RemediationPlan{Entry: s.Entry}
	var entry catalog.Scenario
	found := false
	for _, sc := range scenarios {
		if sc.ID == s.Entry {
			entry, found = sc, true
			break
		}
	}
	if !found {
		plan.Err = fmt.Errorf("no catalog entry with this id")
		return plan
	}
	if entry.Remediation.Action == "report-only" || entry.Remediation.PatchTemplate == "" {
		plan.Skipped = "the entry is report-only: it proposes a dossier, not a patch, so there is no fix to prove"
		return plan
	}
	if s.Remediation == nil {
		plan.Unchecked = "the entry proposes a patch and its spec declares no remediation check, so nothing has shown the patch works"
		return plan
	}
	kubeCtx, err := SpecContext(s)
	if err != nil {
		plan.Err = err
		return plan
	}
	patch, err := remediate.Render(entry, s.Target)
	if err != nil {
		plan.Err = fmt.Errorf("the entry's patch does not render: %w", err)
		return plan
	}
	plan.Apply = []string{"kubectl"}
	if kubeCtx != "" {
		plan.Apply = append(plan.Apply, "--context="+kubeCtx)
	}
	plan.Apply = append(plan.Apply, "apply", "-f", "-")
	plan.Patch = patch
	plan.Revert = s.Remediation.Revert
	return plan
}

// SpecContext returns the one kube context every kubectl command in the spec
// targets: inject, reset and revert. Empty means none pins one, so they all go
// to kubectl's current context together.
//
// Any disagreement is an error, including one command pinning a context and
// another pinning none. A revert that lands in a different cluster from the
// patch deletes something in somebody else's mesh and leaves the patch on
// this one, and the first version of this harness read the context from the
// first inject command alone, in one spelling of the flag.
func SpecContext(s Spec) (string, error) {
	cmds := append(append([]Command{}, s.Inject...), s.Reset...)
	if s.Remediation != nil {
		cmds = append(cmds, s.Remediation.Revert...)
	}
	seen := map[string][]string{}
	for _, c := range cmds {
		kubeCtx, isKubectl := pinnedContext(c.Run)
		if !isKubectl {
			continue
		}
		seen[kubeCtx] = append(seen[kubeCtx], c.String())
	}
	if len(seen) <= 1 {
		for k := range seen {
			return k, nil
		}
		return "", nil
	}
	var parts []string
	for k, cs := range seen {
		name := k
		if name == "" {
			name = "the current context"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", name, strings.Join(cs, "; ")))
	}
	sort.Strings(parts)
	return "", fmt.Errorf("the spec's kubectl commands target more than one kube context: %s. Pin the same --context on every command",
		strings.Join(parts, ", "))
}

// pinnedContext reports the context a kubectl argv pins, in either spelling
// of the flag, and whether the argv is kubectl at all.
func pinnedContext(argv []string) (string, bool) {
	if len(argv) == 0 || filepath.Base(argv[0]) != "kubectl" {
		return "", false
	}
	for i, a := range argv[1:] {
		if v, ok := strings.CutPrefix(a, "--context="); ok {
			return v, true
		}
		if a == "--context" && i+2 < len(argv) {
			return argv[i+2], true
		}
	}
	return "", true
}

// sustainFor is how long the signal must stay clear before the patch is
// credited: the entry's own hold, and at least three polls.
//
// The symmetry is the argument. The entry does not call a breach an incident
// until it has held for `for`, and the harness does not call a clear a
// recovery until it has held as long. One reading is not enough with the
// fault still in place: outlier ejection returns hosts after its
// baseEjectionTime, so a patch that does not hold produces exactly a dip and a
// climb back. The floor keeps an entry with no hold from being credited on a
// single tick.
func sustainFor(entry catalog.Scenario, poll time.Duration) time.Duration {
	sustain := detect.Hold(entry)
	if floor := 3 * poll; sustain < floor {
		sustain = floor
	}
	return sustain
}

// Remediate proves that the patch an entry proposes stops the incident it
// reported, with the fault still in place.
//
// The return value is named because the deferred undo writes to it. With an
// unnamed return, `return res` copies the struct into the return slot before
// any defer runs, so everything the cleanup records, whether the revert
// succeeded, how long the run took, any failure while undoing, is written to a
// variable nobody reads. A test caught three such losses at once.
func (r *Runner) Remediate(ctx context.Context, s Spec) (res RemediationResult) {
	res = RemediationResult{Entry: s.Entry}
	start := r.Now()
	wallStart := r.Wall()
	r.suspended = 0
	r.onIncident = func(detect.Incident) {}

	// Registered first so it runs last, after the undo, and so measures it.
	defer func() {
		res.Duration = r.Now().Sub(start)
		// The whole-run suspension check, as in Run. Go's monotonic clock
		// stops while the machine sleeps, so only the wall clock can see a
		// suspension, including one during a kubectl call that no tick loop
		// was watching. A clear that "held" across a closed lid was never
		// watched holding.
		if res.Skipped != "" || res.Unchecked != "" || res.Blind {
			return
		}
		if gap := r.Wall().Sub(wallStart) - res.Duration; gap > r.suspendThreshold() {
			res.Cleared = false
			res.void(fmt.Sprintf("the process was suspended for about %s during the run", gap.Round(time.Second)),
				"The measurement is void and says nothing about the patch")
		}
	}()

	plan := PlanRemediation(r.scenarios, s)
	switch {
	case plan.Err != nil:
		res.Failures = append(res.Failures, plan.Err.Error()+". Nothing was run")
		return res
	case plan.Skipped != "":
		res.Skipped = plan.Skipped
		return res
	case plan.Unchecked != "":
		res.Unchecked = plan.Unchecked
		return res
	}
	entry, _ := r.scenario(s.Entry)
	res.Patch = plan.Patch
	res.Sustain = sustainFor(entry, r.Poll)

	// A deadline the sustained clear cannot fit inside is arithmetic, and a
	// run against it would report a working patch as a broken one.
	if s.Remediation.ClearsWithin.D() <= res.Sustain {
		res.Failures = append(res.Failures, fmt.Sprintf(
			"remediation.clearsWithin is %s but the signal must stay clear for %s before the patch is credited, so no patch can pass. Nothing was run",
			s.Remediation.ClearsWithin.D(), res.Sustain))
		return res
	}

	if reason, ok := r.preflight(ctx, s); !ok {
		res.Blind, res.BlindReason = true, reason
		res.Failures = append(res.Failures,
			"BLIND: "+reason+". Nothing was injected, and this says nothing about the remedy")
		return res
	}

	// Warm a relative threshold before the fault, exactly as the detection
	// proof does, or the patch is judged against the static fallback: a
	// different bar from the one the entry fired against in production.
	r.warmBaseline(ctx, s)
	if ctx.Err() != nil {
		res.void("the run was interrupted during warm-up", "Nothing was injected")
		return res
	}

	// Undo in the reverse order things were done, and always. A run that
	// leaves its own remediation behind has quietly reconfigured the mesh for
	// every measurement after it, which is worse than leaving a fault: a fault
	// is loud and a fix is not. Both undos run whatever the other did, each
	// on its own bounded context that an interrupt cannot cancel.
	defer func() {
		revertCtx, cancelRevert := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancelRevert()
		res.Reverted = true
		for _, c := range s.Remediation.Revert {
			if err := r.Exec(revertCtx, c.Run); err != nil {
				res.Reverted = false
				res.Failures = append(res.Failures, fmt.Sprintf(
					"REVERT FAILED (%s): %v. The patch may still be on the testbed", c, err))
			}
		}
		resetCtx, cancelReset := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancelReset()
		for _, c := range s.Reset {
			if err := r.Exec(resetCtx, c.Run); err != nil {
				res.Failures = append(res.Failures, fmt.Sprintf(
					"RESET FAILED (%s): %v. The fault may still be on the testbed", c, err))
			}
		}
	}()

	r.Log("injecting the fault")
	for _, c := range s.Inject {
		if err := r.Exec(ctx, c.Run); err != nil {
			res.Failures = append(res.Failures, fmt.Sprintf("inject failed (%s): %v", c, err))
			return res
		}
	}
	if s.Settle.D() > 0 {
		r.Log("settling for %s so rate windows fill", s.Settle.D())
		r.Sleep(ctx, s.Settle.D())
	}

	d := r.detectorFor(s)
	threshold, _ := d.EffectiveThreshold(entry, detect.Target{Params: s.Target})
	res.Threshold = threshold

	// Wait for the signal to be in breach, because a remedy applied to a
	// cluster that is fine proves nothing about the entry.
	//
	// Breach, not a reported incident: the entry reports once its hold
	// elapses, and waiting that out would delay the patch by minutes without
	// changing what is being proven. The patch is rendered from the entry and
	// the target, which is exactly what the report renders it from, so the
	// bytes applied here are the bytes an operator would have merged.
	fired, w := r.waitForBreach(ctx, d, entry, s.FiresWithin.D())
	res.Fired, res.AtFire = fired, w.value
	switch {
	case w.suspended > 0:
		res.void(fmt.Sprintf("the process was suspended for %s while waiting for the breach", w.suspended.Round(time.Second)),
			"Nothing was applied, and this says nothing about the entry or the patch")
		return res
	case !fired && w.interrupted:
		res.void("the run was interrupted while waiting for the breach", "Nothing was applied")
		return res
	case !fired && w.blind > 0:
		res.void(fmt.Sprintf("the entry's signal could not be read on %d tick(s) while waiting for the breach", w.blind),
			"Not seeing a breach while blind says nothing about the entry, and nothing was applied")
		return res
	case !fired:
		res.Failures = append(res.Failures, fmt.Sprintf(
			"the entry never fired within %s, so there was no incident to remedy", s.FiresWithin.D()))
		return res
	}
	r.Log("%s in breach at %.4g; applying the patch it proposes, with the fault still in place", s.Entry, w.value)

	apply := r.ExecInput
	if apply == nil {
		apply = execCommandInput
	}
	if err := apply(ctx, plan.Apply, plan.Patch); err != nil {
		res.Failures = append(res.Failures, "applying the proposed patch failed: "+err.Error())
		return res
	}
	res.Applied = true

	rec := r.waitForRecovery(ctx, d, entry, s.Remediation.ClearsWithin.D(), res.Sustain)
	res.Cleared, res.ClearedAfter, res.AtEnd = rec.cleared, rec.clearedAfter, rec.value
	switch {
	case rec.suspended > 0:
		res.Cleared = false
		res.void(fmt.Sprintf("the process was suspended for %s while the patch was being judged", rec.suspended.Round(time.Second)),
			"The entry fired and the patch was applied; whether it worked is unmeasured")
		return res
	case rec.cleared:
		r.Log("the signal fell to %.4g after %s and held for %s, with the fault untouched",
			rec.value, rec.clearedAfter.Round(time.Second), res.Sustain)
		return res
	case rec.interrupted:
		res.void("the run was interrupted while the patch was being judged",
			"The patch was applied; whether it worked is unmeasured")
		return res
	case rec.blind > 0 || rec.absent > 0:
		res.void(fmt.Sprintf("after the patch the entry's signal was unreadable on %d tick(s) and returned no series or NaN on %d",
			rec.blind, rec.absent),
			"A gap breaks the clear the patch must hold, and a signal with no series looks the same whether the patch stopped the errors or stopped the traffic, so this says nothing about the patch")
		return res
	}
	held := ""
	if rec.longestClear > 0 {
		held = fmt.Sprintf(", and the longest clear lasted %s of the %s required", rec.longestClear.Round(time.Second), res.Sustain)
	}
	res.Failures = append(res.Failures, fmt.Sprintf(
		"the signal was still at %.4g against a threshold of %.4g %s after the patch was applied%s: "+
			"the entry reported an incident and proposed a change that did not stop it",
		rec.value, threshold, s.Remediation.ClearsWithin.D(), held))
	return res
}

// watch is what a wait saw besides its answer: the reasons the answer might
// not be a measurement.
type watch struct {
	value float64
	// blind counts ticks where the subject's evaluation was blind.
	blind int
	// absent counts clear readings that were not a measurement: no series,
	// or a NaN or infinite value.
	absent      int
	suspended   time.Duration
	interrupted bool

	cleared      bool
	clearedAfter time.Duration
	longestClear time.Duration
}

// suspendedSince reports a wall-clock gap since the last tick that means the
// process was suspended, and records it.
func (r *Runner) suspendedSince(last time.Time) (time.Duration, bool) {
	if gap := r.Wall().Sub(last); gap > r.suspendThreshold() {
		r.Log("wall clock jumped %s between ticks: the process was suspended, so this measurement is void", gap.Round(time.Second))
		r.suspended = gap
		return gap, true
	}
	return 0, false
}

// waitForBreach ticks until the entry's signal is over its threshold, and
// reports the value it reached and how well it could see.
func (r *Runner) waitForBreach(ctx context.Context, d *detect.Detector, entry catalog.Scenario, budget time.Duration) (bool, watch) {
	var w watch
	deadline := r.Now().Add(budget)
	lastTick := r.Wall()
	for r.Now().Before(deadline) && ctx.Err() == nil {
		if gap, ok := r.suspendedSince(lastTick); ok {
			w.suspended = gap
			return false, w
		}
		lastTick = r.Wall()

		cycle := d.Tick(ctx, r.Now())
		for _, ev := range cycle.Evaluations {
			if ev.Scenario != entry.ID {
				continue
			}
			switch ev.Outcome {
			case detect.OutcomeFiring:
				w.value = ev.Value
				return true, w
			case detect.OutcomeBlind:
				w.blind++
			case detect.OutcomeClear:
				w.value = ev.Value
			}
		}
		r.Sleep(ctx, r.Poll)
	}
	w.interrupted = ctx.Err() != nil
	return false, w
}

// measuredClear reports whether an evaluation is a real reading under the
// threshold. The detector calls an empty result on a watched target clear,
// and for detecting a failure counter that is right. For crediting a patch it
// is not: the silence is the same whether the patch stopped the errors or the
// traffic. That clear is the only one the detector gives a Reason, which is
// what this keys on, and a test drives it through the real detector so a
// change there is caught here.
func measuredClear(ev detect.Evaluation) bool {
	return ev.Outcome == detect.OutcomeClear && ev.Reason == "" &&
		!math.IsNaN(ev.Value) && !math.IsInf(ev.Value, 0)
}

// waitForRecovery ticks until the signal has been measurably clear for
// sustain without a break, or the budget runs out. Any tick that is not a
// measured clear, in breach, blind or absent, breaks the run: two clears
// either side of a gap are not one long clear.
func (r *Runner) waitForRecovery(ctx context.Context, d *detect.Detector, entry catalog.Scenario, budget, sustain time.Duration) watch {
	var w watch
	start := r.Now()
	deadline := start.Add(budget)
	lastTick := r.Wall()
	var streakStart time.Time
	for r.Now().Before(deadline) && ctx.Err() == nil {
		if gap, ok := r.suspendedSince(lastTick); ok {
			w.suspended = gap
			return w
		}
		lastTick = r.Wall()

		now := r.Now()
		cycle := d.Tick(ctx, now)
		for _, ev := range cycle.Evaluations {
			if ev.Scenario != entry.ID {
				continue
			}
			if measuredClear(ev) {
				w.value = ev.Value
				if streakStart.IsZero() {
					streakStart = now
				}
				held := now.Sub(streakStart)
				if held > w.longestClear {
					w.longestClear = held
				}
				if held >= sustain {
					w.cleared, w.clearedAfter = true, streakStart.Sub(start)
					return w
				}
				continue
			}
			streakStart = time.Time{}
			switch ev.Outcome {
			case detect.OutcomeFiring:
				w.value = ev.Value
			case detect.OutcomeBlind:
				w.blind++
			case detect.OutcomeClear:
				w.absent++
			}
		}
		r.Sleep(ctx, r.Poll)
	}
	w.interrupted = ctx.Err() != nil
	return w
}

// scenario finds a catalog entry by id.
func (r *Runner) scenario(id string) (catalog.Scenario, bool) {
	for _, s := range r.scenarios {
		if s.ID == id {
			return s, true
		}
	}
	return catalog.Scenario{}, false
}
