package proof

import (
	"context"
	"errors"
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
// reports "remediated" wrongly is worse than none. So a clear is credited only
// when it was measured on every tick, a suspended machine or an interrupt
// voids the run as BLIND, and a run that did not clear is voided as BLIND
// whenever what the observer missed could have hidden a clear. A run that
// could not be set up, or whose undo failed, says so in its own words and
// never in the patch's.

// cleanupTimeout bounds each undo phase, as the ordinary proof bounds its
// reset. Without it one hung kubectl after an interrupt holds the process
// forever, and the signal handler swallows every further Ctrl-C.
const cleanupTimeout = 3 * time.Minute

// applyTimeout bounds the command that applies the patch, for the same reason
// the cleanup is bounded: an API server that stops answering must not hold the
// run until someone presses Ctrl-C.
const applyTimeout = 3 * time.Minute

// unreachableLines are the kubectl output lines that mean kubectl's own client
// could not get an answer from the API server, which is the environment and not
// the patch. Each is a line prefix and a phrase the line must also contain.
// They are read from kubectl 1.35.0 run against servers that could not be
// reached:
//
//	apply, validating: error: error validating "STDIN": ... failed to download openapi: Get "https://...
//	apply, no validation: error: unable to recognize "STDIN": Get "https://...
//	other transport failures: Unable to connect to the server: ...
//	delete, get: The connection to the server HOST was refused - did you ...
//
// Matching is per line and anchored at the line start, never a substring of
// the whole output, because a server's rejection can echo any phrase, and a
// reachable server whose admission webhook cannot be called reports
// "dial tcp ... connection refused" mid-line. A line that starts "Error from
// server" or names an invalid object is the server's answer and is not in the
// table. An expired token, a server-side timeout and a missing CRD are the
// server's answer too, and stay a patch failure.
var unreachableLines = []struct{ prefix, contains string }{
	{`error: error validating `, `failed to download openapi: Get "`},
	{`error: unable to recognize `, `: Get "`},
	{`Unable to connect to the server`, ``},
	{`The connection to the server `, ` was refused`},
}

// apiServerUnreachable reports whether err is a command whose output has a
// line from unreachableLines. An error that carries no command output is never
// unreachable: with nothing to read, it stays a patch failure.
func apiServerUnreachable(err error) bool {
	var ce *commandError
	if !errors.As(err, &ce) {
		return false
	}
	for _, line := range strings.Split(ce.output, "\n") {
		line = strings.TrimSpace(line)
		for _, u := range unreachableLines {
			if strings.HasPrefix(line, u.prefix) && strings.Contains(line, u.contains) {
				return true
			}
		}
	}
	return false
}

// RemediationResult is what applying an entry's own patch proved.
//
// It carries three separate facts, because folding them together is how the
// first version blamed the patch for things that were not the patch: whether
// the run could be set up (Errors), what the patch did (Cleared, Blind,
// Failures), and whether the run undid itself afterwards (CleanupErrors).
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

	// Fired means the entry's signal went into breach and held there, measured
	// on every tick, for the sustain, so the entry would have reported.
	Fired    bool `json:"fired"`
	Applied  bool `json:"applied"`
	Cleared  bool `json:"cleared"`
	Reverted bool `json:"reverted"`
	Reset    bool `json:"reset"`
	// Patch is the YAML actually applied, kept so a passing proof is auditable
	// rather than merely green.
	Patch string `json:"patch,omitempty"`

	// AtFire is the signal when the breach had held and the patch went on, and
	// AtEnd is where it finished. Threshold is what both were judged against:
	// a relative threshold is frozen once the fault is in, so it is one number
	// for the whole run.
	AtFire    float64 `json:"at_fire"`
	AtEnd     float64 `json:"at_end"`
	Threshold float64 `json:"threshold"`
	// ClearedAfter is how long after the patch the clear that held began,
	// ClearReadings how many consecutive measured readings it rested on, and
	// Sustain how long it was required to hold.
	ClearedAfter  time.Duration `json:"cleared_after"`
	ClearReadings int           `json:"clear_readings,omitempty"`
	Sustain       time.Duration `json:"sustain"`
	Duration      time.Duration `json:"duration"`

	Blind       bool   `json:"blind"`
	BlindReason string `json:"blind_reason,omitempty"`
	// Failures are about the patch: why it did not pass, with a BLIND reason
	// first when the run was void.
	Failures []string `json:"failures,omitempty"`
	// Errors are about the setup: a spec that cannot run as written, a fault
	// that did not inject, an incident that never formed. None of them says
	// anything about the patch.
	Errors []string `json:"errors,omitempty"`
	// CleanupErrors are about the undo: a revert or reset that failed or
	// panicked. They make the command exit non-zero and never rewrite what
	// the patch was seen to do before them.
	CleanupErrors []string `json:"cleanup_errors,omitempty"`
}

// Passed reports whether the entry's own remedy demonstrably ended its own
// incident. It does not include the cleanup, which is reported apart.
func (r RemediationResult) Passed() bool { return r.Verdict() == "PASS" }

// Verdict is the word for the summary table.
//
//	skipped    report-only entry, nothing to prove
//	unchecked  the entry proposes a patch and no spec checks it
//	ERROR      the run could not be set up, so it says nothing about the patch
//	BLIND      the observer could not tell, so it says nothing about the patch
//	PASS       the patch was applied during a held breach and the signal then
//	           held a measured clear for the sustain
//	FAIL       the patch was applied, or refused by the cluster, and was
//	           watched not ending the incident
func (r RemediationResult) Verdict() string {
	switch {
	case r.Skipped != "":
		return "skipped"
	case r.Unchecked != "":
		return "unchecked"
	case len(r.Errors) > 0:
		return "ERROR"
	case r.Blind:
		return "BLIND"
	case r.Cleared && len(r.Failures) == 0:
		return "PASS"
	default:
		return "FAIL"
	}
}

// Cleanup is the undo's own result: "ok" when every revert and reset command
// succeeded, "FAILED" when any did not, and "-" when the run stopped before
// there was anything to undo.
func (r RemediationResult) Cleanup() string {
	switch {
	case len(r.CleanupErrors) > 0:
		return "FAILED"
	case r.Reverted && r.Reset:
		return "ok"
	}
	return "-"
}

// void marks the run as saying nothing about the patch. The reason goes first
// in the failures so it is the line an operator reads.
func (r *RemediationResult) void(reason, consequence string) {
	r.Blind, r.BlindReason = true, reason
	r.Failures = append([]string{"BLIND: " + reason + ". " + consequence}, r.Failures...)
}

// setupError records a run that could not be set up.
func (r *RemediationResult) setupError(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
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
	// SpecContext has already required one non-empty context on every
	// command of a spec that applies a patch, so this always pins it. The
	// generated command is held to the same shape as the declared ones.
	plan.Apply = []string{"kubectl", "--context=" + kubeCtx, "apply", "-f", "-"}
	if why := uncheckable(plan.Apply); why != "" {
		plan.Err = fmt.Errorf("the apply command %q cannot be checked against the cluster: %s", strings.Join(plan.Apply, " "), why)
		return plan
	}
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
//
// A spec that applies a patch is held to more, and fails closed: every
// command must have the shape uncheckable accepts, kubectl --context=NAME
// first, no cluster overrides, and an allowlisted subcommand, because only
// then can the cluster it reaches be read from its arguments. A
// detection-only spec keeps the looser rule it always had, in which a
// command that is not kubectl is not checked and the context is read on a
// best-effort basis.
func SpecContext(s Spec) (string, error) {
	if s.Remediation != nil {
		for _, group := range []struct {
			name string
			cmds []Command
		}{{"inject", s.Inject}, {"reset", s.Reset}, {"remediation.revert", s.Remediation.Revert}} {
			for _, c := range group.cmds {
				if why := uncheckable(c.Run); why != "" {
					return "", fmt.Errorf("%s command %q cannot be checked against the cluster: %s. "+
						"A spec that applies a patch must run every command as kubectl --context=NAME followed by an allowed subcommand, with no other cluster override",
						group.name, strings.Join(c.Run, " "), why)
				}
			}
		}
	}
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

// pinnedContext reports the context a kubectl argv appears to pin, in either
// spelling of the flag, and whether the argv is kubectl at all. It reads the
// last --context token, as kubectl does when the flag is repeated. It is a
// best-effort reading for detection-only specs and not a guarantee: another
// flag can take a --context token as its value, which is why a spec that
// applies a patch must also pass uncheckable.
func pinnedContext(argv []string) (string, bool) {
	if len(argv) == 0 || filepath.Base(argv[0]) != "kubectl" {
		return "", false
	}
	kubeCtx := ""
	for i := 1; i < len(argv); i++ {
		if v, ok := strings.CutPrefix(argv[i], "--context="); ok {
			kubeCtx = v
			continue
		}
		if argv[i] == "--context" && i+1 < len(argv) {
			kubeCtx = argv[i+1]
			i++
		}
	}
	return kubeCtx, true
}

// remediationSubcommands are the kubectl subcommand forms a spec that applies
// a patch may run: exactly the ones the shipped specs and the generated apply
// use. nested is the one word that must follow (kubectl nests commands:
// `rollout status`, `set env`, and `apply edit-last-applied` opens an editor);
// noArgs means the subcommand takes no positional argument at all, which keeps
// `apply edit-last-applied` and its siblings out. Anything else can be a
// plugin, rewrite the kubeconfig, open $KUBE_EDITOR, run git or mint tokens.
// To add a form, change this table and add a test; a review reads it.
var remediationSubcommands = map[string]struct {
	nested string
	noArgs bool
}{
	"apply":   {noArgs: true},
	"delete":  {},
	"patch":   {},
	"rollout": {nested: "status"},
	"scale":   {},
	"set":     {nested: "env"},
	"wait":    {},
}

// allowedLongFlags are the only long flags a remediation spec may carry after
// the subcommand, and the form of each. true means a value is required and
// must be glued on with =, which is the only form the specs use, so no flag
// can take the next token as its value. false means the bare flag. These are
// every long flag the 61 kubectl commands in the shipped specs use. (The
// namespace is given before the subcommand and checked there.) To add one,
// change this table and add a test; a review reads it.
var allowedLongFlags = map[string]bool{
	"--ignore-not-found": false,
	"--grace-period":     true,
	"--type":             true,
	"--timeout":          true,
	"--for":              true,
	"--replicas":         true,
}

// refusedLongFlags are the kubectl flags that change which API server a
// command reaches, which kubeconfig or preferences file it reads, or what
// credentials and transport settings it uses. Names are compared after
// kubectl's own normalisation, which maps _ to - (k8s.io/component-base
// cli/flag WordSepNormalizeFunc). Nothing else is normalised: pflag looks a
// long flag up by exact name (flag.go parseLongArg, f.formal[...]), so
// --serv is an unknown flag, not --server; kubectl 1.35 answers "unknown flag:
// --serv". Only the first four change the cluster. The rest keep the address
// but cannot be shown harmless from argv (--proxy-url sends the traffic
// through a proxy of the author's choosing), and no spec needs any of them.
var refusedLongFlags = map[string]string{
	"context":    "a second --context, and only the one directly after kubectl is allowed",
	"kubeconfig": "--kubeconfig, which can point the same context name at another cluster",
	"kuberc":     "--kuberc, which swaps the preferences file that can alias and default commands",
	"server":     "--server, which sends the command to another API server whatever its --context says",
	"cluster":    "--cluster, which sends the command to another API server whatever its --context says",

	"user": "--user, which swaps the credentials", "token": "--token, which swaps the credentials",
	"username": "--username, which swaps the credentials", "password": "--password, which swaps the credentials",
	"as": "--as, which impersonates another identity", "as-group": "--as-group, which impersonates another identity",
	"as-uid": "--as-uid, which impersonates another identity", "as-user-extra": "--as-user-extra, which impersonates another identity",
	"certificate-authority":    "--certificate-authority, which changes who the server is trusted to be",
	"client-certificate":       "--client-certificate, which swaps the credentials",
	"client-key":               "--client-key, which swaps the credentials",
	"insecure-skip-tls-verify": "--insecure-skip-tls-verify, which stops checking who the server is",
	"tls-server-name":          "--tls-server-name, which changes who the server is trusted to be",
	"proxy-url":                "--proxy-url, which routes the command through a proxy",
}

// uncheckable says why the cluster one command reaches cannot be read from
// its argv, or returns "" when it can. The rule is positional so that no
// other flag can swallow the context as its value:
//
//   - argv[0] is kubectl and argv[1] is --context=NAME, the only --context
//     token anywhere in the command;
//   - no token anywhere is a refusedLongFlags flag in any spelling; those
//     name the connection and credential overrides, and are kept for their
//     message, since the allowlist below refuses them too;
//   - between kubectl and the subcommand only -n or --namespace with a value;
//   - the subcommand form is one of remediationSubcommands;
//   - after it, every token is an allowedLongFlags flag in its one form, one of
//     -f -p -l -n alone with its value in the next token, or a positional
//     argument. Both lists are allowlists because a deny list passes whatever
//     nobody thought of (--kustomize runs git, --profile-output truncates a
//     file, --edit opens an editor). Short flags can be grouped or carry their
//     value glued on (-Rs URL, -As=URL), so a test for the letter s cannot be
//     complete either;
//   - a -f value is "-" or a relative local path, and no positional argument is
//     a URL.
//
// A shell, env or any other wrapper can reach any cluster, a kubeconfig can
// give a familiar context name to a different one, and --server or --cluster
// override the context's own. The environment (KUBECONFIG) is not argv and is
// not checked here.
func uncheckable(argv []string) string {
	if len(argv) == 0 || argv[0] != "kubectl" {
		name := "nothing"
		if len(argv) > 0 {
			name = argv[0]
		}
		return fmt.Sprintf("it runs %s rather than kubectl directly", name)
	}
	if len(argv) < 2 || argv[1] == "--context" {
		return "it does not start kubectl --context=NAME; write the context as one token, --context=NAME, directly after kubectl"
	}
	if v, ok := strings.CutPrefix(argv[1], "--context="); !ok || v == "" {
		return "it does not start kubectl --context=NAME; write the context as one token, --context=NAME, directly after kubectl, so no other flag can take it as its value"
	}
	for _, a := range argv[2:] {
		if !strings.HasPrefix(a, "--") {
			continue
		}
		name := strings.ReplaceAll(strings.SplitN(a[2:], "=", 2)[0], "_", "-")
		if why, refused := refusedLongFlags[name]; refused {
			return "it carries " + why
		}
	}
	i := 2
	for i < len(argv) && strings.HasPrefix(argv[i], "-") {
		switch a := argv[i]; {
		case a == "-n" || a == "--namespace":
			if i+1 >= len(argv) || argv[i+1] == "" || strings.HasPrefix(argv[i+1], "-") {
				return fmt.Sprintf("its %s flag has no value", a)
			}
			i += 2
		case strings.HasPrefix(a, "--namespace=") && a != "--namespace=":
			i++
		default:
			return fmt.Sprintf("it puts %s before the subcommand, where only -n or --namespace with a value is allowed", a)
		}
	}
	if i >= len(argv) {
		return "it names no subcommand"
	}
	form, ok := remediationSubcommands[argv[i]]
	if !ok {
		return fmt.Sprintf("its subcommand %q is not one a remediation spec may run (allowed: apply, delete, patch, rollout status, scale, set env, wait); anything else can be a plugin, rewrite the kubeconfig, open an editor or fetch from a host", argv[i])
	}
	sub := argv[i]
	i++
	if form.nested != "" {
		if i >= len(argv) || argv[i] != form.nested {
			return fmt.Sprintf("%s must be followed by %q directly; other %s forms are not allowed", sub, form.nested, sub)
		}
		i++
	}
	for ; i < len(argv); i++ {
		a := argv[i]
		switch {
		case strings.HasPrefix(a, "--"):
			name, val, hasValue := strings.Cut(a, "=")
			needsValue, ok := allowedLongFlags[name]
			switch {
			case !ok:
				return fmt.Sprintf("it carries %s, which no remediation spec uses; the allowed long flags are %s. To add one, change allowedLongFlags in pkg/proof/remediate.go with a test, and have it reviewed", name, allowedLongFlagList())
			case needsValue && (!hasValue || val == ""):
				return fmt.Sprintf("%s needs its value glued on as %s=VALUE", name, name)
			case !needsValue && hasValue:
				return fmt.Sprintf("%s takes no value", name)
			}
		case a == "-f" || a == "-p" || a == "-l" || a == "-n":
			i++
			if i >= len(argv) || argv[i] == "" {
				return fmt.Sprintf("its %s flag has no value", a)
			}
			v := argv[i]
			switch {
			case a == "-f":
				if why := localFileProblem(v); why != "" {
					return fmt.Sprintf("its -f value %q %s", v, why)
				}
			case strings.HasPrefix(v, "-"):
				return fmt.Sprintf("the value %q of %s starts with a dash, so it could be another flag", v, a)
			}
			// A -p value is inline data and may mention a URL; kubectl does
			// not fetch it.
		case strings.HasPrefix(a, "-"):
			return fmt.Sprintf("it carries the token %s; only -f, -p, -l and -n are allowed, each alone, because short flags can be grouped or carry their value glued on (-Rs URL, -As=URL), and -s is kubectl's server override", a)
		default:
			if form.noArgs {
				return fmt.Sprintf("%s takes no positional argument here, and %q would be read as one (apply edit-last-applied and its siblings open an editor)", sub, a)
			}
			if hasURLScheme(a) {
				return fmt.Sprintf("the argument %q is a URL; a remediation names resources and local files only", a)
			}
		}
	}
	return ""
}

func allowedLongFlagList() string {
	var names []string
	for n, v := range allowedLongFlags {
		if v {
			n += "=VALUE"
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// localFileProblem says why a -f value is not a local, relative path or "-"
// (standard input), or returns "". The shipped specs only ever use "-" and
// paths below the repository root such as demo/manifests/x.yaml, so an
// absolute path and any .. segment are refused as well as every URL, because
// kubectl fetches any value with a scheme from its host. This is a check on
// the text of the path only: kubectl resolves it from the directory the proof
// is run from and follows symlinks, so a symlink checked into the tree, or a
// run from another directory, can still name a file outside the repository.
// Whatever it names is only ever sent to the pinned cluster.
func localFileProblem(v string) string {
	switch {
	case v == "-":
		return ""
	case strings.Contains(v, "://") || hasURLScheme(v):
		return "is a URL, and kubectl would fetch it from that host"
	case filepath.IsAbs(v) || strings.HasPrefix(v, "/") || strings.HasPrefix(v, "~"):
		return "is an absolute path; use a path relative to the repository root"
	}
	for _, seg := range strings.FieldsFunc(v, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return "climbs out with .., so it could leave the repository"
		}
	}
	return ""
}

// hasURLScheme reports whether s starts with scheme:// (RFC 3986 scheme).
func hasURLScheme(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 {
		return false
	}
	for j, r := range s[:i] {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case j > 0 && (r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.'):
		default:
			return false
		}
	}
	return true
}

// sustainFor is how long the signal must stay clear before the patch is
// credited, and how long the breach must hold before the patch is applied:
// the entry's own hold, and at least three polls.
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

// readingsFor is how many consecutive readings a hold of d must rest on at
// this poll: one at each end and one per poll between. Time alone is not
// enough, because with slow ticks two readings 35s apart span a 30s hold and
// nothing was seen between them.
func readingsFor(d, poll time.Duration) int {
	if poll <= 0 {
		return 2
	}
	return int((d+poll-1)/poll) + 1
}

// frozenBaseline lets the detector read the normal learned in warm-up and
// stops it learning anything more. The detector feeds every clear reading back
// into its baseline, which is right in production and wrong here: once the
// fault is in, every reading under a relative threshold is the fault's own
// level, and learning it raises the bar the patch is judged against. Measured
// in a test: warmed at 10 with a multiplier of 3, a useless patch that left
// the signal at 31 with one dip to 29 was credited with a recovery, because
// each clear reading raised the threshold over the next.
type frozenBaseline struct{ inner detect.BaselineStore }

func (frozenBaseline) Observe(string, float64) {}

func (f frozenBaseline) Baseline(key string, minSamples int) (float64, bool) {
	return f.inner.Baseline(key, minSamples)
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
		if res.Skipped != "" || res.Unchecked != "" || res.Blind || len(res.Errors) > 0 {
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
		res.setupError("%s. Nothing was run", plan.Err)
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

	// Deadlines the sustained breach or the sustained clear cannot fit inside
	// are arithmetic. A run against them would report a fault or a working
	// patch as broken, so they are refused before the cluster is touched.
	if s.FiresWithin.D() <= res.Sustain {
		res.setupError("firesWithin is %s but the breach must hold for %s before the patch is applied, so no run can reach the patch. Nothing was run",
			s.FiresWithin.D(), res.Sustain)
		return res
	}
	if s.Remediation.ClearsWithin.D() <= res.Sustain {
		res.setupError("remediation.clearsWithin is %s but the signal must stay clear for %s before the patch is credited, so no patch can pass. Nothing was run",
			s.Remediation.ClearsWithin.D(), res.Sustain)
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
	// is loud and a fix is not. Every undo command runs whatever the others
	// did, failed or panicked, each phase on its own bounded context that an
	// interrupt cannot cancel. What the undo finds goes into CleanupErrors
	// and never changes what the patch was seen to do.
	defer func() {
		revertCtx, cancelRevert := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancelRevert()
		res.Reverted = true
		for _, c := range s.Remediation.Revert {
			if err := r.undo(revertCtx, c); err != nil {
				res.Reverted = false
				res.CleanupErrors = append(res.CleanupErrors, fmt.Sprintf(
					"REVERT FAILED (%s): %v. The patch may still be on the testbed", c, err))
			}
		}
		resetCtx, cancelReset := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancelReset()
		res.Reset = true
		for _, c := range s.Reset {
			if err := r.undo(resetCtx, c); err != nil {
				res.Reset = false
				res.CleanupErrors = append(res.CleanupErrors, fmt.Sprintf(
					"RESET FAILED (%s): %v. The fault may still be on the testbed", c, err))
			}
		}
	}()

	r.Log("injecting the fault")
	for _, c := range s.Inject {
		if err := r.Exec(ctx, c.Run); err != nil {
			if ctx.Err() != nil {
				res.void("the run was interrupted while the fault was being injected", "Nothing was applied")
				return res
			}
			res.setupError("inject failed (%s): %v. Nothing was applied, so this says nothing about the patch", c, err)
			return res
		}
	}
	if s.Settle.D() > 0 {
		r.Log("settling for %s so rate windows fill", s.Settle.D())
		r.Sleep(ctx, s.Settle.D())
	}

	// From here to the verdict the learned normal is read and never written,
	// so the threshold below is the one every reading is judged against. No
	// detector has ticked since warm-up ended, so nothing has been learned
	// from the fault either.
	d := r.detectorFor(s)
	if d.Baseline != nil {
		d.Baseline = frozenBaseline{inner: d.Baseline}
	}
	target := detect.Target{Params: s.Target}
	threshold, _ := d.EffectiveThreshold(entry, target)
	res.Threshold = threshold

	// Wait for the breach to hold, measured on every tick, for the sustain
	// before applying anything. A remedy applied to a cluster that is fine
	// proves nothing, and one applied at the first breaching tick is credited
	// with any fault that decays by itself.
	//
	// This is a minimal control, not a full one. It shows the fault held
	// without the patch for as long as the clear must later hold with it; it
	// does not show the fault would have kept holding for the whole recovery
	// window, which only a second run without the patch could.
	//
	// The hold is the entry's own, so a held breach is the incident the entry
	// would have reported. The patch is rendered from the entry and the
	// target, which is exactly what the report renders it from, so the bytes
	// applied here are the bytes an operator would have merged.
	b := r.waitForHeldBreach(ctx, d, entry, s.FiresWithin.D(), res.Sustain)
	res.AtFire = b.value
	switch {
	case b.suspended > 0:
		res.void(fmt.Sprintf("the process was suspended for %s while waiting for the breach", b.suspended.Round(time.Second)),
			"Nothing was applied, and this says nothing about the entry or the patch")
		return res
	case b.held:
	case b.brokeOff:
		res.void(fmt.Sprintf("the fault did not hold without the patch: the signal was in breach for %s and then fell to %.4g, under the threshold of %.4g, before the %s hold the patch waits for",
			b.heldFor.Round(time.Second), b.fellTo, threshold, res.Sustain),
			"Nothing was applied. A fault that recovers on its own would be credited to any patch, so this run is not judged")
		return res
	case b.interrupted:
		res.void("the run was interrupted while waiting for the breach", "Nothing was applied")
		return res
	case b.gaps > 0:
		res.void(fmt.Sprintf("the entry's signal could not be measured on %d tick(s) while waiting for the breach to hold", b.gaps),
			"A gap restarts the hold, so not seeing one hold says nothing about the entry, and nothing was applied")
		return res
	case b.began:
		res.setupError("the breach began but had held for only %s of the %s required when firesWithin (%s) ran out, so there was no held incident to remedy. Nothing was applied, and this says nothing about the patch",
			b.heldFor.Round(time.Second), res.Sustain, s.FiresWithin.D())
		return res
	default:
		res.setupError("the fault never put the entry in breach within %s, so there was no incident to remedy. Nothing was applied, and this says nothing about the patch",
			s.FiresWithin.D())
		return res
	}
	res.Fired = true
	r.Log("%s in breach at %.4g and held for %s; applying the patch it proposes, with the fault still in place", s.Entry, b.value, b.heldFor.Round(time.Second))

	apply := r.ExecInput
	if apply == nil {
		apply = execCommandInput
	}
	limit := r.ApplyTimeout
	if limit <= 0 {
		limit = applyTimeout
	}
	applyCtx, cancelApply := context.WithTimeout(ctx, limit)
	err := apply(applyCtx, plan.Apply, plan.Patch)
	cancelApply()
	if err != nil {
		// The timeout is read from the error the command returned, not from
		// the context afterwards: a rejection that arrived just before the
		// deadline is still a rejection.
		switch {
		case ctx.Err() != nil:
			res.void("the run was interrupted while the patch was being applied",
				"Whether the patch landed is unknown, and the revert ran anyway")
		case errors.Is(err, context.DeadlineExceeded):
			res.setupError("the apply command did not finish within %s. Whether any part of the patch landed is unknown, so check the testbed before the next run, and this says nothing about the patch: %v", limit, err)
		case apiServerUnreachable(err):
			res.setupError("kubectl could not get an answer from the API server. Whether any part of the patch landed is unknown, so check the testbed before the next run, and this says nothing about the patch: %v", err)
		default:
			res.Failures = append(res.Failures, fmt.Sprintf(
				"the cluster refused the proposed patch: %v. The patch as the entry renders it could not be applied", err))
		}
		return res
	}
	res.Applied = true

	rec := r.waitForRecovery(ctx, d, entry, s.Remediation.ClearsWithin.D(), res.Sustain)
	res.Cleared, res.ClearedAfter, res.ClearReadings, res.AtEnd = rec.cleared, rec.clearedAfter, rec.readings, rec.value

	// The verdict is only as good as the bar it was judged against. The
	// baseline is frozen, so this cannot move today; if a later change to the
	// detector makes it move, the run is void rather than judged against a
	// number it does not report.
	if now, _ := d.EffectiveThreshold(entry, target); now != threshold {
		res.Cleared = false
		res.void(fmt.Sprintf("the threshold moved from %.4g to %.4g while the patch was being judged", threshold, now),
			"The verdict would not be against the threshold it reports, so this says nothing about the patch")
		return res
	}

	switch {
	case rec.suspended > 0:
		res.Cleared = false
		res.void(fmt.Sprintf("the process was suspended for %s while the patch was being judged", rec.suspended.Round(time.Second)),
			"The entry fired and the patch was applied; whether it worked is unmeasured")
		return res
	case rec.cleared:
		r.Log("the signal fell to %.4g after %s and held for %s over %d readings, with the fault untouched",
			rec.value, rec.clearedAfter.Round(time.Second), res.Sustain, rec.readings)
		return res
	case rec.interrupted:
		res.void("the run was interrupted while the patch was being judged",
			"The patch was applied; whether it worked is unmeasured")
		return res
	case rec.hidden:
		res.void(fmt.Sprintf("after the patch the entry's signal was unreadable on %d tick(s) and returned no series or NaN on %d, "+
			"and those ticks, or the time between slow ones, sat inside a stretch with no reading in breach long enough to have been a held clear",
			rec.blind, rec.absent),
			"What the observer missed could have been the clear the patch needed, and a signal with no series looks the same whether the patch stopped the errors or stopped the traffic, so this says nothing about the patch")
		return res
	}
	held := ""
	if rec.longestClear > 0 {
		held = fmt.Sprintf(", and the longest clear lasted %s of the %s required", rec.longestClear.Round(time.Second), res.Sustain)
	}
	res.Failures = append(res.Failures, fmt.Sprintf(
		"the signal was still at %.4g against a threshold of %.4g %s after the patch was applied%s: "+
			"the entry's breach held for its own hold, and the change it proposed did not stop it",
		rec.value, threshold, s.Remediation.ClearsWithin.D(), held))
	return res
}

// undo runs one cleanup command and turns a panic into an error, so one
// broken command cannot skip the ones after it. The return is named so the
// deferred recover can set it.
func (r *Runner) undo(ctx context.Context, c Command) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panicked: %v", p)
		}
	}()
	return r.Exec(ctx, c.Run)
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

// subjectEvaluation finds the entry's own evaluation in a cycle. A cycle
// without one is not a reading, and is treated as a gap.
func subjectEvaluation(c detect.Cycle, id string) (detect.Evaluation, bool) {
	for _, ev := range c.Evaluations {
		if ev.Scenario == id {
			return ev, true
		}
	}
	return detect.Evaluation{}, false
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

// measuredBreach is the other real reading: a finite value over the
// threshold.
func measuredBreach(ev detect.Evaluation) bool {
	return ev.Outcome == detect.OutcomeFiring && !math.IsNaN(ev.Value) && !math.IsInf(ev.Value, 0)
}

// breachWatch is what the wait for a held breach saw.
type breachWatch struct {
	value float64
	// held means the breach was measured on every tick for the sustain.
	held bool
	// began means at least one reading was in breach, and heldFor is how
	// long the last unbroken run of them lasted.
	began   bool
	heldFor time.Duration
	// brokeOff means a measured clear came after the breach began: the fault
	// did not hold without the patch, and fellTo is where it fell.
	brokeOff bool
	fellTo   float64
	// gaps counts ticks that were not a measurement.
	gaps        int
	suspended   time.Duration
	interrupted bool
}

// waitForHeldBreach ticks until the entry's signal has been measured in
// breach on every tick for sustain, with enough readings to span it. A gap
// restarts the hold, because a breach nobody watched is not a breach that
// held. A measured clear once the breach has begun ends the wait: the fault
// let go without the patch.
func (r *Runner) waitForHeldBreach(ctx context.Context, d *detect.Detector, entry catalog.Scenario, budget, sustain time.Duration) breachWatch {
	var w breachWatch
	need := readingsFor(sustain, r.Poll)
	deadline := r.Now().Add(budget)
	lastTick := r.Wall()
	var since time.Time
	n := 0
	for r.Now().Before(deadline) && ctx.Err() == nil {
		if gap, ok := r.suspendedSince(lastTick); ok {
			w.suspended = gap
			return w
		}
		lastTick = r.Wall()

		now := r.Now()
		ev, ok := subjectEvaluation(d.Tick(ctx, now), entry.ID)
		switch {
		case ok && measuredBreach(ev):
			if n == 0 {
				since = now
			}
			n++
			w.began, w.value = true, ev.Value
			w.heldFor = now.Sub(since)
			if w.heldFor >= sustain && n >= need {
				w.held = true
				return w
			}
		case ok && measuredClear(ev):
			if w.began {
				w.brokeOff, w.fellTo = true, ev.Value
				return w
			}
			w.value = ev.Value
		default:
			w.gaps++
			n = 0
		}
		r.Sleep(ctx, r.Poll)
	}
	w.interrupted = ctx.Err() != nil
	return w
}

// recoveryWatch is what the wait for a held clear saw.
type recoveryWatch struct {
	value float64
	// blind counts ticks where the subject's evaluation was blind or
	// missing, and absent counts clear readings that were not a measurement:
	// no series, or a NaN or infinite value.
	blind  int
	absent int
	// hidden means a stretch with no reading in breach lasted the sustain
	// without being a measured held clear, because of a gap or because the
	// ticks were too far apart to count. Had the observer seen it all, the
	// run might have passed there.
	hidden      bool
	suspended   time.Duration
	interrupted bool

	cleared      bool
	clearedAfter time.Duration
	readings     int
	longestClear time.Duration
}

// waitForRecovery ticks until the signal has been measurably clear for
// sustain, on enough consecutive readings to span it, or the budget runs out.
// Any tick that is not a measured clear, in breach, blind or absent, breaks
// the measured run: two clears either side of a gap are not one long clear.
//
// Alongside it runs an optimistic count that treats every unmeasured tick as
// if it had been clear and is broken only by a reading in breach. If that
// count ever spans the sustain, what the observer missed could have hidden a
// pass, and a run that did not clear is void rather than a FAIL. If it never
// does, readings in breach rule out any held clear the gaps might have hidden,
// so a single blind tick in a run watched failing does not excuse the patch.
func (r *Runner) waitForRecovery(ctx context.Context, d *detect.Detector, entry catalog.Scenario, budget, sustain time.Duration) recoveryWatch {
	var w recoveryWatch
	need := readingsFor(sustain, r.Poll)
	start := r.Now()
	deadline := start.Add(budget)
	lastTick := r.Wall()
	var streakStart, maybeStart time.Time
	n := 0
	for r.Now().Before(deadline) && ctx.Err() == nil {
		if gap, ok := r.suspendedSince(lastTick); ok {
			w.suspended = gap
			return w
		}
		lastTick = r.Wall()

		now := r.Now()
		ev, ok := subjectEvaluation(d.Tick(ctx, now), entry.ID)
		breach := ok && measuredBreach(ev)
		switch {
		case ok && measuredClear(ev):
			w.value = ev.Value
			if n == 0 {
				streakStart = now
			}
			n++
			held := now.Sub(streakStart)
			if held > w.longestClear {
				w.longestClear = held
			}
			if held >= sustain && n >= need {
				w.cleared, w.clearedAfter, w.readings = true, streakStart.Sub(start), n
				return w
			}
		case breach:
			w.value = ev.Value
			n = 0
		case ok && ev.Outcome == detect.OutcomeClear:
			w.absent++
			n = 0
		default:
			w.blind++
			n = 0
		}

		if breach {
			maybeStart = time.Time{}
		} else {
			if maybeStart.IsZero() {
				maybeStart = now
			}
			if now.Sub(maybeStart) >= sustain {
				w.hidden = true
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
