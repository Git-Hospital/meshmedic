package proof

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// applyRig is a remediation run whose apply command is scripted. The signal
// breaches and holds, so the run reaches the apply; what the apply does is the
// test's.
func applyRig(t *testing.T, apply func(ctx context.Context) error) (*Runner, *recorder) {
	t.Helper()
	r, rec, _, _ := remediationRig(t, remediableEntry("error-surge"),
		breachUntilPatched(func(int) reading { return value(20) }))
	r.ExecInput = func(ctx context.Context, argv []string, _ string) error {
		rec.argv = append(rec.argv, argv)
		return apply(ctx)
	}
	return r, rec
}

// Cleanup is the same after a failed apply as after any other failure: the
// patch may or may not have landed, and the fault certainly is in.
func assertCleanupRan(t *testing.T, res RemediationResult, rec *recorder) {
	t.Helper()
	if !ran(rec, "delete the-patch") || !ran(rec, "reset-fault") {
		t.Errorf("revert=%v reset=%v, want both to have run", ran(rec, "delete the-patch"), ran(rec, "reset-fault"))
	}
	if !res.Reverted || !res.Reset {
		t.Errorf("reverted=%v reset=%v, want both true", res.Reverted, res.Reset)
	}
	if len(res.CleanupErrors) > 0 {
		t.Errorf("cleanup errors: %v", res.CleanupErrors)
	}
}

// An API server that stops answering must not hold the run until Ctrl-C. The
// apply gets its own deadline, and the run ends in ERROR: whether the patch
// landed is unknown, and a hung cluster says nothing about the patch.
func TestRemediationApplyTimesOutAsAnEnvironmentError(t *testing.T) {
	r, rec := applyRig(t, func(ctx context.Context) error {
		<-ctx.Done() // the API server never answers
		return ctx.Err()
	})
	r.ApplyTimeout = 20 * time.Millisecond

	done := make(chan RemediationResult, 1)
	go func() { done <- r.Remediate(context.Background(), remediationSpec("error-surge")) }()
	var res RemediationResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the run hung on an apply that never returned")
	}

	if res.Verdict() != "ERROR" {
		t.Fatalf("verdict %s, want ERROR: errors=%v failures=%v", res.Verdict(), res.Errors, res.Failures)
	}
	if len(res.Failures) > 0 {
		t.Errorf("a timed-out apply was also blamed on the patch: %v", res.Failures)
	}
	if res.Applied {
		t.Error("an apply that never finished was recorded as applied")
	}
	if !strings.Contains(strings.Join(res.Errors, " "), "did not finish") {
		t.Errorf("the error should say the apply did not finish, got %v", res.Errors)
	}
	assertCleanupRan(t, res, rec)
}

// kubectl's own text for a server it could not talk to is not the cluster
// refusing the patch. The run is an ERROR, and cleanup still runs.
func TestRemediationApplyToAnUnreachableServerIsAnEnvironmentError(t *testing.T) {
	for _, msg := range []string{
		"kubectl apply -f -: exit status 1: Unable to connect to the server: dial tcp 127.0.0.1:6443: connect: connection refused",
		"kubectl apply -f -: exit status 1: The connection to the server 127.0.0.1:6443 was refused - did you specify the right host or port?",
	} {
		r, rec := applyRig(t, func(context.Context) error { return errors.New(msg) })

		res := r.Remediate(context.Background(), remediationSpec("error-surge"))

		if res.Verdict() != "ERROR" {
			t.Errorf("%q: verdict %s, want ERROR: errors=%v failures=%v", msg, res.Verdict(), res.Errors, res.Failures)
		}
		if len(res.Failures) > 0 {
			t.Errorf("%q: an unreachable server was blamed on the patch: %v", msg, res.Failures)
		}
		assertCleanupRan(t, res, rec)
	}
}

// A patch the API server answered and rejected is the patch's failure, and so
// is an admission webhook that could not be called: the API server was
// reachable, and the webhook's own "connection refused" is not kubectl failing
// to reach it.
func TestRemediationRejectedPatchStaysAPatchFailure(t *testing.T) {
	for _, msg := range []string{
		`kubectl apply -f -: exit status 1: The Deployment "web" is invalid: spec.template.spec.containers[0].image: Required value`,
		`kubectl apply -f -: exit status 1: admission webhook "policy.example.com" denied the request: privileged containers are not allowed`,
		`kubectl apply -f -: exit status 1: Operation cannot be fulfilled on deployments.apps "web": the object has been modified; please apply your changes to the latest version and try again`,
		`kubectl apply -f -: exit status 1: Error from server (InternalError): failed calling webhook "x.example.com": Post "https://svc.ns.svc:443/validate": dial tcp 10.0.0.9:443: connect: connection refused`,
	} {
		r, rec := applyRig(t, func(context.Context) error { return errors.New(msg) })

		res := r.Remediate(context.Background(), remediationSpec("error-surge"))

		if res.Verdict() != "FAIL" {
			t.Errorf("%q: verdict %s, want FAIL: errors=%v failures=%v", msg, res.Verdict(), res.Errors, res.Failures)
		}
		if len(res.Errors) > 0 {
			t.Errorf("%q: a rejected patch was reported as an environment error: %v", msg, res.Errors)
		}
		if !strings.Contains(strings.Join(res.Failures, " "), "refused the proposed patch") {
			t.Errorf("%q: failures should say the cluster refused the patch, got %v", msg, res.Failures)
		}
		assertCleanupRan(t, res, rec)
	}
}
