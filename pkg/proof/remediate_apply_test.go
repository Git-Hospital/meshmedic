package proof

import (
	"context"
	"errors"
	"os/exec"
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
	if !strings.Contains(strings.Join(res.Errors, " "), "did not finish within") {
		t.Errorf("the error should say the apply did not finish, got %v", res.Errors)
	}
	assertCleanupRan(t, res, rec)
}

// kubectlFailure is the error the real executor returns for a kubectl that
// exited 1 with this output.
func kubectlFailure(output string) error {
	return &commandError{
		argv:   []string{"kubectl", "--context=kind-test", "apply", "-f", "-"},
		err:    errors.New("exit status 1"),
		output: output,
	}
}

// Captured from kubectl 1.35.0 (client) run against servers that could not be
// reached. The first three are what `kubectl apply -f -` prints, which
// validates against the server's OpenAPI first, so it is not the "Unable to
// connect" line that people expect.
const (
	kubectlClosedPort = `error: error validating "STDIN": error validating data: failed to download openapi: Get "https://127.0.0.1:9/openapi/v2?timeout=32s": dial tcp 127.0.0.1:9: connect: connection refused; if you choose to ignore these errors, turn validation off with --validate=false`
	kubectlNoValidate = `E1113 01:49:54.744575   45970 memcache.go:265] "Unhandled Error" err="couldn't get current server API group list: Get \"https://127.0.0.1:9/api?timeout=32s\": dial tcp 127.0.0.1:9: connect: connection refused"
error: unable to recognize "STDIN": Get "https://127.0.0.1:9/api?timeout=32s": dial tcp 127.0.0.1:9: connect: connection refused`
	kubectlNoRoute = `error: error validating "STDIN": error validating data: failed to download openapi: Get "https://10.255.255.1:6443/openapi/v2?timeout=4s": net/http: request canceled while waiting for connection (Client.Timeout exceeded while awaiting headers); if you choose to ignore these errors, turn validation off with --validate=false`
	kubectlBadDNS  = `error: error validating "STDIN": error validating data: failed to download openapi: Get "https://no-such-host.invalid:6443/openapi/v2?timeout=4s": dial tcp: lookup no-such-host.invalid: no such host; if you choose to ignore these errors, turn validation off with --validate=false`
	// What delete, get and other verbs print.
	kubectlRefused = `The connection to the server 127.0.0.1:9 was refused - did you specify the right host or port?`
)

// kubectl's own client failing to get an answer is not the cluster refusing
// the patch. The run is an ERROR, and cleanup still runs.
func TestRemediationApplyToAnUnreachableServerIsAnEnvironmentError(t *testing.T) {
	for name, out := range map[string]string{
		"closed port, validating":  kubectlClosedPort,
		"closed port, no validate": kubectlNoValidate,
		"no route":                 kubectlNoRoute,
		"bad dns":                  kubectlBadDNS,
		"connection refused line":  kubectlRefused,
		"unable to connect line":   "Unable to connect to the server: x509: certificate signed by unknown authority",
	} {
		r, rec := applyRig(t, func(context.Context) error { return kubectlFailure(out) })

		res := r.Remediate(context.Background(), remediationSpec("error-surge"))

		if res.Verdict() != "ERROR" {
			t.Errorf("%s: verdict %s, want ERROR: errors=%v failures=%v", name, res.Verdict(), res.Errors, res.Failures)
		}
		if len(res.Failures) > 0 {
			t.Errorf("%s: an unreachable server was blamed on the patch: %v", name, res.Failures)
		}
		if !strings.Contains(strings.Join(res.Errors, " "), "check the testbed") {
			t.Errorf("%s: the error should tell the operator to check the testbed, got %v", name, res.Errors)
		}
		assertCleanupRan(t, res, rec)
	}
}

// The klog line on its own is noise, not the client failing: only the
// "error:" line below it says the apply had no answer.
func TestRemediationKlogLineAloneIsNotUnreachable(t *testing.T) {
	klog := strings.SplitN(kubectlNoValidate, "\n", 2)[0]
	if apiServerUnreachable(kubectlFailure(klog)) {
		t.Error("a klog line alone was taken for an unreachable API server")
	}
}

// A patch the API server answered and rejected is the patch's failure, however
// the rejection is worded. The unreachable phrases appear here mid-line or
// under "Error from server", where they are the server's text.
func TestRemediationRejectedPatchStaysAPatchFailure(t *testing.T) {
	for name, out := range map[string]string{
		"invalid":         `The Deployment "web" is invalid: spec.template.spec.containers[0].image: Required value`,
		"denied":          `Error from server (Forbidden): admission webhook "policy.example.com" denied the request: privileged containers are not allowed`,
		"conflict":        `Error from server (Conflict): Operation cannot be fulfilled on deployments.apps "web": the object has been modified; please apply your changes to the latest version and try again`,
		"webhook down":    `Error from server (InternalError): error when creating "STDIN": Internal error occurred: failed calling webhook "x.example.com": failed to call webhook: Post "https://svc.ns.svc:443/validate?timeout=10s": dial tcp 10.0.0.9:443: connect: connection refused`,
		"echoes unable":   `Error from server (Forbidden): admission webhook "p.example.com" denied the request: Unable to connect to the server is not a valid reason`,
		"echoes refused":  `Error from server (Invalid): the note says The connection to the server 1.2.3.4 was refused`,
		"echoes mid-line": `Error from server (Forbidden): denied: error: error validating x: failed to download openapi: Get "https://a"`,
		"unauthorized":    `error: You must be logged in to the server (Unauthorized)`,
	} {
		r, rec := applyRig(t, func(context.Context) error { return kubectlFailure(out) })

		res := r.Remediate(context.Background(), remediationSpec("error-surge"))

		if res.Verdict() != "FAIL" {
			t.Errorf("%s: verdict %s, want FAIL: errors=%v failures=%v", name, res.Verdict(), res.Errors, res.Failures)
		}
		if len(res.Errors) > 0 {
			t.Errorf("%s: a rejected patch was reported as an environment error: %v", name, res.Errors)
		}
		if !strings.Contains(strings.Join(res.Failures, " "), "refused the proposed patch") {
			t.Errorf("%s: failures should say the cluster refused the patch, got %v", name, res.Failures)
		}
		assertCleanupRan(t, res, rec)
	}
}

// A real rejection that arrives with the deadline already passed is still a
// rejection. The timeout is read from the error the command returned, not from
// the clock afterwards.
func TestRemediationRejectionBesideTheDeadlineStaysAPatchFailure(t *testing.T) {
	r, rec := applyRig(t, func(ctx context.Context) error {
		<-ctx.Done() // the deadline passes, then the answer arrives
		return kubectlFailure(`Error from server (Invalid): The Deployment "web" is invalid`)
	})
	r.ApplyTimeout = 10 * time.Millisecond

	res := r.Remediate(context.Background(), remediationSpec("error-surge"))

	if res.Verdict() != "FAIL" {
		t.Errorf("verdict %s, want FAIL: errors=%v failures=%v", res.Verdict(), res.Errors, res.Failures)
	}
	assertCleanupRan(t, res, rec)
}

// The real executor wraps the context's error when the context ended the
// command, and not when the command failed first.
func TestCommandErrorWrapsTheContextOnlyWhenItEndedTheCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := execCommandInput(ctx, []string{"sh", "-c", "sleep 5"}, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a command killed by the deadline should wrap it, got %v", err)
	}
	err = execCommandInput(context.Background(), []string{"sh", "-c", "echo nope; exit 1"}, "")
	if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Errorf("a command that failed by itself should not wrap a context error, got %v", err)
	}
}

// A killed command must not keep the run waiting on a grandchild that still
// holds the output pipe, which is what a credential plugin does.
func TestExecReturnsWhenAGrandchildHoldsThePipe(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	old := execWaitDelay
	execWaitDelay = 100 * time.Millisecond
	defer func() { execWaitDelay = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- execCommandInput(ctx, []string{"sh", "-c", "sleep 4 & wait"}, "") }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a killed command reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the command kept the run waiting on a child holding the pipe")
	}
}
