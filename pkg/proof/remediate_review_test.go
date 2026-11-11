package proof

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Git-Hospital/meshmedic/pkg/baseline"
	"github.com/Git-Hospital/meshmedic/pkg/catalog"
)

// These tests come from an independent review of the remediation harness.
// Each one is a way a live run could have published a wrong verdict: a PASS
// that was not true, a FAIL that blamed the patch for something else, or a
// real failure hidden as BLIND.

// relativeEntry learns its normal in warm-up: at 10 with a multiplier of 3,
// the threshold the patch is judged against is 30.
func relativeEntry() catalog.Scenario {
	entry := remediableEntry("latency")
	entry.Signal.Threshold = 1000
	entry.Signal.BaselineMultiplier = 3
	entry.Signal.BaselineMinSamples = 3
	return entry
}

// sincePatch reports how many ticks have passed since the first tick that saw
// the patch applied, so a test can script the recovery window.
type sincePatch struct{ first int }

func (s *sincePatch) at(tick int) int {
	if s.first < 0 {
		s.first = tick
	}
	return tick - s.first
}

func newSincePatch() *sincePatch { return &sincePatch{first: -1} }

func joinedText(res RemediationResult) string {
	return strings.Join(append(append(append([]string{}, res.Failures...), res.Errors...), res.CleanupErrors...), " ") +
		" " + res.BlindReason
}

// Finding 1, the false PASS. The detector feeds every clear reading back into
// the baseline, so a store shared with the recovery wait raises a relative
// threshold each time the signal dips under it. A patch that did nothing,
// with one noisy dip, then ratchets the bar over the fault's own level and is
// credited with a recovery while the signal sits above the reported
// threshold.
func TestRemediationRelativeThresholdDoesNotRatchetDuringRecovery(t *testing.T) {
	k := newSincePatch()
	r, _, _, _ := remediationRig(t, relativeEntry(), func(tick int, injected, patched bool) reading {
		switch {
		case patched:
			if k.at(tick) == 0 {
				return value(29) // one noisy dip under 30
			}
			return value(31) // the fault, untouched by a useless patch
		case injected:
			return value(31)
		}
		return value(10)
	})
	r.Baseline = baseline.New("", 0.05)
	s := remediationSpec("latency")
	s.Warmup = Duration(time.Minute)

	res := r.Remediate(context.Background(), s)

	if res.Passed() {
		t.Fatalf("false PASS: the signal never left the fault's level (at end %v, threshold %v)", res.AtEnd, res.Threshold)
	}
	if res.Verdict() != "FAIL" {
		t.Errorf("verdict = %s, want FAIL: every tick was measured and the signal stayed in breach: %v", res.Verdict(), res.Failures)
	}
	if math.Abs(res.Threshold-30) > 1e-9 {
		t.Errorf("reported threshold = %v, want 30, the bar the warm-up set", res.Threshold)
	}
	learned, _ := r.Baseline.Baseline(targetKeyFor("latency", s.Target), 3)
	if math.Abs(learned-10) > 1e-9 {
		t.Errorf("the baseline moved to %v after the fault was injected, want it frozen at 10", learned)
	}
}

// Finding 1, the earlier half. Between the fault and the breach the signal
// climbs through values just under the threshold, and each of those clear
// readings taught the baseline the fault's own level, so the bar rose ahead
// of the signal and the run never saw the breach it needed.
func TestRemediationRelativeThresholdDoesNotDriftBeforeTheBreach(t *testing.T) {
	firstInjected := -1
	r, _, _, _ := remediationRig(t, relativeEntry(), func(tick int, injected, patched bool) reading {
		switch {
		case patched:
			return value(10)
		case injected:
			if firstInjected < 0 {
				firstInjected = tick
			}
			if tick-firstInjected < 6 {
				return value(29) // a rate window filling
			}
			return value(31)
		}
		return value(10)
	})
	r.Baseline = baseline.New("", 0.05)
	s := remediationSpec("latency")
	s.Warmup = Duration(time.Minute)
	s.FiresWithin = Duration(3 * time.Minute)

	res := r.Remediate(context.Background(), s)

	if !res.Passed() {
		t.Fatalf("verdict = %s, want a pass judged against 30: %v", res.Verdict(), res.Failures)
	}
	if math.Abs(res.Threshold-30) > 1e-9 {
		t.Errorf("reported threshold = %v, want 30", res.Threshold)
	}
}

// Finding 2. One scrape error right after the patch, then the signal in breach
// on every later tick. The observer watched the patch fail for far longer
// than a sustained clear needs, so the gap could not have hidden a pass.
func TestRemediationOneBlindTickDoesNotHideAFailedPatch(t *testing.T) {
	k := newSincePatch()
	r, _, _, _ := remediationRig(t, remediableEntry("error-surge"), breachUntilPatched(func(tick int) reading {
		if k.at(tick) == 0 {
			return reading{err: errors.New("scrape blip")}
		}
		return value(150)
	}))

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if res.Verdict() != "FAIL" {
		t.Fatalf("verdict = %s, want FAIL: %v", res.Verdict(), res.Failures)
	}
	if !strings.Contains(joinedText(res), "did not") {
		t.Errorf("failures = %v, want the patch named as not having ended the incident", res.Failures)
	}
}

// The same rule at the other end of the window. A gap just before the
// deadline, between readings that are all in breach, cannot have hidden a
// sustained clear either: the readings either side of it rule one out.
func TestRemediationLateBlindTickAmongBreachesIsAFail(t *testing.T) {
	k := newSincePatch()
	r, _, _, _ := remediationRig(t, remediableEntry("error-surge"), breachUntilPatched(func(tick int) reading {
		if k.at(tick) == 11 { // the last tick inside a 2m window
			return reading{err: errors.New("scrape blip")}
		}
		return value(150)
	}))

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if res.Verdict() != "FAIL" {
		t.Fatalf("verdict = %s, want FAIL: %v", res.Verdict(), res.Failures)
	}
}

// And the case where blindness did matter. The signal is clear on every
// reading for a full sustain, except one tick nobody could read, and then
// climbs back. Had that tick been seen clear, the run would have passed
// there, so calling it FAIL would blame the patch for the observer.
func TestRemediationBlindTickInsideAPossibleClearIsBlind(t *testing.T) {
	k := newSincePatch()
	r, _, _, _ := remediationRig(t, remediableEntry("error-surge"), breachUntilPatched(func(tick int) reading {
		switch n := k.at(tick); {
		case n == 1:
			return reading{err: errors.New("scrape blip")}
		case n <= 3:
			return value(20)
		}
		return value(150)
	}))

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if res.Verdict() != "BLIND" {
		t.Fatalf("verdict = %s, want BLIND: the unread tick could have completed a clear: %v", res.Verdict(), res.Failures)
	}
}

// Finding 3. A cleanup failure after a held clear is a second fact, not a
// rewrite of the first. The patch ended the incident; the reset then failed.
func TestRemediationCleanupFailureDoesNotRewriteTheResult(t *testing.T) {
	r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	rec.failOn = "reset-fault"

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if !res.Cleared || !res.Passed() {
		t.Fatalf("the patch held a clear and the result says %s: %v", res.Verdict(), res.Failures)
	}
	if len(res.Failures) != 0 {
		t.Errorf("the cleanup failure was filed against the patch: %v", res.Failures)
	}
	if res.Cleanup() != "FAILED" || res.Reset || !strings.Contains(strings.Join(res.CleanupErrors, " "), "RESET FAILED") {
		t.Errorf("cleanup = %s, reset = %v, errors = %v; want the failed reset reported as its own fact",
			res.Cleanup(), res.Reset, res.CleanupErrors)
	}
	if !res.Reverted {
		t.Error("the revert succeeded and was not recorded")
	}
}

// A Ctrl-C while kubectl apply runs is the operator's act. The branch reported
// it as "applying the proposed patch failed: context canceled".
func TestRemediationInterruptDuringApplyIsBlind(t *testing.T) {
	r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.ExecInput = func(c context.Context, argv []string, stdin string) error {
		rec.argv = append(rec.argv, argv)
		cancel()
		return c.Err()
	}

	res := r.Remediate(ctx, remediationSpec("error-surge"))

	if res.Verdict() != "BLIND" {
		t.Fatalf("verdict = %s, want BLIND for an interrupt: %v", res.Verdict(), res.Failures)
	}
	if strings.Contains(joinedText(res), "patch failed") {
		t.Errorf("the patch was blamed for the interrupt: %v", res.Failures)
	}
	if !ran(rec, "delete the-patch") || !ran(rec, "reset-fault") {
		t.Errorf("an interrupt during apply must still revert and reset: %v", rec.argv)
	}
}

// An inject that fails is the testbed's problem. Nothing was applied, so no
// verdict about the patch is possible.
func TestRemediationInjectFailureIsAnError(t *testing.T) {
	r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	rec.failOn = "set env x"

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if res.Verdict() != "ERROR" {
		t.Fatalf("verdict = %s, want ERROR: %v", res.Verdict(), res.Failures)
	}
	if len(rec.stdin) != 0 {
		t.Error("a patch was applied after the fault failed to inject")
	}
	if len(res.Failures) != 0 || !strings.Contains(strings.Join(res.Errors, " "), "inject failed") {
		t.Errorf("failures = %v, errors = %v; want the inject failure as a setup error only", res.Failures, res.Errors)
	}
	if !ran(rec, "reset-fault") {
		t.Error("a partly injected fault must still be reset")
	}
}

// The up-front refusal of an unwinnable deadline is a spec error, not a patch
// that "did not stop it".
func TestRemediationUnwinnableDeadlineIsAnError(t *testing.T) {
	entry := remediableEntry("error-surge")
	entry.Signal.For = "5m"
	r, _, _, _ := remediationRig(t, entry, breachUntilPatched(func(int) reading { return value(20) }))
	s := remediationSpec("error-surge")
	s.FiresWithin = Duration(10 * time.Minute)
	s.Remediation.ClearsWithin = Duration(time.Minute)

	res := r.Remediate(context.Background(), s)

	if res.Verdict() != "ERROR" {
		t.Fatalf("verdict = %s, want ERROR: %v", res.Verdict(), res.Failures)
	}
}

// A fault that never produced a breach left nothing to remedy. That says
// something about the fault or the entry, and nothing about the patch.
func TestRemediationNoBreachIsAnErrorNotAPatchVerdict(t *testing.T) {
	r, _, _, _ := remediationRig(t, remediableEntry("error-surge"), func(int, bool, bool) reading { return value(5) })

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if res.Verdict() != "ERROR" {
		t.Fatalf("verdict = %s, want ERROR: %v", res.Verdict(), res.Failures)
	}
}

// Finding 4, the detection-only half. A spec without a remediation block keeps
// its looser rule: the context is read from the last --context token, and a
// command whose shape cannot be checked is not refused. This is a best-effort
// reading for the preflight, not a guarantee about where kubectl goes; only a
// spec that applies a patch is held to the strict shape below.
func TestSpecContextDetectionOnlyReadsTheLastContextToken(t *testing.T) {
	s := remediationSpec("error-surge")
	s.Remediation = nil
	s.Inject = []Command{{Run: []string{"kubectl", "--context=kind-test", "--context=other", "set", "env", "x"}}}
	s.Reset = []Command{{Run: []string{"kubectl", "--context", "other", "delete", "reset-fault"}}}

	got, err := SpecContext(s)
	if err != nil || got != "other" {
		t.Fatalf("SpecContext = %q, %v; want other", got, err)
	}
}

// For a spec that applies a patch, every command must be a direct kubectl
// carrying exactly one --context and no --kubeconfig, or nothing runs.
func TestRemediationRefusesACommandWhoseClusterCannotBeChecked(t *testing.T) {
	cases := []struct {
		name  string
		where string
		argv  []string
	}{
		{"two contexts", "inject", []string{"kubectl", "--context=kind-test", "--context=prod", "set", "env", "x"}},
		{"two contexts, spaced", "revert", []string{"kubectl", "--context", "kind-test", "--context", "prod", "delete", "the-patch"}},
		// The four bypasses a re-check found against a real kubectl.
		{"a context swallowed as the namespace", "revert", []string{"kubectl", "-n", "--context=kind-test", "delete", "the-patch"}},
		{"a server glued to -s", "reset", []string{"kubectl", "--context=kind-test", "-shttps://other:6443", "delete", "reset-fault"}},
		{"a config rewrite", "inject", []string{"kubectl", "--context=kind-test", "config", "set", "clusters.kind-test.server", "https://other"}},
		{"a plugin", "revert", []string{"kubectl", "foo", "--context=kind-test"}},
		{"the context in two tokens", "revert", []string{"kubectl", "--context", "kind-test", "delete", "the-patch"}},
		{"a second context later", "revert", []string{"kubectl", "--context=kind-test", "delete", "the-patch", "--context=kind-test"}},
		{"a namespace with no value", "reset", []string{"kubectl", "--context=kind-test", "-n"}},
		{"a namespace value that is a flag", "reset", []string{"kubectl", "--context=kind-test", "-n", "-v=9", "delete", "reset-fault"}},
		{"an unknown subcommand", "revert", []string{"kubectl", "--context=kind-test", "foo", "the-patch"}},
		{"an unlisted global flag", "revert", []string{"kubectl", "--context=kind-test", "-v=9", "delete", "the-patch"}},
		{"no subcommand", "revert", []string{"kubectl", "--context=kind-test"}},
		{"through a shell", "revert", []string{"sh", "-c", "kubectl --context=kind-test delete the-patch"}},
		{"through env", "reset", []string{"env", "KUBECONFIG=/prod", "kubectl", "--context=kind-test", "reset-fault"}},
		{"a kubeconfig", "revert", []string{"kubectl", "--kubeconfig=/prod", "--context=kind-test", "delete", "the-patch"}},
		{"a kubeconfig, spaced", "reset", []string{"kubectl", "--kubeconfig", "/prod", "--context", "kind-test", "reset-fault"}},
		{"no context", "inject", []string{"kubectl", "-n", "demo", "set", "env", "x"}},
		{"an empty context", "inject", []string{"kubectl", "--context=", "set", "env", "x"}},
		{"a dangling context flag", "revert", []string{"kubectl", "delete", "the-patch", "--context"}},
		// The same context name, a different API server.
		{"a server", "revert", []string{"kubectl", "--context=kind-test", "--server=https://prod:6443", "delete", "the-patch"}},
		{"a server, short", "reset", []string{"kubectl", "--context=kind-test", "-s", "https://prod:6443", "reset-fault"}},
		{"a cluster", "inject", []string{"kubectl", "--context=kind-test", "--cluster", "prod", "set", "env", "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"),
				breachUntilPatched(func(int) reading { return value(20) }))
			s := remediationSpec("error-surge")
			cmd := []Command{{Run: c.argv}}
			switch c.where {
			case "inject":
				// Keep the phase marker the fake Prometheus keys on.
				s.Inject = append(cmd, s.Inject...)
			case "reset":
				s.Reset = cmd
			case "revert":
				s.Remediation.Revert = cmd
			}

			res := r.Remediate(context.Background(), s)

			if len(rec.argv) != 0 {
				t.Fatalf("commands ran although one cannot be checked: %v", rec.argv)
			}
			if res.Verdict() != "ERROR" {
				t.Errorf("verdict = %s, want ERROR", res.Verdict())
			}
			text := joinedText(res)
			if !strings.Contains(text, strings.Join(c.argv, " ")) {
				t.Errorf("the refusal does not name the command %q: %s", strings.Join(c.argv, " "), text)
			}
			if c.name == "the context in two tokens" && !strings.Contains(text, "--context=NAME") {
				t.Errorf("the refusal does not tell the author to write --context=NAME: %s", text)
			}
		})
	}
}

// The shapes the shipped specs use, and the generated apply, are accepted.
func TestRemediationAcceptsTheShapesShippedSpecsUse(t *testing.T) {
	for _, argv := range [][]string{
		{"kubectl", "--context=kind-test", "-n", "demo", "set", "env", "deploy/x", "A=1"},
		{"kubectl", "--context=kind-test", "-n", "demo", "delete", "destinationrule", "x", "--ignore-not-found"},
		{"kubectl", "--context=kind-test", "-n", "demo", "patch", "deploy/x", "--type=merge", "-p", "{}"},
		{"kubectl", "--context=kind-test", "-n", "demo", "rollout", "status", "deploy/x", "--timeout=180s"},
		{"kubectl", "--context=kind-test", "-n", "demo", "scale", "deploy/x", "--replicas=0"},
		{"kubectl", "--context=kind-test", "-n", "demo", "wait", "--for=delete", "pod", "-l", "app=x"},
		{"kubectl", "--context=kind-test", "--namespace", "demo", "delete", "x"},
		{"kubectl", "--context=kind-test", "--namespace=demo", "delete", "x"},
		{"kubectl", "--context=kind-test", "apply", "-f", "-"},
	} {
		if why := uncheckable(argv); why != "" {
			t.Errorf("%q refused: %s", strings.Join(argv, " "), why)
		}
	}
	plan := PlanRemediation([]catalog.Scenario{remediableEntry("error-surge")}, remediationSpec("error-surge"))
	if plan.Err != nil {
		t.Fatalf("plan: %v", plan.Err)
	}
	if why := uncheckable(plan.Apply); why != "" {
		t.Errorf("the generated apply %q fails its own rule: %s", strings.Join(plan.Apply, " "), why)
	}
}

// Every shipped spec still resolves to the demo cluster under the new rule.
func TestShippedSpecsResolveToTheDemoContext(t *testing.T) {
	_, specs := shipped(t)
	for _, sp := range specs {
		got, err := SpecContext(sp)
		if err != nil {
			t.Errorf("%s: %v", sp.Entry, err)
			continue
		}
		if got != "kind-meshmedic-demo" {
			t.Errorf("%s: context = %q, want kind-meshmedic-demo", sp.Entry, got)
		}
	}
}

// A detection-only spec is no stricter than it was: a shell command is not
// checked there, as before.
func TestSpecContextLeavesDetectionOnlySpecsAsTheyWere(t *testing.T) {
	s := remediationSpec("error-surge")
	s.Remediation = nil
	s.Reset = append(s.Reset, Command{Run: []string{"sh", "-c", "true"}})
	if got, err := SpecContext(s); err != nil || got != "kind-test" {
		t.Fatalf("SpecContext = %q, %v; want kind-test and no error", got, err)
	}
}

// Finding 5a. A revert that panics must not skip the reset.
func TestRemediationPanickingRevertStillResets(t *testing.T) {
	r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	inner := r.Exec
	r.Exec = func(ctx context.Context, argv []string) error {
		if strings.Contains(strings.Join(argv, " "), "delete the-patch") {
			panic("boom")
		}
		return inner(ctx, argv)
	}

	var res RemediationResult
	panicked := func() (p any) {
		defer func() { p = recover() }()
		res = r.Remediate(context.Background(), remediationSpec("error-surge"))
		return nil
	}()

	if panicked != nil {
		t.Errorf("a panicking revert escaped Remediate: %v", panicked)
	}
	if !ran(rec, "reset-fault") {
		t.Fatalf("the revert panicked and the reset never ran: %v", rec.argv)
	}
	if res.Reverted {
		t.Error("a panicking revert was recorded as reverted")
	}
	if !strings.Contains(strings.Join(res.CleanupErrors, " "), "panicked: boom") {
		t.Errorf("cleanup errors = %v, want the panic recorded", res.CleanupErrors)
	}
	if !res.Passed() {
		t.Errorf("the patch held a clear before the panic, verdict = %s", res.Verdict())
	}
}

// Finding 5b. Held is measured in time, and with slow ticks two readings 35s
// apart satisfied a 30s hold. Nothing was seen between them, so the clear was
// not watched holding; it is not a pass, and it is not the patch's fault.
func TestRemediationHeldClearNeedsEnoughReadings(t *testing.T) {
	r, rec, _, now := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	sleep := r.Sleep
	r.Sleep = func(ctx context.Context, d time.Duration) {
		sleep(ctx, d)
		if len(rec.stdin) > 0 {
			*now = now.Add(25 * time.Second) // a slow tick, well under the suspension threshold
		}
	}
	s := remediationSpec("error-surge")
	s.Remediation.ClearsWithin = Duration(100 * time.Second)

	res := r.Remediate(context.Background(), s)

	if res.Passed() {
		t.Fatalf("a clear read only %s apart was credited as held", 35*time.Second)
	}
	if res.Verdict() != "BLIND" {
		t.Errorf("verdict = %s, want BLIND: every reading was clear, there were too few of them: %v", res.Verdict(), res.Failures)
	}
}

// Finding 6. A fault that decays by itself would be credited to any patch
// applied at its first breaching tick. The breach must hold, measured, before
// the patch goes on; one that does not is not judged.
func TestRemediationFaultThatDoesNotHoldIsNotJudged(t *testing.T) {
	firstInjected := -1
	r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"), func(tick int, injected, patched bool) reading {
		switch {
		case patched:
			return value(20)
		case injected:
			if firstInjected < 0 {
				firstInjected = tick
			}
			if tick == firstInjected {
				return value(150)
			}
			return value(20) // decaying on its own
		}
		return value(20)
	})

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if res.Passed() {
		t.Fatal("a fault that decayed on its own was credited to the patch")
	}
	if res.Verdict() != "BLIND" || !strings.Contains(res.BlindReason, "did not hold") {
		t.Errorf("verdict = %s (%q), want BLIND saying the fault did not hold without the patch", res.Verdict(), res.BlindReason)
	}
	if len(rec.stdin) != 0 {
		t.Error("a patch was applied to a breach that had not held")
	}
}

func TestRemediationPatchesOnlyAfterTheBreachHeld(t *testing.T) {
	var firstBreach, applied time.Time
	r, rec, _, now := remediationRig(t, remediableEntry("error-surge"), nil)
	r.prom = phaseProm{tick: new(int), rec: rec, signal: func(_ int, injected, patched bool) reading {
		switch {
		case patched:
			return value(20)
		case injected:
			if firstBreach.IsZero() {
				firstBreach = *now
			}
			return value(150)
		}
		return value(20)
	}}
	inner := r.ExecInput
	r.ExecInput = func(ctx context.Context, argv []string, stdin string) error {
		applied = *now
		return inner(ctx, argv, stdin)
	}

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if !res.Passed() {
		t.Fatalf("verdict = %s: %v", res.Verdict(), res.Failures)
	}
	if held := applied.Sub(firstBreach); held < res.Sustain {
		t.Errorf("the patch went on %s after the first breaching reading, want at least the %s sustain", held, res.Sustain)
	}
}

// With the same slow ticks and room for enough readings, the clear passes,
// and it rests on at least one reading per poll across the hold.
func TestRemediationSlowTicksPassOnceEnoughReadingsHold(t *testing.T) {
	r, rec, _, now := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	sleep := r.Sleep
	r.Sleep = func(ctx context.Context, d time.Duration) {
		sleep(ctx, d)
		if len(rec.stdin) > 0 {
			*now = now.Add(25 * time.Second)
		}
	}

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if !res.Passed() {
		t.Fatalf("verdict = %s: %v", res.Verdict(), res.Failures)
	}
	if want := readingsFor(res.Sustain, r.Poll); res.ClearReadings < want {
		t.Errorf("the clear rested on %d readings, want at least %d", res.ClearReadings, want)
	}
}
