package proof

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Git-Hospital/meshmedic/pkg/catalog"
	"github.com/Git-Hospital/meshmedic/pkg/prom"
)

// These tests hold the remediation harness to the rule the rest of the package
// already follows: it must never credit a patch for something the environment
// did, and never blame a patch, or an entry, for something the observer could
// not see. Each one reproduces a way a live run could have reported the wrong
// verdict.

// reading is one answer the subject's signal gives on one tick.
type reading struct {
	v      float64
	noData bool
	err    error
}

func value(v float64) reading { return reading{v: v} }

// phaseProm answers from what has been done to the cluster so far, so a test
// can say "breaching until the patch lands, then no series" without counting
// ticks by hand. probe answers the coverage probe and the liveness check; nil
// means the observer can always see.
type phaseProm struct {
	tick   *int
	rec    *recorder
	signal func(tick int, injected, patched bool) reading
	probe  func(tick int, injected, patched bool) error
}

func (p phaseProm) phase() (int, bool, bool) {
	injected := false
	for _, argv := range p.rec.argv {
		if strings.Contains(strings.Join(argv, " "), "set env x") {
			injected = true
		}
	}
	return *p.tick, injected, len(p.rec.stdin) > 0
}

func (p phaseProm) sight() error {
	if p.probe == nil {
		return nil
	}
	return p.probe(p.phase())
}

func (p phaseProm) Query(_ context.Context, promql string) (float64, error) {
	if promql == "vector(1)" {
		return 1, p.sight()
	}
	if !strings.Contains(promql, "signal_") {
		return 0, prom.ErrNoData
	}
	if err := p.sight(); err != nil {
		return 0, err
	}
	r := p.signal(p.phase())
	switch {
	case r.err != nil:
		return 0, r.err
	case r.noData:
		return 0, prom.ErrNoData
	}
	return r.v, nil
}

func (p phaseProm) QuerySeries(_ context.Context, _ string) ([]prom.Sample, error) {
	if err := p.sight(); err != nil {
		return nil, err
	}
	return []prom.Sample{{Value: 1}}, nil
}

// breachUntilPatched is the common shape: healthy before the fault, in breach
// while the fault is in place, and whatever after says once the patch lands.
func breachUntilPatched(after func(tick int) reading) func(int, bool, bool) reading {
	return func(tick int, injected, patched bool) reading {
		switch {
		case patched:
			return after(tick)
		case injected:
			return value(150)
		}
		return value(20)
	}
}

func remediationRig(t *testing.T, entry catalog.Scenario, signal func(int, bool, bool) reading) (*Runner, *recorder, *phaseProm, *time.Time) {
	t.Helper()
	tick := 0
	rec := &recorder{}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	p := &phaseProm{tick: &tick, rec: rec, signal: signal}
	r := newRemediationRunner(t, []catalog.Scenario{entry}, p, rec, &tick, &now)
	return r, rec, p, &now
}

func ran(rec *recorder, fragment string) bool {
	for _, argv := range rec.argv {
		if strings.Contains(strings.Join(argv, " "), fragment) {
			return true
		}
	}
	return false
}

// Hypothesis 1. A revert that fails must not leave the fault behind as well:
// the reset is a separate undo with a separate purpose, and skipping it because
// an unrelated command failed leaves the testbed broken twice over.
//
// The failed revert is its own fact. The patch held a clear before it, so the
// patch's result stays a pass, and the cleanup is reported as failed beside
// it; the command exits non-zero on the cleanup alone.
func TestRemediationResetsTheFaultEvenWhenTheRevertFails(t *testing.T) {
	r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	rec.failOn = "delete the-patch"

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if !ran(rec, "reset-fault") {
		t.Fatalf("the revert failed and the fault's reset never ran: %v", rec.argv)
	}
	if res.Reverted {
		t.Error("a failed revert was recorded as reverted")
	}
	if !res.Reset {
		t.Error("the reset succeeded and was not recorded")
	}
	if !res.Passed() {
		t.Errorf("the failed revert rewrote the patch's own result: %s %v", res.Verdict(), res.Failures)
	}
	if res.Cleanup() != "FAILED" || !strings.Contains(strings.Join(res.CleanupErrors, " "), "REVERT FAILED") {
		t.Errorf("cleanup = %s %v, want the failed revert named", res.Cleanup(), res.CleanupErrors)
	}
	if len(res.Failures) != 0 {
		t.Errorf("a cleanup failure was filed against the patch: %v", res.Failures)
	}
}

// Hypothesis 2, the credit half. After the patch the signal returns no series.
// For a failure counter on a watched target the detector calls that clear, and
// for detection that is right. For a remediation it is not a measurement: a
// patch that stopped all traffic produces exactly the same silence as one that
// stopped the errors, and the harness cannot tell which.
func TestRemediationDoesNotCreditASignalThatReturnsNoSeries(t *testing.T) {
	r, _, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return reading{noData: true} }))

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if res.Passed() || res.Cleared {
		t.Fatalf("a signal that vanished was credited as a working patch: %s %v", res.Verdict(), res.Failures)
	}
	if !res.Blind {
		t.Errorf("verdict = %s, want BLIND: nothing was measured after the patch", res.Verdict())
	}
	if strings.Contains(strings.Join(res.Failures, " "), "did not stop it") {
		t.Errorf("the patch was blamed for a signal nobody could read: %v", res.Failures)
	}
	if !strings.Contains(res.BlindReason, "no series") {
		t.Errorf("blind reason = %q, want it to say the signal returned no series", res.BlindReason)
	}
}

// The same silence by another route: a ratio whose denominator went to zero.
// Prometheus answers NaN, which compares false against every threshold and so
// reads as clear.
func TestRemediationDoesNotCreditANaNSignal(t *testing.T) {
	r, _, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(math.NaN()) }))

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if res.Passed() || res.Cleared {
		t.Fatalf("a NaN reading was credited as a working patch: %s %v", res.Verdict(), res.Failures)
	}
}

// Hypothesis 2, the blame half. The observer goes blind the moment the patch
// lands. Nothing about the patch was measured, so FAIL is the wrong word.
func TestRemediationBlindAfterThePatchIsNotBlamedOnThePatch(t *testing.T) {
	r, _, p, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(150) }))
	p.probe = func(_ int, _, patched bool) error {
		if patched {
			return errors.New("dial tcp 127.0.0.1:9090: connect: connection refused")
		}
		return nil
	}

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if !res.Blind {
		t.Fatalf("verdict = %s, want BLIND: %v", res.Verdict(), res.Failures)
	}
	if strings.Contains(strings.Join(res.Failures, " "), "did not stop it") {
		t.Errorf("the patch was blamed for the observer going blind: %v", res.Failures)
	}
}

// The observer goes blind once the fault is in. The entry was never seen, so
// "never fired" would blame it for the network.
func TestRemediationBlindBeforeTheBreachIsNotBlamedOnTheEntry(t *testing.T) {
	r, rec, p, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	p.probe = func(_ int, injected, _ bool) error {
		if injected {
			return errors.New("connection refused")
		}
		return nil
	}

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if !res.Blind {
		t.Fatalf("verdict = %s, want BLIND: %v", res.Verdict(), res.Failures)
	}
	if strings.Contains(strings.Join(res.Failures, " "), "never fired") {
		t.Errorf("the entry was blamed for the observer going blind: %v", res.Failures)
	}
	if len(rec.stdin) != 0 {
		t.Error("a patch was applied without a breach having been seen")
	}
}

// Hypothesis 3. Outlier ejection puts hosts back after baseEjectionTime, so a
// signal that dips under the threshold for one tick and climbs back is the
// expected shape of a patch that does not hold. One reading is not a recovery.
func TestRemediationRequiresTheClearToHold(t *testing.T) {
	entry := remediableEntry("error-surge")
	entry.Signal.For = "60s"
	r, _, _, _ := remediationRig(t, entry, breachUntilPatched(func(tick int) reading {
		if tick%3 == 0 {
			return value(20) // a dip every third tick, then back in breach
		}
		return value(150)
	}))
	s := remediationSpec("error-surge")
	s.FiresWithin = Duration(2 * time.Minute) // room for the 60s breach hold
	s.Remediation.ClearsWithin = Duration(4 * time.Minute)

	res := r.Remediate(context.Background(), s)

	if res.Passed() || res.Cleared {
		t.Fatalf("a one-tick dip was credited as a recovery: %s, cleared after %s", res.Verdict(), res.ClearedAfter)
	}
	if res.Blind {
		t.Errorf("the observer saw every tick, so this is a finding, not a blind run: %s", res.BlindReason)
	}
	if !strings.Contains(strings.Join(res.Failures, " "), "did not stop it") {
		t.Errorf("failures = %v, want the patch named as not having stopped the incident", res.Failures)
	}
}

// A clear that holds for the entry's own hold duration is a recovery, and the
// result says how long it was required to hold.
func TestRemediationPassesWhenTheClearHoldsForTheEntrysHold(t *testing.T) {
	entry := remediableEntry("error-surge")
	entry.Signal.For = "60s"
	r, _, _, _ := remediationRig(t, entry, breachUntilPatched(func(int) reading { return value(20) }))
	s := remediationSpec("error-surge")
	s.FiresWithin = Duration(2 * time.Minute) // room for the 60s breach hold
	s.Remediation.ClearsWithin = Duration(3 * time.Minute)

	res := r.Remediate(context.Background(), s)

	if !res.Passed() {
		t.Fatalf("verdict = %s: %v", res.Verdict(), res.Failures)
	}
	if res.Sustain != time.Minute {
		t.Errorf("sustain = %s, want the entry's 60s hold", res.Sustain)
	}
}

// A deadline the sustained clear cannot fit inside is arithmetic, and it is
// refused before the cluster is touched rather than reported as a broken patch.
func TestRemediationRefusesAClearsWithinInsideTheSustain(t *testing.T) {
	entry := remediableEntry("error-surge")
	entry.Signal.For = "5m"
	r, rec, _, _ := remediationRig(t, entry, breachUntilPatched(func(int) reading { return value(20) }))
	s := remediationSpec("error-surge")
	s.FiresWithin = Duration(10 * time.Minute)
	s.Remediation.ClearsWithin = Duration(time.Minute)

	res := r.Remediate(context.Background(), s)

	if res.Passed() {
		t.Fatal("an unwinnable remediation deadline passed")
	}
	if len(rec.argv) != 0 {
		t.Errorf("an unwinnable spec touched the cluster: %v", rec.argv)
	}
	if res.Verdict() != "ERROR" || !strings.Contains(strings.Join(res.Errors, " "), "clearsWithin") {
		t.Errorf("verdict = %s, errors = %v, want ERROR naming the deadline", res.Verdict(), res.Errors)
	}
}

// Hypothesis 4. A revert pinned to another context deletes something in
// somebody else's cluster and leaves the patch on this one.
func TestRemediationRefusesCommandsThatTargetDifferentContexts(t *testing.T) {
	r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	s := remediationSpec("error-surge")
	s.Remediation.Revert = []Command{{Run: []string{"kubectl", "--context=prod", "delete", "the-patch"}}}

	res := r.Remediate(context.Background(), s)

	if res.Passed() {
		t.Fatal("a spec whose revert targets another cluster passed")
	}
	if len(rec.argv) != 0 {
		t.Errorf("commands ran against two clusters: %v", rec.argv)
	}
	if res.Verdict() != "ERROR" || !strings.Contains(strings.Join(res.Errors, " "), "context") {
		t.Errorf("verdict = %s, errors = %v, want ERROR naming the context mismatch", res.Verdict(), res.Errors)
	}
}

// The context the patch is applied through is the spec's own, read from its
// commands rather than assumed. The first version read only the first inject
// command. Since the context guard, a spec that applies a patch writes it in
// one form only, --context=NAME directly after kubectl; the two-token form is
// refused, see TestRemediationRefusesACommandWhoseClusterCannotBeChecked.
func TestRemediationAppliesThroughTheSpecsContext(t *testing.T) {
	r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	s := remediationSpec("error-surge")
	s.Inject = []Command{{Run: []string{"kubectl", "--context=kind-other", "set", "env", "x"}}}
	s.Reset = []Command{{Run: []string{"kubectl", "--context=kind-other", "delete", "reset-fault"}}}
	s.Remediation.Revert = []Command{{Run: []string{"kubectl", "--context=kind-other", "delete", "the-patch"}}}

	r.Remediate(context.Background(), s)

	var apply string
	for _, argv := range rec.argv {
		if joined := strings.Join(argv, " "); strings.Contains(joined, "apply -f -") {
			apply = joined
		}
	}
	if apply == "" {
		t.Fatal("no apply command was run")
	}
	if !strings.Contains(apply, "--context=kind-other") {
		t.Errorf("the patch was applied without the fault's context: %s", apply)
	}
}

// Hypothesis 5 is refuted for the recovery wait, which has always had its
// deadline. The cleanup did not: it ran on a context that can never expire, so
// one hung kubectl after an interrupt held the process forever, with the
// signal handler swallowing every further Ctrl-C. The ordinary proof bounds
// its reset; this has to bound both undos.
func TestRemediationCleanupIsBoundedEvenAfterAnInterrupt(t *testing.T) {
	r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	unbounded := []string{}
	inner := r.Exec
	r.Exec = func(ctx context.Context, argv []string) error {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "delete the-patch") || strings.Contains(joined, "reset-fault") {
			if _, ok := ctx.Deadline(); !ok {
				unbounded = append(unbounded, joined)
			}
		}
		return inner(ctx, argv)
	}

	r.Remediate(context.Background(), remediationSpec("error-surge"))

	if !ran(rec, "delete the-patch") || !ran(rec, "reset-fault") {
		t.Fatalf("cleanup did not run: %v", rec.argv)
	}
	if len(unbounded) > 0 {
		t.Errorf("cleanup ran without a deadline: %v", unbounded)
	}
}

// Hypothesis 6. A relative-threshold entry judged without a warm-up is judged
// against its static fallback, which is a different bar. Here the fallback is
// so high the entry never fires; in the other direction it would credit a
// patch that left the signal twice its learned normal.
func TestRemediationWarmsARelativeThresholdBeforeJudgingIt(t *testing.T) {
	entry := remediableEntry("latency")
	entry.Signal.Threshold = 1000
	entry.Signal.BaselineMultiplier = 2
	entry.Signal.BaselineMinSamples = 3
	r, rec, _, _ := remediationRig(t, entry, func(_ int, injected, patched bool) reading {
		switch {
		case patched:
			return value(150)
		case injected:
			return value(300)
		}
		return value(100)
	})
	s := remediationSpec("latency")
	s.Warmup = Duration(time.Minute)

	res := r.Remediate(context.Background(), s)

	if !res.Passed() {
		t.Fatalf("verdict = %s: %v", res.Verdict(), res.Failures)
	}
	if res.Threshold >= 1000 {
		t.Errorf("judged against the static fallback %v rather than the learned threshold", res.Threshold)
	}
	if len(rec.stdin) != 1 {
		t.Errorf("patch applied %d times, want once", len(rec.stdin))
	}
}

// Hypothesis 7. The machine sleeps while the patch is being judged. Go's
// monotonic clock stops with it, so the recovery deadline expires without a
// reading, and the old harness called the patch a failure.
func TestRemediationSuspensionDuringRecoveryIsBlindNotAFailedPatch(t *testing.T) {
	r, rec, _, now := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(150) }))
	wall := *now
	r.Wall = func() time.Time { return wall }
	jumped := false
	sleep := r.Sleep
	r.Sleep = func(ctx context.Context, d time.Duration) {
		sleep(ctx, d)
		wall = wall.Add(d)
		if len(rec.stdin) > 0 && !jumped {
			jumped = true
			wall = wall.Add(5 * time.Hour)
		}
	}

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if !res.Blind {
		t.Fatalf("a five-hour suspension was not detected: %s %v", res.Verdict(), res.Failures)
	}
	if strings.Contains(strings.Join(res.Failures, " "), "did not stop it") {
		t.Errorf("the patch was blamed for the machine sleeping: %v", res.Failures)
	}
	if !ran(rec, "delete the-patch") || !ran(rec, "reset-fault") {
		t.Error("cleanup must still run after a suspension")
	}
}

// And the other direction: a suspension inside the run voids a pass too,
// because a clear that "held" across a closed lid was never watched holding.
func TestRemediationSuspensionVoidsAPass(t *testing.T) {
	r, rec, _, now := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	wall := *now
	r.Wall = func() time.Time { return wall }
	inner := r.ExecInput
	r.ExecInput = func(ctx context.Context, argv []string, stdin string) error {
		wall = wall.Add(5 * time.Hour) // the lid closes during kubectl apply
		return inner(ctx, argv, stdin)
	}

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if res.Passed() {
		t.Fatal("a run suspended for five hours passed")
	}
	if !res.Blind {
		t.Errorf("verdict = %s, want BLIND", res.Verdict())
	}
	_ = rec
}

// Interrupting the run is the operator's act, not the patch's. The old
// harness reported a Ctrl-C during the recovery window as a patch that "did
// not stop" the incident.
func TestRemediationInterruptIsNotBlamedOnThePatch(t *testing.T) {
	r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(150) }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := r.ExecInput
	r.ExecInput = func(c context.Context, argv []string, stdin string) error {
		err := inner(c, argv, stdin)
		cancel()
		return err
	}

	res := r.Remediate(ctx, remediationSpec("error-surge"))

	if res.Passed() {
		t.Fatal("an interrupted run passed")
	}
	if strings.Contains(strings.Join(res.Failures, " "), "did not stop it") {
		t.Errorf("the patch was blamed for the interrupt: %v", res.Failures)
	}
	if !ran(rec, "delete the-patch") || !ran(rec, "reset-fault") {
		t.Error("an interrupt must still revert and reset")
	}
}

// Two different silences under one name. A report-only entry proposes nothing,
// which is a design choice. An entry that proposes a patch whose spec has no
// remediation check is a gap: nobody has shown that patch works. The first
// version called both "skipped", and the summary then told the operator that
// the second kind proposes no patch, which is false.
func TestRemediationSeparatesAnUncheckedPatchFromAReportOnlyEntry(t *testing.T) {
	patching := remediableEntry("error-surge")
	reportOnly := remediableEntry("triage")
	reportOnly.Remediation.Action = "report-only"
	reportOnly.Remediation.PatchTemplate = ""

	tick := 0
	rec := &recorder{}
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	script := scriptedProm{tick: &tick, values: map[string][]float64{"error-surge": {150}, "triage": {150}}}
	r := newRemediationRunner(t, []catalog.Scenario{patching, reportOnly}, script, rec, &tick, &now)

	unchecked := remediationSpec("error-surge")
	unchecked.Remediation = nil
	got := r.Remediate(context.Background(), unchecked)
	if got.Verdict() != "unchecked" {
		t.Errorf("a patch nobody checks: verdict = %s, want unchecked", got.Verdict())
	}
	if got.Passed() {
		t.Error("an unchecked patch must not pass")
	}

	plain := remediationSpec("triage")
	plain.Remediation = nil
	got = r.Remediate(context.Background(), plain)
	if got.Verdict() != "skipped" || !strings.Contains(got.Skipped, "report-only") {
		t.Errorf("a report-only entry: verdict = %s (%q), want skipped because it is report-only", got.Verdict(), got.Skipped)
	}
	if len(rec.argv) != 0 {
		t.Errorf("neither run should touch the cluster: %v", rec.argv)
	}
}

// The plan the operator is shown is the plan that runs: the same argv and the
// same bytes.
func TestRemediationPlanIsWhatIsApplied(t *testing.T) {
	entry := remediableEntry("error-surge")
	r, rec, _, _ := remediationRig(t, entry, breachUntilPatched(func(int) reading { return value(20) }))
	s := remediationSpec("error-surge")

	plan := PlanRemediation([]catalog.Scenario{entry}, s)
	if plan.Err != nil || plan.Skipped != "" || plan.Unchecked != "" {
		t.Fatalf("plan = %+v", plan)
	}
	r.Remediate(context.Background(), s)

	if len(rec.stdin) != 1 || rec.stdin[0] != plan.Patch {
		t.Errorf("applied bytes differ from the plan shown:\n%v\nvs\n%s", rec.stdin, plan.Patch)
	}
	if !ran(rec, strings.Join(plan.Apply, " ")) {
		t.Errorf("apply argv %v was not what ran: %v", plan.Apply, rec.argv)
	}
}

// The shipped remediation specs must be runnable as written: one context for
// every command, a patch that renders, and a deadline with room for the
// sustained clear on top of the signal's fall.
func TestShippedRemediationSpecsArePlannable(t *testing.T) {
	scenarios, specs := shipped(t)
	byID := map[string]catalog.Scenario{}
	for _, s := range scenarios {
		byID[s.ID] = s
	}
	checked := 0
	for _, sp := range specs {
		if sp.Remediation == nil {
			continue
		}
		checked++
		plan := PlanRemediation(scenarios, sp)
		if plan.Err != nil || plan.Skipped != "" || plan.Unchecked != "" {
			t.Errorf("%s: plan = %+v", sp.Entry, plan)
			continue
		}
		// The signal cannot fall faster than its own rate window drains, and
		// the clear starts counting only once it has. So the deadline needs the
		// window, then the sustained clear, then a margin for the patch to take
		// effect and for the ticks around both.
		sustain := sustainFor(byID[sp.Entry], 10*time.Second)
		window := longestRangeWindow(t, byID[sp.Entry].Signal.PromQL)
		if slack := sp.Remediation.ClearsWithin.D() - window - sustain; slack < minimumSlack {
			t.Errorf("%s: clearsWithin %s leaves %s over a %s rate window and a %s sustained clear", sp.Entry,
				sp.Remediation.ClearsWithin.D(), slack, window, sustain)
		}
	}
	if checked == 0 {
		t.Fatal("no shipped spec declares a remediation check")
	}
}

// longestRangeWindow is the widest [range] selector in a PromQL query, which
// is how long a rate over it keeps remembering a fault after the fault stops
// producing samples.
func longestRangeWindow(t *testing.T, promql string) time.Duration {
	t.Helper()
	var longest time.Duration
	for _, m := range rangeSelector.FindAllStringSubmatch(promql, -1) {
		d, err := time.ParseDuration(m[1])
		if err != nil {
			t.Fatalf("range %q in %q: %v", m[1], promql, err)
		}
		if d > longest {
			longest = d
		}
	}
	return longest
}

var rangeSelector = regexp.MustCompile(`\[(\d+[smh])(?::[^\]]*)?\]`)
