#!/usr/bin/env bash
#
# Tests for hack/compat-pilot/observe.sh with stub helm, kubectl and istioctl.
# No cluster, no network, no sleeping. Run from anywhere:
#
#   hack/compat-exp/test/observe_test.sh
#
# Each test builds a small copy of the repository layout in a temp dir (the
# script finds hack/compat-exp/patches/ relative to itself), puts the stubs on
# PATH, runs observe.sh and asserts on the JSON record with jq.
# The A && B || C chains below are deliberate: ok and bad never fail.
# shellcheck disable=SC2015
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); echo "ok   $1"; }
bad() { fail=$((fail + 1)); echo "FAIL $1"; }
# check NAME CMD...: passes when the command succeeds.
check() { local name="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name"; fi; }
# jqok NAME FILE FILTER: passes when the filter prints true.
jqok() { [[ "$(jq -r "$3" "$2" 2>/dev/null)" == "true" ]] && ok "$1" || { bad "$1"; jq -c "$3" "$2" 2>&1 | head -3; }; }

# --- the fake repository and the stubs ----------------------------------------

mkdir -p "$T/repo/hack/compat-pilot" "$T/repo/hack/compat-exp/patches" "$T/bin" "$T/state" "$T/stub"
cp "$repo/hack/compat-pilot/observe.sh" "$T/repo/hack/compat-pilot/observe.sh"
cp "$repo/hack/compat-pilot/charts.json" "$T/repo/hack/compat-pilot/charts.json"
export STUB_DIR="$T/stub"

cat >"$T/bin/helm" <<'STUB'
#!/usr/bin/env bash
echo "helm $*" >>"$STUB_DIR/helm.log"
case "$1" in
  pull)
    # helm pull CHART ... --untardir DIR: lay down the fixture chart.
    while [[ $# -gt 0 ]]; do [[ "$1" == --untardir ]] && dir="$2"; shift; done
    mkdir -p "$dir" && cp -R "$STUB_DIR/fixture-chart" "$dir/fixture-chart" ;;
  lint) exit "${STUB_LINT_RC:-0}" ;;
  install) exit "${STUB_INSTALL_RC:-0}" ;;
  get) cat "$STUB_DIR/hooks.txt" 2>/dev/null ;;
esac
exit 0
STUB

cat >"$T/bin/istioctl" <<'STUB'
#!/usr/bin/env bash
echo '[]'
STUB

cat >"$T/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
d="$STUB_DIR"
echo "kubectl $*" >>"$d/kubectl.log"
a=" $* "
serve() { if [[ -f "$d/$1" ]]; then cat "$d/$1"; else echo '{"items":[]}'; fi; }
case "$a" in
  *" config get-contexts "*) echo "${STUB_CONTEXT:-kind-mmc-w0}" ;;
  *" get pods -o json "*)
    n=$(($(cat "$d/pods.n" 2>/dev/null || echo 0) + 1)); echo "$n" >"$d/pods.n"
    if [[ -f "$d/pods.$n.json" ]]; then cat "$d/pods.$n.json"; else serve pods.json; fi ;;
  *" get events "*) echo '{"items":[]}' ;;
  *" get jobs "*) serve jobs.json ;;
  *" get svc "*) serve svc.json ;;
  *" get validatingwebhookconfigurations "*) serve validating.json ;;
  *" get mutatingwebhookconfigurations "*) serve mutating.json ;;
  *" get apiservices "*) serve apiservices.json ;;
  *" get --raw "*)
    while [[ $# -gt 0 ]]; do [[ "$1" == --raw ]] && p="$2"; shift; done
    f="$d/raw/$(sed -n 's|.*/services/\([^/]*\)/proxy/.*|\1|p' <<<"$p")"
    if [[ ! -f "$f" ]]; then echo "Error from server (NotFound): no stub for $p" >&2; exit 1; fi
    rc="$(head -1 "$f")"
    if [[ "$rc" == 0 ]]; then tail -n +2 "$f"; else tail -n +2 "$f" >&2; fi
    exit "$rc" ;;
  *" apply "*) cat >/dev/null ;;
esac
exit 0
STUB
chmod +x "$T/bin/"*

# --- helpers --------------------------------------------------------------------

# reset: clear the stub data between tests.
reset() {
  rm -rf "$T/stub" "$T/out" "$T/patches-made"
  mkdir -p "$T/stub/raw" "$T/out"
  rm -f "$T/repo/hack/compat-exp/patches/"*
  unset STUB_INSTALL_RC STUB_LINT_RC
}

# world_files WORLD: what world.sh would have left behind.
world_files() {
  local c; c="mmc-$(tr '[:upper:]' '[:lower:]' <<<"$1")"
  : >"$T/state/$c.kubeconfig"
  printf 'istio=1.31.3\nkubernetes=1.35\n' >"$T/state/$c.world"
}

# run_observe CHARTS_FILE WORLD ID RUN: rc in $rc, record in $rec if written.
run_observe() {
  local cf="$1" world="$2" id="$3" run="$4"
  world_files "$world"
  rec="$T/out/$id.$world.run$run.json"
  env PATH="$T/bin:$PATH" STUB_CONTEXT="kind-mmc-$(tr '[:upper:]' '[:lower:]' <<<"$world")" \
    MMC_STATE="$T/state" MMC_SETTLE=0 MMC_LATE=0 ${cf:+CHARTS_FILE="$cf"} \
    bash "${OBSERVE:-$T/repo/hack/compat-pilot/observe.sh}" "$world" "$id" "$run" "$T/out" \
    >"$T/stdout" 2>"$T/stderr"
  rc=$?
}

# A chart "fixture-chart" with one template, and the diffs against it.
make_fixture_chart() {
  mkdir -p "$T/stub/fixture-chart/templates"
  printf 'apiVersion: v2\nname: fixture-chart\nversion: 1.0.0\n' >"$T/stub/fixture-chart/Chart.yaml"
  printf 'replicas: 1\nextra: false\n' >"$T/stub/fixture-chart/values.yaml"
  printf 'kind: ConfigMap\n' >"$T/stub/fixture-chart/templates/cm.yaml"
}
good_patch='--- a/values.yaml
+++ b/values.yaml
@@ -1,2 +1,2 @@
 replicas: 1
-extra: false
+extra: true
'
bad_patch='--- a/values.yaml
+++ b/values.yaml
@@ -1,2 +1,2 @@
 replicas: 9
-extra: nope
+extra: true
'

# pod JSON helpers
# pod NAME OWNERKIND OWNERNAME EXTRA_JQ: one running pod, adjusted by a jq filter.
pod() {
  jq -nc --arg n "$1" --arg ok "$2" --arg on "$3" '{
    metadata: {name: $n, labels: {}, annotations: {}, ownerReferences: [{kind: $ok, name: $on}]},
    spec: {containers: [{name: "app"}]},
    status: {phase: "Running", conditions: [{type: "Ready", status: "True"}],
             containerStatuses: [{name: "app", ready: true, restartCount: 0, state: {running: {}}, image: "img"}]}
  }' | jq -c "${4:-.}"
}
# pods_file FILE POD...
pods_file() { local f="$1"; shift; printf '%s\n' "$@" | jq -s '{items: .}' >"$f"; }

charts_one() { # FILE with a single chart entry given as JSON
  printf '[%s]\n' "$2" >"$1"
}

# --- item 3: CHARTS_FILE and the patch fix form -----------------------------------

echo "# item 3"

# The default is the pilot's list, so the pilot workflows keep working.
reset
run_observe "" W0 podinfo 1
[[ $rc -eq 0 ]] && jqok "default CHARTS_FILE reads the pilot list" "$rec" '.chart == "podinfo" and .chartVersion == "6.15.0"' ||
  bad "default CHARTS_FILE reads the pilot list (rc=$rc)"
check "default run installs from the pinned chart version" grep -q -- "install podinfo oci://ghcr.io/stefanprodan/charts/podinfo .*--version 6.15.0" "$T/stub/helm.log"
jqok "no patch recorded for an unpatched chart" "$rec" '.patch == null'

# An unknown id is a usage error against the file named by CHARTS_FILE.
reset
charts_one "$T/c.json" '{"id":"a","chart":"c","version":"1"}'
run_observe "$T/c.json" W0 nope 1
[[ $rc -eq 2 ]] && ok "unknown id in CHARTS_FILE exits 2" || bad "unknown id in CHARTS_FILE exits 2 (rc=$rc)"

# A values fix, with CHARTS_FILE pointing elsewhere.
reset
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"oci://example/app","version":"2.0.0","values":["--set","x=1"]}'
run_observe "$T/c.json" W0 app-fix 1
[[ $rc -eq 0 ]] && jqok "values fix: record names the fixed release" "$rec" '.chart == "app-fix" and .fixOf == "app" and .patch == null' || bad "values fix (rc=$rc)"
check "values fix: install uses the release of the chart it fixes, with the values" grep -q -- "install app oci://example/app .*--version 2.0.0.*--set x=1" "$T/stub/helm.log"
check "values fix: no helm pull" bash -c "! grep -q '^helm pull' '$T/stub/helm.log'"

# A patch fix that applies.
reset
make_fixture_chart
printf '%s' "$good_patch" >"$T/repo/hack/compat-exp/patches/ok.diff"
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"oci://example/app","version":"2.0.0","patch":"hack/compat-exp/patches/ok.diff"}'
run_observe "$T/c.json" W0 app-fix 1
want_sha="$(shasum -a 256 "$T/repo/hack/compat-exp/patches/ok.diff" | cut -d' ' -f1)"
[[ $rc -eq 0 ]] && jqok "patch fix: sha256, line count and changed lines recorded" "$rec" \
  ".patch == {path: \"hack/compat-exp/patches/ok.diff\", sha256: \"$want_sha\", lines: 6, changedLines: 2}" || bad "patch fix applies (rc=$rc: $(cat "$T/stderr"))"
check "patch fix: helm pull --untar of the pinned version" grep -q -- "^helm pull oci://example/app --version 2.0.0 --untar --untardir" "$T/stub/helm.log"
check "patch fix: helm lint called on the patched directory" grep -q "^helm lint .*fixture-chart" "$T/stub/helm.log"
check "patch fix: installed from the patched directory under the fixed release name" grep -q "^helm install app .*/fixture-chart --namespace pilot" "$T/stub/helm.log"
check "patch fix: the install has no --version for a directory" bash -c "! grep '^helm install' '$T/stub/helm.log' | grep -q -- --version"

# A patch that does not apply fails the run loudly, before any namespace exists.
reset
make_fixture_chart
printf '%s' "$bad_patch" >"$T/repo/hack/compat-exp/patches/bad.diff"
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"oci://example/app","version":"2.0.0","patch":"hack/compat-exp/patches/bad.diff"}'
run_observe "$T/c.json" W0 app-fix 1
[[ $rc -eq 3 && ! -f "$rec" ]] && grep -q "does not apply" "$T/stderr" && ok "patch that does not apply: exit 3, no record, loud message" ||
  bad "patch that does not apply (rc=$rc)"
check "patch that does not apply: no install, no namespace" bash -c "! grep -q '^helm install' '$T/stub/helm.log' && ! grep -q 'create namespace' '$STUB_DIR/kubectl.log'"

# helm lint failing on the patched chart also fails the run.
reset
make_fixture_chart
printf '%s' "$good_patch" >"$T/repo/hack/compat-exp/patches/ok.diff"
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"oci://example/app","version":"2.0.0","patch":"hack/compat-exp/patches/ok.diff"}'
STUB_LINT_RC=1 run_observe "$T/c.json" W0 app-fix 1
[[ $rc -eq 3 && ! -f "$rec" ]] && grep -q "lint" "$T/stderr" && ok "helm lint failure: exit 3, no record" || bad "helm lint failure (rc=$rc)"
export -n STUB_LINT_RC

# A patch outside hack/compat-exp/patches/ is refused, in each spelling.
for p in "hack/compat-pilot/x.diff" "/etc/passwd" "hack/compat-exp/patches/../../compat-pilot/charts.json" "hack/compat-exp/patchesx/a.diff" "hack/compat-exp/patches/"; do
  reset
  make_fixture_chart
  printf '%s' "$good_patch" >"$T/repo/hack/compat-pilot/x.diff"
  jq -nc --arg p "$p" '[{id:"app-fix",fixOf:"app",chart:"oci://example/app",version:"2.0.0",patch:$p}]' >"$T/c.json"
  run_observe "$T/c.json" W0 app-fix 1
  [[ $rc -eq 2 && ! -f "$rec" ]] && ok "patch path refused: $p" || bad "patch path refused: $p (rc=$rc)"
done
check "refused patch path never reaches helm" bash -c "! test -f '$STUB_DIR/helm.log'"

# A patch over 40 changed lines is refused; exactly 40 is accepted.
mkpatch() { # N: a diff against a values.yaml of N lines changing all of them
  local n="$1" i
  { echo '--- a/values.yaml'; echo '+++ b/values.yaml'; echo "@@ -1,$n +1,$n @@"
    for ((i = 1; i <= n; i++)); do echo "-l$i"; done
    for ((i = 1; i <= n; i++)); do echo "+m$i"; done; }
}
reset
make_fixture_chart
seq 1 21 | sed 's/^/l/' >"$T/stub/fixture-chart/values.yaml"
mkpatch 21 >"$T/repo/hack/compat-exp/patches/big.diff"
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"c","version":"1","patch":"hack/compat-exp/patches/big.diff"}'
run_observe "$T/c.json" W0 app-fix 1
[[ $rc -eq 2 && ! -f "$rec" ]] && grep -q "42 lines" "$T/stderr" && ok "patch over 40 changed lines (42) refused" || bad "patch over 40 changed lines (rc=$rc)"
reset
make_fixture_chart
seq 1 20 | sed 's/^/l/' >"$T/stub/fixture-chart/values.yaml"
mkpatch 20 >"$T/repo/hack/compat-exp/patches/edge.diff"
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"c","version":"1","patch":"hack/compat-exp/patches/edge.diff"}'
run_observe "$T/c.json" W0 app-fix 1
[[ $rc -eq 0 ]] && jqok "patch of exactly 40 changed lines accepted" "$rec" '.patch.changedLines == 40' || bad "patch of exactly 40 lines (rc=$rc: $(cat "$T/stderr"))"

# --- the pilot's observe.sh and this one agree on everything that existed ----------

echo "# compatibility with the pilot instrument"

# The reference is the script before these changes: COMPAT_BASE (default
# origin/main) names the commit to take it from. Same stubs, same inputs; every
# field and key that existed before must be identical, and the new ones are
# additions only.
base="${COMPAT_BASE:-origin/main}"
if git -C "$repo" show "$base:hack/compat-pilot/observe.sh" >"$T/observe.old.sh" 2>/dev/null &&
  ! grep -q CHARTS_FILE "$T/observe.old.sh"; then
  norm='del(.helm.durationSeconds, .patch, .podMesh, .capacityPending, .lateWorkloads)
        | .obs |= with_entries(select(.key | test("^(apiserver|late):") | not))'
  for world in W0 W1 W3; do
    reset
    pods_file "$T/stub/pods.json" \
      "$(pod web-7d9f-abc ReplicaSet web-7d9f)" \
      "$(pod db-0 StatefulSet db '.status.containerStatuses[0] |= (.ready = false | .restartCount = 3 | .state = {waiting: {reason: "CrashLoopBackOff", message: "back-off"}})')" \
      "$(pod init-xyz Job init '.status.initContainerStatuses = [{name: "i", ready: false, restartCount: 0, image: "reg/x:1", state: {waiting: {reason: "ImagePullBackOff"}}}]')" \
      "$(pod mmc-client Pod mmc-client '.metadata.labels["mmc-client"] = "true"')"
    cat >"$T/stub/svc.json" <<'JSON'
{"items":[{"metadata":{"name":"web"},"spec":{"type":"ClusterIP","clusterIP":"10.0.0.1","ports":[{"port":80,"name":"http","protocol":"TCP"}]}}]}
JSON
    cat >"$T/stub/jobs.json" <<'JSON'
{"items":[{"metadata":{"name":"init"},"status":{"conditions":[{"type":"Complete","status":"True"}]}}]}
JSON
    echo "web-pre-hook" >"$T/stub/hooks.txt"
    charts_one "$T/c.json" '{"id":"app","chart":"oci://example/app","version":"1.0.0","values":["--set","a=b"]}'
    cp "$T/c.json" "$T/charts.json" # the old script reads the list next to itself
    OBSERVE="$T/observe.old.sh" run_observe "" "$world" app 1
    old_rc=$rc; cp "$rec" "$T/old.json" 2>/dev/null
    rm -f "$rec"; rm -f "$T/stub/pods.n"
    OBSERVE="$T/repo/hack/compat-pilot/observe.sh" run_observe "$T/c.json" "$world" app 1
    new_rc=$rc
    [[ $old_rc -eq 0 && $new_rc -eq 0 ]] || { bad "old and new both record in $world (old=$old_rc new=$new_rc)"; continue; }
    if [[ "$(jq -S "$norm" "$T/old.json")" == "$(jq -S "$norm" "$rec")" ]]; then
      ok "$world: every pre-existing field and key identical to the pilot's observe.sh"
    else
      bad "$world: records differ"; diff <(jq -S "$norm" "$T/old.json") <(jq -S "$norm" "$rec") | head -10
    fi
  done
else
  bad "no pre-change observe.sh at $base to compare against (set COMPAT_BASE to the commit before these changes)"
fi

echo
echo "$pass passed, $fail failed"
[[ $fail -eq 0 ]]
