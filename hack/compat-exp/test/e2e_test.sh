#!/usr/bin/env bash
#
# One end-to-end run of the instrument with stubs: every file one tool writes is
# the file the next tool reads. Only helm, kubectl, istioctl and gh are stubs.
#
#   hack/compat-exp/test/e2e_test.sh
#
# The chain, in the order of hack/compat-exp/README.md:
#   candidates.json -> frame charts -> (qualification records) -> select.sh ->
#   charts-exp.json -> guard.sh plan -> charts.json -> observe.sh x (W0,W1,W3 x 3 runs)
#   -> difftable -json (run 1, run 2) -> certify: guard.sh plan, observe.sh, difftable
#   -cell-worlds -> recheck -> analyze.sh (gh stub) = ledger check + static.sh (helm
#   stub render + staticcheck) + crossrun.
#
# HAND-MADE INTERMEDIATES (no tool of the instrument writes them here):
#   1. candidates.json: frame build needs 300 stored API responses; the file is
#      generated in frame's output shape (the key set is compared with the golden).
#   2. the qualification records <id>.qualify.json: qualify.sh needs a cluster; they
#      are generated with the fields select.sh reads. qualify.sh -> select.sh is
#      covered by qualify_test.sh.
#   3. the ledger row and fix entry and ignore list: judgment data a person writes.
#   4. the stub cluster's pod JSON (what the "chart" does), the rendered manifests
#      helm template returns, and gh's run view (start and end times). The run metadata
#      file is not hand-made: the workflow's own "run metadata" step writes it.
#   5. the records of the 47 sample charts that are not run: copies of app-1's artifact
#      directories under their own ids (crossrun needs the 50 primary charts).
#
# RUN FOR REAL, from the text of .github/workflows/compat-exp.yml (test/lib-runmeta.sh
# wf_step): the guard job's "run metadata" step; the observe job's "three runs" step
# (once per chart and world, its results/ becoming the artifact exp-<run id>-<chart>-<world>);
# the report job's "check records" and "difftable" steps, whose upload paths (report-*.json,
# table.md, results/) become the artifact compat-exp-report-<run id>; the select job's
# "check records" and "select" steps, whose out/ and qual/ become charts-exp-<run id>. The
# stub gh serves those trees unchanged.
# STILL HAND-BUILT because they need the runner, an action or a cluster: the upload and
# download of artifacts (GitHub's own), checkout, tool installation, world.sh (the world's
# state files and the stub cluster), qualify.sh and the qualify job (needs a cluster; its
# artifacts qual-<run>-<chart> hold hand-made records), the guard job's guard and ledger
# steps (guard.sh plan is run directly), and `go run ./hack/compat-pilot/difftable`, which a
# `go` shim serves with the prebuilt binary.
# shellcheck disable=SC2015,SC2016
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
# shellcheck source=lib-runmeta.sh
source "$here/lib-runmeta.sh"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); echo "ok   $1"; }
bad() { fail=$((fail + 1)); echo "FAIL $1"; }
eq() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1: got '$2', want '$3'"; fi; }
must() { local name="$1"; shift; if "$@" >"$T/last.out" 2>&1; then ok "$name"; else bad "$name"; head -8 "$T/last.out"; fi; }

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.test GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.test

# --- the tools ------------------------------------------------------------------------

B="$T/tools"
mkdir -p "$B"
for p in frame:./hack/compat-exp/frame difftable:./hack/compat-pilot/difftable ledger:./hack/compat-exp/ledger/cmd/ledger \
  crossrun:./hack/compat-exp/crossrun staticcheck:./hack/compat-exp/staticcheck; do
  (cd "$repo" && go build -o "$B/${p%%:*}" "${p#*:}") || { echo "build of ${p%%:*} failed" >&2; exit 1; }
done

# --- 1. candidates.json -> frame charts ---------------------------------------------------

# 55 candidates; ranks 4 and 5 share a package name, so their ids are <repository>-<name>.
mkdir -p "$T/frame"
jq -n '{input_sha256: "x", exclusions_sha256: "y", candidates: [range(1; 56) as $i | {
    rank: $i, stars: (10000 - $i),
    package: (if $i == 4 or $i == 5 then "dup" else "app-\($i)" end),
    repository: "repo\($i)", repository_url: "https://example.test/\($i)",
    publisher: "pub\($i)", publisher_source: "github-source-link", publisher_link: "https://github.com/pub\($i)",
    repo: "https://example.test/\($i)", chart: (if $i == 4 or $i == 5 then "dup" else "app-\($i)" end), version: "1.0.\($i)"}]}' >"$T/frame/candidates.json"
eq "candidates.json has frame's key set" "$(jq -c '.candidates[0] | keys' "$T/frame/candidates.json")" "$(jq -c '.candidates[0] | keys' "$repo/hack/compat-exp/frame/testdata/candidates.golden.json")"
must "frame charts" "$B/frame" charts -in "$T/frame/candidates.json" -out "$T/frame/candidates-charts.json"

# --- 2. qualification records -> select.sh ---------------------------------------------------

mkdir -p "$T/qual"
jq -r '.[].id' "$T/frame/candidates-charts.json" | while read -r id; do
  jq -n --arg id "$id" '{id: $id, qualified: true, firstFailure: null,
    installs: [{n:1,durationSeconds:120,ready:true,helmRc:0},{n:2,durationSeconds:118,ready:true,helmRc:0},{n:3,durationSeconds:119,ready:true,helmRc:0}],
    bias: {hasStatefulSet: false, hasNetworkPolicy: false, hasNonHttpPort: false, hasHeadlessService: false}}' >"$T/qual/$id.qualify.json"
done
in_ws() { local d="$1"; shift; (cd "$d" && env "$@"); }   # run a command in a workspace directory, with env assignments
# the qualify job's artifacts qual-<run>-<chart> (hand-made records: qualify.sh needs a cluster), downloaded
# per artifact by the select job, then the select job's own two steps run for real: "check records" and "select"
SEL="$T/ws-select"
mkdir -p "$SEL/qual-artifacts" "$SEL/out"
cp -R "$repo/hack" "$SEL/hack"
cp "$T/frame/candidates.json" "$SEL/hack/compat-exp/candidates.json"
for f in "$T/qual"/*.qualify.json; do
  id="$(basename "$f" .qualify.json)"
  mkdir -p "$SEL/qual-artifacts/qual-7001-$id"
  cp "$f" "$SEL/qual-artifacts/qual-7001-$id/"
done
sel_check="$(wf_step "$repo/.github/workflows/compat-exp.yml" select "check records")"
sel_run="$(wf_step "$repo/.github/workflows/compat-exp.yml" select select)"
must "workflow step: select job, check records" in_ws "$SEL" RUN_ID=7001 bash -c "$sel_check"
must "workflow step: select job, select" in_ws "$SEL" GITHUB_RUN_ID=7001 GITHUB_STEP_SUMMARY="$T/summary.md" bash -c "$sel_run"
cp "$SEL/out/charts-exp.json" "$T/charts-exp.json"
eq "select: 50 charts and 5 reserves" "$(jq -c '[(.charts | length), (.reserves | length)]' "$T/charts-exp.json")" "[50,5]"
eq "ids: frame charts and select.sh agree, in order" "$(jq -c '[.charts[].id, .reserves[].id]' "$T/charts-exp.json")" "$(jq -c '.[0:55] | map(.id)' "$T/frame/candidates-charts.json")"
eq "ids: the duplicate package is <repository>-<name> in both" "$(jq -c '.charts[3:5] | map(.id)' "$T/charts-exp.json")" '["repo4-dup","repo5-dup"]'
eq "select: publisher and qualified carried for crossrun" "$(jq -c '.charts[1] | {publisher, qualified}' "$T/charts-exp.json")" '{"publisher":"pub2","qualified":true}'
id1=app-1 id2=app-2 id3=app-3

# --- 3. the freeze and the plan --------------------------------------------------------------

# A throwaway repository holding the real instrument, tagged, then the frozen list committed.
R="$T/repo"
mkdir -p "$R/docs" "$R/.github"
cp -R "$repo/hack" "$R/hack"
cp -R "$repo/.github/workflows" "$R/.github/workflows"
# the recheck version is decided in the freeze commit (the workflow's marked line)
sed -i.bak 's/"recheck-version-undecided"/"1.32.0"/' "$R/.github/workflows/compat-exp.yml" && rm -f "$R/.github/workflows/compat-exp.yml.bak"
cp "$repo/docs/compat-experiment.md" "$R/docs/"
cp "$repo/go.mod" "$repo/go.sum" "$R/"
cp "$T/frame/candidates.json" "$R/hack/compat-exp/candidates.json"
cp "$T/frame/candidates-charts.json" "$R/hack/compat-exp/candidates-charts.json"
git -C "$R" init -q -b main && git -C "$R" add -A && git -C "$R" commit -q -m instrument && git -C "$R" tag compat-exp-freeze
cp "$T/charts-exp.json" "$R/hack/compat-exp/charts-exp.json"
git -C "$R" add hack/compat-exp/charts-exp.json && git -C "$R" commit -q -m "frozen list"

guard() { # MODE [VAR=VAL...]: plan into $T/plan-$MODE
  local m="$1"; shift
  env -i PATH="$PATH" HOME="$HOME" GUARD_ROOT="$R" GUARD_MODE="$m" GUARD_ISTIO=1.31.3 ISTIO_PRIMARY=1.31.3 "$@" \
    "$repo/hack/compat-exp/guard.sh" plan "$T/plan-$m" >"$T/plan-$m.out" 2>"$T/plan-$m.err"
}
must_guard() { local name="$1"; shift; if guard "$@"; then ok "$name"; else bad "$name"; cat "$T/plan-$2.err" 2>/dev/null | head -5; fi; }
must_guard "guard plan observe on the real instrument and select.sh's list" observe GUARD_CHART_IDS="$id1 $id2 $id3"
eq "plan: the matrix is the three charts x W0 W1 W3" "$(grep '^count=' "$T/plan-observe.out")" "count=9"
eq "plan: charts.json holds the reserves too (flat list observe.sh reads)" "$(jq length "$T/plan-observe/charts.json")" 55

# --- 4. the stub cluster ------------------------------------------------------------------------

S="$T/stub"
mkdir -p "$T/bin" "$S/render" "$T/state"
cat >"$T/bin/helm" <<'STUB'
#!/usr/bin/env bash
case "$1" in
  version) echo "v4.0.4+gabcdef" ;;
  template)
    f="$STUB_RENDER/$2.yaml"
    [[ -f "$f" ]] || f="$STUB_RENDER/default.yaml"
    cat "$f" ;;
  get) [[ -z "${STUB_HOOKS:-}" ]] || echo "helm.sh/hook: test" ;;
  test)
    # helm test ran: the next pod listings (two if it failed: the log listing, then
    # the instrument's capture; one if it passed) show the pods it created
    [[ -z "${STUB_PODS_AFTER:-}" ]] || echo "$([[ "${STUB_TEST_RC:-0}" == 0 ]] && echo 1 || echo 2)" >"$STUB_PODS_AFTER.left"
    exit "${STUB_TEST_RC:-0}" ;;
esac
exit 0
STUB
cat >"$T/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
a=" $* "
case "$a" in
  *" config get-contexts "*) echo "$STUB_CONTEXT" ;;
  *" get pods -o json "*)
    if [[ -n "${STUB_PODS_AFTER:-}" && -f "$STUB_PODS_AFTER.left" ]]; then
      left="$(cat "$STUB_PODS_AFTER.left")"
      if [[ "$left" -le 1 ]]; then rm -f "$STUB_PODS_AFTER.left"; else echo $((left - 1)) >"$STUB_PODS_AFTER.left"; fi
      cat "$STUB_PODS_AFTER"
    else cat "$STUB_PODS"; fi ;;
  *" get "*" -o json "*) echo '{"items":[]}' ;;
  *" get --raw "*) echo "Error from server (NotFound): no stub" >&2; exit 1 ;;
  *" apply "*) cat >/dev/null ;;
esac
exit 0
STUB
cat >"$T/bin/istioctl" <<'STUB'
#!/usr/bin/env bash
echo '[]'
STUB
chmod +x "$T/bin/"*
export STUB_RENDER="$S/render"
# What a chart "does": a Deployment pod, ready or not.
mkpods() { # READY(true|false) NAME > file
  jq -n --argjson r "$1" --arg n "$2" '{items: [{
    metadata: {name: ($n + "-abc"), labels: {}, ownerReferences: [{kind: "ReplicaSet", name: ($n + "-7d9f8")}]},
    spec: {containers: [{name: "app"}]},
    status: {phase: "Running", conditions: [{type: "Ready", status: (if $r then "True" else "False" end)}],
             containerStatuses: [{name: "app", image: "img:1", ready: $r, restartCount: 0, state: {running: {}}}]}}]}'
}
# A chart whose image cannot be pulled: the pod waits in ImagePullBackOff.
mkpods_pull() { # NAME > file
  jq -n --arg n "$1" '{items: [{
    metadata: {name: ($n + "-abc"), labels: {}, ownerReferences: [{kind: "ReplicaSet", name: ($n + "-7d9f8")}]},
    spec: {containers: [{name: "app"}]},
    status: {phase: "Pending", conditions: [{type: "Ready", status: "False"}],
             containerStatuses: [{name: "app", image: "docker.io/x/app:1", ready: false, restartCount: 0, state: {waiting: {reason: "ImagePullBackOff"}}}]}}]}'
}
# The observe job's steps "three runs" is run for real (the workflow's own text), once per chart and world, in a
# workspace laid out like the runner's; what the job uploads (results/) becomes the artifact directory
# exp-<run id>-<chart>-<world>, as the job names it. Hand-built: the world (state files, the stub cluster).
three_runs="$(wf_step "$repo/.github/workflows/compat-exp.yml" observe "three runs")"
OBS="$T/ws-observe"
mkdir -p "$OBS"
ln -sfn "$repo/hack" "$OBS/hack"
# observe_set RUN_ID DIR CHARTS_FILE ISTIO ID...: W0, W1, W3 x runs 1 to 3 of each id, into DIR/exp-RUN_ID-ID-WORLD/.
# A chart whose id is app-2 (or its fix) is not Ready in W3, unless the id ends in -fix.
observe_set() {
  local runid="$1" dir="$2" cf="$3" istio="$4" id w c
  shift 4
  for w in W0 W1 W3; do
    c="mmc-$(tr '[:upper:]' '[:lower:]' <<<"$w")"
    : >"$T/state/$c.kubeconfig"
    printf 'world=%s\nistio=%s\nkubernetes=1.36\n' "$w" "$istio" >"$T/state/$c.world"
  done
  for id in "$@"; do
    for w in W0 W1 W3; do
      local ready=true name="${id%-fix}"
      [[ "$id" == app-2 && "$w" == W3 ]] && ready=false
      # the part fixes of app-2 (rule 3): ids ending -stuck leave W3 not Ready; all of them name app-2's workload
      [[ "$id" == app-2-*-stuck && "$w" == W3 ]] && ready=false
      [[ "$id" == app-2-p* ]] && name=app-2
      mkpods "$ready" "$name" >"$S/pods.json"
      # PULL_W0: charts whose W0 pods cannot pull their image (a registry refusal, as in the pilots)
      [[ "$w" == W0 && " ${PULL_W0:-} " == *" $id "* ]] && mkpods_pull "$name" >"$S/pods.json"
      observe_job "$runid" "$dir" "$cf" "$id" "$w" "${TEST_POD:-auto}" || return 1
    done
  done
}
# observe_job RUN_ID DIR CHARTS_FILE ID WORLD TEST_POD: one observe job on the stub cluster, pods in $S/pods.json.
# The chart app-3 has a helm test hook; in W1 its test fails and the test pod it creates carries the sidecar opt-out
# label (TEST_POD auto or optout), carries none (plain), is gone when the instrument looks (gone), or is joined by a second
# test pod without the label, app-3-test-grpc (two). In the other
# worlds the test passes and leaves no pod.
observe_job() {
  local runid="$1" dir="$2" cf="$3" id="$4" w="$5" tp="$6" extra=()
  rm -f "$S/pods-after.json" "$S/pods-after.json.left"
  if [[ "$id" == app-3 ]]; then
    extra+=(STUB_HOOKS=1)
    if [[ "$w" == W1 ]]; then
      extra+=(STUB_TEST_RC=1 STUB_PODS_AFTER="$S/pods-after.json")
      case "$tp" in auto | optout | two) lbl='{"sidecar.istio.io/inject":"false"}' ;; *) lbl='{}' ;; esac
      jq --argjson l "$lbl" --argjson gone "$([[ "$tp" == gone ]] && echo true || echo false)" --argjson two "$([[ "$tp" == two ]] && echo true || echo false)" '.items += (if $gone then [] else [{
          metadata: {name: "app-3-test", labels: $l, annotations: {"helm.sh/hook": "test", "helm.sh/hook-weight": "0"}},
          spec: {containers: [{name: "test"}]},
          status: {phase: "Failed", conditions: [{type: "Ready", status: "False"}],
                   containerStatuses: [{name: "test", image: "img:1", ready: false, restartCount: 0, state: {terminated: {exitCode: 1, reason: "Error"}}}]}}] end)
          | if $two then .items += [{metadata: {name: "app-3-test-grpc", labels: {}, annotations: {"helm.sh/hook": "test", "helm.sh/hook-weight": "1"}},
              spec: {containers: [{name: "test"}]},
              status: {phase: "Failed", conditions: [{type: "Ready", status: "False"}],
                       containerStatuses: [{name: "test", image: "img:1", ready: false, restartCount: 0, state: {terminated: {exitCode: 1, reason: "Error"}}}]}}] else . end' "$S/pods.json" >"$S/pods-after.json"
    fi
  fi
  rm -rf "$OBS/results"
  in_ws "$OBS" PATH="$T/bin:$PATH" STUB_CONTEXT="kind-mmc-$(tr '[:upper:]' '[:lower:]' <<<"$w")" STUB_PODS="$S/pods.json" ${extra[@]+"${extra[@]}"} \
    MMC_STATE="$T/state" MMC_SETTLE=0 MMC_LATE=0 CHARTS_FILE="$cf" WORLD="$w" CHART="$id" \
    bash -c "$three_runs" >/dev/null 2>"$T/observe.err" || {
    bad "observe job $w $id"; head -3 "$T/observe.err"; return 1; }
  rm -rf "$dir/exp-$runid-$id-$w"
  mkdir -p "$dir"
  mv "$OBS/results" "$dir/exp-$runid-$id-$w"
}

# The report job's steps "check records" and "difftable" are run for real in a workspace holding the downloaded
# artifacts (results/exp-*/), and the files its upload step names (report-*.json, table.md, results/) become the
# artifact compat-exp-report-<run id>. Hand-built: `go run ./hack/compat-pilot/difftable` is served by the binary
# built above (the go shim), and the step summary goes to a file.
mkdir -p "$T/shim"
cat >"$T/shim/go" <<SHIM
#!/usr/bin/env bash
[[ "\$1 \$2" == "run ./hack/compat-pilot/difftable" ]] || { echo "go shim: only the difftable run is served" >&2; exit 9; }
shift 2
exec "$B/difftable" "\$@"
SHIM
chmod +x "$T/shim/go"
rep_check="$(wf_step "$repo/.github/workflows/compat-exp.yml" report "check records")"
rep_diff="$(wf_step "$repo/.github/workflows/compat-exp.yml" report difftable)"
# report_job RUN_ID DIR [PLAN_OUT]: DIR holds the run's exp-RUN_ID-* artifact directories; the artifact tree
# is left in $T/ws-report-RUN_ID (report-RUN_ID.json, table.md, results/)
report_job() {
  local runid="$1" dir="$2" cw="" ig="" w="$T/ws-report-$1"
  if [[ -n "${3:-}" ]]; then cw="$(sed -n 's/^cell_worlds=//p' "$3")"; ig="$(sed -n 's/^ignore=//p' "$3")"; fi
  rm -rf "$w"; mkdir -p "$w/results" "$w/hack/compat-exp"
  cp -R "$dir"/exp-"$runid"-* "$w/results/"
  if [[ -d "$R/hack/compat-exp/ignore" ]]; then cp -R "$R/hack/compat-exp/ignore" "$w/hack/compat-exp/ignore"; fi
  must "workflow step: report job $runid, check records" in_ws "$w" RUN_ID="$runid" bash -c "$rep_check"
  must "workflow step: report job $runid, difftable" in_ws "$w" PATH="$T/shim:$PATH" RUN_ID="$runid" CELL_WORLDS="$cw" IGNORE_FILE="$ig" GITHUB_STEP_SUMMARY="$T/summary.md" bash -c "$rep_diff"
  cp "$w/report-$runid.json" "$T/report-$runid.json"
}

# --- 5. run 1 and run 2 -----------------------------------------------------------------------------

observe_set 1001 "$T/res-1001" "$T/plan-observe/charts.json" 1.31.3 $id1 $id2 $id3 && ok "observe job steps: 3 charts x 3 worlds x 3 runs"
eq "artifacts: 9 directories named exp-<run id>-<chart>-<world>, 27 records named <id>.<world>.run<N>.json" \
  "$(find "$T/res-1001" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ') $(find "$T/res-1001" -name "app-[123].W[013].run[123].json" | wc -l | tr -d ' ')" "9 27"
eq "ids: the record's chart field is the list's id" "$(jq -s -c 'map(.chart) | unique' "$T/res-1001"/*/*.json)" '["app-1","app-2","app-3"]'
# hand-made: the sample is the 50 primary charts (crossrun refuses a smaller one), so the other 47 get
# copies of app-1's artifact directories under their own ids; the stub cluster has no behaviour that differs per chart.
fill_copies() { # RUN_ID DIR
  local id w f
  for id in $(jq -r '.charts[3:][].id' "$T/charts-exp.json"); do
    for w in W0 W1 W3; do
      mkdir -p "$2/exp-$1-$id-$w"
      for f in "$2/exp-$1-app-1-$w"/*.json; do
        jq --arg id "$id" '.chart = $id' "$f" >"$2/exp-$1-$id-$w/$id.${f##*/app-1.}"
      done
    done
  done
}
fill_copies 1001 "$T/res-1001"
observe_set 1002 "$T/res-1002" "$T/plan-observe/charts.json" 1.31.3 $id1 $id2 $id3 && ok "observe job steps for run 2"
fill_copies 1002 "$T/res-1002"
for r in 1001 1002; do report_job $r "$T/res-$r"; done
eq "ids: the report's charts are the records' charts" "$(jq -c '[.charts[].chart] | sort == ([$l[0].charts[].id] | sort)' --slurpfile l "$T/charts-exp.json" "$T/report-1001.json")" true
eq "run 1: app-2 differs in W3 only, app-3 (a failing helm test pod) in W1 only" "$(jq -c '[.charts[] | select(.chart | IN("app-1","app-2","app-3")) | {c: .chart, w: [.worlds[] | {(.world): .verdict}] | add}] | sort_by(.c)' "$T/report-1001.json")" \
  '[{"c":"app-1","w":{"W1":"same","W3":"same"}},{"c":"app-2","w":{"W1":"same","W3":"differs"}},{"c":"app-3","w":{"W1":"differs","W3":"same"}}]'
keys="$(jq -c '[.charts[] | select(.chart == "app-2") | .worlds[] | select(.world == "W3") | .keys[] | select(.verdict == "differs") | .key]' "$T/report-1001.json")"
eq "run 1: two findings, the same not-ready workload at install and in the late snapshot" "$keys" '["late:workload:Deployment/app-2:ready","workload:Deployment/app-2:ready"]'

# --- 6. certification: guard, observe, difftable -cell-worlds ---------------------------------------------

mkdir -p "$R/hack/compat-exp/fixes" "$R/hack/compat-exp/ignore"
base="$(jq -c --arg id "$id2" '.charts[] | select(.id == $id)' "$T/charts-exp.json")"
jq -n --argjson b "$base" '[{id: "app-2-fix", fixOf: $b.id, cause: "ambient-l7", chart: $b.chart, version: $b.version, repo: $b.repo,
  values: ["--set", "waypoint.enabled=true"], cellWorlds: ["W3"], ignore: "hack/compat-exp/ignore/app-2.json"}]' >"$R/hack/compat-exp/fixes/app-2.json"
echo '[]' >"$R/hack/compat-exp/ignore/app-2.json"
git -C "$R" add hack/compat-exp/fixes hack/compat-exp/ignore && git -C "$R" commit -q -m "fix entry"
must_guard "guard plan certify" certify GUARD_FIXES_FILE=hack/compat-exp/fixes/app-2.json GUARD_FIX_ID=app-2-fix
eq "certify plan: cell worlds and ignore come from the entry" "$(grep -E '^(cell_worlds|ignore)=' "$T/plan-certify.out" | tr '\n' ' ')" "cell_worlds=W3 ignore=hack/compat-exp/ignore/app-2.json "
observe_set 1003 "$T/res-1003" "$T/plan-certify/charts.json" 1.31.3 app-2 app-2-fix && ok "observe job steps for the base chart and its fix"
eq "ids: the fix's record carries fixOf = the base id" "$(jq -r '.fixOf' "$T/res-1003/exp-1003-app-2-fix-W3/app-2-fix.W3.run1.json")" "app-2"
report_job 1003 "$T/res-1003" "$T/plan-certify.out"
eq "certificate: earned, W3 cleared" "$(jq -c '.charts[] | select(.fixOf == "app-2") | .certificate | {certified, cleared}' "$T/report-1003.json")" '{"certified":true,"cleared":["W3"]}'
observe_set 1004 "$T/res-1004" "$T/plan-certify/charts.json" 1.31.3 app-2 app-2-fix && ok "observe job steps for the second certification run"
report_job 1004 "$T/res-1004" "$T/plan-certify.out"

# --- 7. recheck on the second version -----------------------------------------------------------------------

observe_set 1005 "$T/res-1005" "$T/plan-observe/charts.json" 1.32.0 $id2 && ok "observe job steps for the recheck"
report_job 1005 "$T/res-1005"

# --- 8. the ledger: one row per differing key, written by a person ---------------------------------------------------------------

pub2="$(jq -r --arg id "$id2" '.charts[] | select(.id == $id) | .publisher' "$T/charts-exp.json")"
jq -n --arg c "$id2" --argjson ks "$keys" --arg p "$pub2" '{schema: 1, newKeys: [], rows: [$ks[] as $k | {
  chart: $c, world: "W3", key: $k, cause: "ambient-l7", type: "defect",
  typeBasis: {doc: "the chart documents no waypoint switch"},
  fix: {entry: "app-2-fix", form: "values", helmLint: true},
  runs: {run1: "1001", run2: "1002", certification: ["1003", "1004"]},
  recheck: {version: "1.32.0", result: "reproduces-3of3", run: "1005"},
  upstream: {merged: false}, reviewer: {verdict: "agree", run: "9001"},
  status: "counted", publisher: $p}]}' >"$T/ledger.json"
# app-3: its helm test fails in W1; the failing caller is the chart's own test pod, which carries the sidecar opt-out
# label (a judgment a person writes; crossrun checks it against testPodMesh in the records observe.sh wrote)
pub3="$(jq -r --arg id "$id3" '.charts[] | select(.id == $id) | .publisher' "$T/charts-exp.json")"
jq --arg p "$pub3" '.rows += [{chart: "app-3", world: "W1", key: "helm:test", status: "excluded", publisher: $p,
  exclusion: {reason: "c-optout", evidence: "the chart documents the test pod and sets sidecar.istio.io/inject=false on it", callers: ["app-3-test"]},
  reviewer: {verdict: "agree", run: "9001"}}]' "$T/ledger.json" >"$T/x" && mv "$T/x" "$T/ledger.json"
"$B/ledger" check -ledger "$T/ledger.json" -causes "$repo/hack/compat-exp/causes.json" >"$T/lc.out" 2>&1 && ok "ledger check on the real causes.json" || { bad "ledger check"; cat "$T/lc.out"; }

# --- 9. analyze.sh with a gh stub: freeze, runs, qualification, download, ledger check, static.sh, crossrun ----------

# the ledger and the data files are committed, as analyze.sh demands
cp "$T/ledger.json" "$R/hack/compat-exp/ledger.json"
echo '{"schema":1,"rows":[],"newKeys":[]}' >"$R/hack/compat-exp/ledger-run1.json"   # for the check after run 1, before any ledger row
git -C "$R" add hack/compat-exp/ledger.json hack/compat-exp/ledger-run1.json && git -C "$R" commit -q -m "ledger rows"
tagsha="$(git -C "$R" rev-parse compat-exp-freeze)"
headsha="$(git -C "$R" rev-parse HEAD)"

G="$T/gh"
mkdir -p "$G"
git clone -q "$R" "$T/gen" || { echo "clone failed"; exit 1; }
# artifact of a workflow run: the report, its records, and the run metadata the workflow uploads
mkartifact() { # ID MODE ISTIO: what the report job's upload step names, from the workspace the job ran in
  local r="$1" mode="$2" istio="$3" w="$T/ws-report-$1"
  mkdir -p "$G/$r"
  cp "$w"/report-*.json "$w/table.md" "$G/$r/"
  cp -R "$w/results" "$G/$r/results"
  echo "compat-exp-report-$r" >"$G/$r.artifact"
  # the guard job's run-metadata-<id>-<attempt> artifact: the workflow's own step, run on a clone of the instrument
  gen_run_metadata "$R/.github/workflows/compat-exp.yml" "$T/gen" "$G/$r.meta-1/run-metadata.json" "$mode" "$istio" "$headsha" "$r" 1 || bad "run metadata step for $r"
}
mkartifact 1001 observe 1.31.3
mkartifact 1002 observe 1.31.3
mkartifact 1003 certify 1.31.3
mkartifact 1004 certify 1.31.3
mkartifact 1005 recheck 1.32.0
# the qualification run: what the select job's upload step names (out/ and qual/ of its workspace)
mkdir -p "$G/7001"
cp -R "$SEL/out" "$SEL/qual" "$G/7001/"
echo "charts-exp-7001" >"$G/7001.artifact"
gen_run_metadata "$R/.github/workflows/compat-exp.yml" "$T/gen" "$G/7001.meta-1/run-metadata.json" qualify 1.31.3 "$headsha" 7001 1 || bad "run metadata step for 7001"

# gh run view JSON: latest attempt's view, and the jobs of every attempt (the times come from the jobs)
mkview() { # ID ATTEMPTS FIRST_JOB_START FIRST_JOB_END LAST_JOB_START LAST_JOB_END
  local id="$1" n="$2"
  jq -n --arg id "$id" --arg sha "$headsha" --argjson n "$n" --arg s1 "$3" --arg e1 "$4" --arg s2 "$5" --arg e2 "$6" '
    def view($jobs): {attempt: $n, conclusion: "success", databaseId: ($id | tonumber), event: "workflow_dispatch", headSha: $sha,
      startedAt: $jobs[0].startedAt, status: "completed", updatedAt: ($jobs[-1].completedAt), workflowName: "compat-exp", jobs: $jobs};
    [{name: "observe", startedAt: $s1, completedAt: $e1}] as $a1
    | [{name: "observe", startedAt: $s2, completedAt: $e2}] as $a2
    | (if $n == 1 then view($a1) else view($a2) end),
      view($a1)' -c >"$T/views.$id"
  sed -n 1p "$T/views.$id" >"$G/$id.view.json"
  sed -n 2p "$T/views.$id" >"$G/$id.attempt-1.json"
  [[ "$n" -lt 2 ]] || sed -n 1p "$T/views.$id" >"$G/$id.attempt-2.json"
}
# run 1: first attempt 08:00-09:10, a rerun 09:30-10:20 (the run ends at 10:20, not 09:10)
mkview 1001 2 2026-11-20T08:00:00Z 2026-11-20T09:10:00Z 2026-11-20T09:30:00Z 2026-11-20T10:20:00Z
# run 2: starts 22:30 (12 h 10 min after 10:20), rerun later
mkview 1002 2 2026-11-20T22:30:00Z 2026-11-20T23:20:00Z 2026-11-21T00:10:00Z 2026-11-21T01:00:00Z
for r in 1003 1004 1005; do mkview "$r" 1 2026-11-21T08:00:00Z 2026-11-21T08:30:00Z 2026-11-21T08:00:00Z 2026-11-21T08:30:00Z; done
mkview 7001 1 2026-11-01T08:00:00Z 2026-11-01T09:00:00Z 2026-11-01T08:00:00Z 2026-11-01T09:00:00Z

# The REST run objects (gh api repos/OWNER/REPO/actions/runs/ID): the workflow file, its id and the run-name GitHub
# evaluated from the workflow at the run's commit, built from the metadata the workflow's own step wrote.
for r in 1001 1002 1003 1004 1005 7001; do
  jq -n --arg id "$r" --slurpfile m "$G/$r.meta-1/run-metadata.json" '$m[0] as $m | {id: ($id | tonumber), path: ".github/workflows/compat-exp.yml", workflow_id: 4242,
    head_sha: $m.headSha, event: "workflow_dispatch", status: "completed", conclusion: "success", created_at: "2030-01-01T00:00:00Z",
    display_title: "compat-exp mode=\($m.mode) istio=\($m.istio) charts=\($m.chartsFile) fixes=\($m.fixesFile) ids=\($m.chartIds) fix=\($m.fixId)"}' >"$G/$r.rest.json"
done
echo '{"id":4242}' >"$G/workflow.json"
jq -n --slurpfile q "$G/7001.rest.json" '{total_count: 1, workflow_runs: $q}' >"$G/qruns.json"

cat >"$T/bin/gh" <<'STUB'
#!/usr/bin/env bash
echo "gh $*" >>"$GH_LOG"
if [[ "$1" == api ]]; then
  jqf=""; apath=""
  shift
  while [[ $# -gt 0 ]]; do case "$1" in --paginate) shift ;; --jq) jqf="$2"; shift 2 ;; *) apath="$1"; shift ;; esac; done
  case "$apath" in
    repos/*/actions/runs/[0-9]*) f="$GH_FIX/${apath##*/}.rest.json" ;;
    repos/*/actions/workflows/compat-exp.yml/runs\?*) f="$GH_FIX/qruns.json" ;;
    repos/*/actions/workflows/compat-exp.yml) f="$GH_FIX/workflow.json" ;;
    *) echo "gh stub: unknown api path $apath" >&2; exit 9 ;;
  esac
  [[ -f "$f" ]] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
  if [[ -n "$jqf" ]]; then jq -r "$jqf" "$f"; else cat "$f"; fi
  exit 0
fi
[[ "$1 $2" == "run download" || "$1 $2" == "run view" ]] || { echo "gh stub: only run download, run view and api reads are allowed" >&2; exit 9; }
id="$3"
if [[ "$2" == download ]]; then
  while [[ $# -gt 0 ]]; do case "$1" in -D) dir="$2"; shift 2 ;; -n) name="$2"; shift 2 ;; *) shift ;; esac; done
  if [[ "$name" =~ ^run-metadata-$id-([0-9]+)$ ]]; then
    [[ -d "$GH_FIX/$id.meta-${BASH_REMATCH[1]}" ]] || { echo "no artifact $name" >&2; exit 1; }
    cp -R "$GH_FIX/$id.meta-${BASH_REMATCH[1]}/." "$dir/"
    exit 0
  fi
  [[ -d "$GH_FIX/$id" && "$name" == "$(cat "$GH_FIX/$id.artifact")" ]] || { echo "no artifact $name" >&2; exit 1; }
  cp -R "$GH_FIX/$id/." "$dir/"
else
  att=""
  while [[ $# -gt 0 ]]; do case "$1" in --attempt) att="$2"; shift 2 ;; *) shift ;; esac; done
  if [[ -n "$att" && -f "$GH_FIX/$id.attempt-$att.json" ]]; then cat "$GH_FIX/$id.attempt-$att.json"; else cat "$GH_FIX/$id.view.json"; fi
fi
STUB
chmod +x "$T/bin/gh"
# what helm template returns: app-2 has L7 config without a waypoint, the others are clean
cp "$repo/hack/compat-exp/staticcheck/testdata/protocol-consistent.yaml" "$S/render/default.yaml"
cp "$repo/hack/compat-exp/staticcheck/testdata/l7-routes.yaml" "$S/render/app-2.yaml"

export GH_LOG="$T/gh.log" GH_FIX="$G"
analyze() { # OUTDIR [extra args]: the real thing, tools built from the checked tree, run from the throwaway repository
  local o="$1"; shift
  : >"$GH_LOG"
  env PATH="$T/bin:$PATH" MMC_STATE="$T/state" "$R/hack/compat-exp/analyze.sh" --out "$o" "$@" >"$T/analyze.out" 2>"$T/analyze.err"
}
analyze "$T/analysis" --run1 1001 --run2 1002 --cert 1003 --cert 1004 --recheck 1005
arc=$?
eq "analyze.sh exits 0 (tools built from the checked tree, no overrides)" "$arc" 0
[[ $arc -eq 0 ]] || head -10 "$T/analyze.err"
eq "gh was used for run download, run view and read-only api calls (GET paths under actions/) only" "$(grep -cv '^gh \(run \(download\|view\)\|api .*repos/[^ ]*/actions/\)' "$GH_LOG")" 0
res="$T/analysis/result.json"
eq "manifest: run 1 ends with its rerun (latest completion of any job in any attempt)" "$(jq -c '.runs[0] | {id, started, ended}' "$T/analysis/inputs.json")" \
  '{"id":"1001","started":"2026-11-20T08:00:00Z","ended":"2026-11-20T10:20:00Z"}'
eq "manifest: run 2 starts with its earliest job in any attempt" "$(jq -c '.runs[1] | {id, started}' "$T/analysis/inputs.json")" '{"id":"1002","started":"2026-11-20T22:30:00Z"}'
eq "manifest: recheck istio is the run's metadata, not a guess" "$(jq -c '.recheck | {id, istio}' "$T/analysis/inputs.json")" '{"id":"1005","istio":"1.32.0"}'
eq "static.json: rows for the 50 charts x 2 worlds x the checks" "$(jq length "$T/analysis/static.json")" "$((50 * 2 * 8))"
eq "static: only app-2 is flagged by the L7 check, in W3" "$(jq -c '[.[] | select(.flagged)] | map([.chart, .world, .check])' "$T/analysis/static.json")" '[["app-2","W3","l7-without-waypoint"]]'
eq "static: the L7 check is not applicable to W1 (not 'not flagged')" "$(jq -c '.[] | select(.chart == "app-2" and .world == "W1" and .check == "l7-without-waypoint") | {applicable, flagged}' "$T/analysis/static.json")" '{"applicable":false,"flagged":false}'
eq "crossrun: two cells of one chart, both counted and credited to ambient-l7" "$(jq -c '[.cells[] | select(.status == "counted") | {chart, world, credit}]' "$res")" '[{"chart":"app-2","world":"W3","credit":"ambient-l7"},{"chart":"app-2","world":"W3","credit":"ambient-l7"}]'
eq "crossrun: metrics" "$(jq -c '.metrics' "$res")" '{"nNew":1,"pNew":1,"d":1,"pD":1,"s":1}'
eq "crossrun: S was computed from static.json" "$(jq -c '[.sComputed, (.staticChecks[0] | {key, check, flagsEveryCountedFinding, qualifies})]' "$res")" '[true,{"key":"ambient-l7","check":"l7-without-waypoint","flagsEveryCountedFinding":true,"qualifies":true}]'
eq "crossrun: region E (N_new 1)" "$(jq -r '.region.region' "$res")" E
eq "crossrun: the result is final, the gap is 12 h 10 min" "$(jq -c '[.final, .gapOk, (.gapHours * 6 | round)]' "$res")" '[true,true,73]'
eq "crossrun: the sample is the 50 primary charts, nothing replaced" "$(jq -c '[(.sample.charts | length), (.sample.replacements | length)]' "$res")" '[50,0]'
eq "ids: every cell's chart is a chart of the frozen list" "$(jq -r --slurpfile l "$T/charts-exp.json" '[.cells[].chart] | unique - ([$l[0].charts[].id, $l[0].reserves[].id]) | length' "$res")" 0
eq "crossrun: no warnings" "$(jq -c '.warnings' "$res")" '[]'
eq "crossrun: undecided share under both readings" "$(jq -c '.undecidedShare | [.cellsReading.undecided, .chartWorldsReading.undecided, .drivesTheStop]' "$res")" '[0,0,"cells"]'
must "result.md written" test -s "$T/analysis/result.md"
# provenance: the freeze, HEAD, every run, the files read
eq "provenance: freeze commit and HEAD" "$(jq -c '.provenance | [.freezeCommit, .head]' "$res")" "[\"$tagsha\",\"$headsha\"]"
eq "provenance: every run id with headSha, mode, Istio and times" "$(jq -c '[.provenance.runs[] | {role, id, mode, istio, ok: (.headSha | length == 40)}]' "$res")" \
  '[{"role":"qualification","id":"7001","mode":"qualify","istio":"1.31.3","ok":true},{"role":"run1","id":"1001","mode":"observe","istio":"1.31.3","ok":true},{"role":"run2","id":"1002","mode":"observe","istio":"1.31.3","ok":true},{"role":"certification","id":"1003","mode":"certify","istio":"1.31.3","ok":true},{"role":"certification","id":"1004","mode":"certify","istio":"1.31.3","ok":true},{"role":"recheck","id":"1005","mode":"recheck","istio":"1.32.0","ok":true}]'
eq "provenance: run 1 times" "$(jq -c '.provenance.runs[1] | {started, ended, attempts}' "$res")" '{"started":"2026-11-20T08:00:00Z","ended":"2026-11-20T10:20:00Z","attempts":2}'
eq "provenance: sha256 of the chart list, ledger and causes that were read" "$(jq -c '.provenance.files | [.chartList.sha256, .ledger.sha256, .causes.sha256]' "$res")" \
  "[\"$(shasum -a 256 "$R/hack/compat-exp/charts-exp.json" | cut -d' ' -f1)\",\"$(shasum -a 256 "$R/hack/compat-exp/ledger.json" | cut -d' ' -f1)\",\"$(shasum -a 256 "$R/hack/compat-exp/causes.json" | cut -d' ' -f1)\"]"
eq "provenance: a valid run carries no testMode" "$(jq -c '[has("testMode"), (.provenance | has("testMode"))]' "$res")" '[false,false]'
[[ "$(head -c 20 "$T/analysis/result.md")" != "**THIS RESULT IS NOT"* ]] && ok "result.md does not say it is invalid" || bad "result.md says it is invalid"

# seam: the helm test pod is in the record observe.sh wrote (testPodMesh), and crossrun's c-optout check reads it there
rec3="$T/res-1001/exp-1001-app-3-W1/app-3.W1.run1.json"
eq "record: podMesh is the snapshot before helm test (no test pod), testPodMesh holds the test pod" \
  "$(jq -c '[([.podMesh[].pod] | index("app-3-test")), (.testPodMesh | map(.pod)), .testPodMeshStatus, .obs["helm:test"]]' "$rec3")" '[null,["app-3-test"],"listed","fail"]'
eq "record: the test pod carries the opt-out label and its Helm hook annotations" \
  "$(jq -c '.testPodMesh[0] | [.meshMetadata, .testHook, .helmHooks]' "$rec3")" '[[{"kind":"label","key":"sidecar.istio.io/inject","value":"false"}],true,{"helm.sh/hook":"test","helm.sh/hook-weight":"0"}]'
eq "crossrun: app-3's helm:test cell in W1 is excluded as c-optout" "$(jq -c '[.cells[] | select(.chart == "app-3") | {world, key, status, reason: .exclusionReasons[0]}]' "$res")" \
  '[{"world":"W1","key":"helm:test","status":"excluded","reason":"c-optout"}]'
eq "crossrun: the opt-out evidence names the test pod" "$(jq -c '[.optOutEvidence[]? | select(.chart == "app-3") | .pods[] | select(.testPod) | {pod, effectiveOptOut}] | unique' "$res")" '[{"pod":"app-3-test","effectiveOptOut":true}]'

# the same chart without the opt-out label on its test pod, with the test pod gone, and with a second test pod left in the
# mesh (the row names only the opted-out one): the exclusion is refused.
# Records are a real observe job of app-3 in W1 (3 runs per sample run) in place of the artifact's own, the rest as downloaded.
for variant in plain gone two; do
  for r in 1001 1002; do
    rm -rf "$T/v-$variant-$r"; cp -R "$T/analysis/run-$r/results" "$T/v-$variant-$r"
    mkpods true app-3 >"$S/pods.json"
    observe_job "$r" "$T/v-$variant-$r" "$T/plan-observe/charts.json" app-3 W1 "$variant"
  done
  jq --arg a "$T/v-$variant-1001" --arg b "$T/v-$variant-1002" '.runs[0].records = $a | .runs[1].records = $b' "$T/analysis/inputs.json" >"$T/inputs-$variant.json"
  "$B/crossrun" -inputs "$T/inputs-$variant.json" -json "$T/r-$variant.json" -md "$T/r-$variant.md" >"$T/cr-$variant.out" 2>&1
  eq "crossrun, test pod $variant: the c-optout exclusion is refused (exit 1)" "$?" 1
done
grep -q "does not carry the opt-out" "$T/cr-plain.out" && ok "  plain: the error says the pod does not carry the opt-out" || bad "  plain: error text: $(head -3 "$T/cr-plain.out")"
grep -q 'testPodMeshStatus is "no-new-pods"' "$T/cr-gone.out" && ok "  gone: the error says the test pod could not be recorded" || bad "  gone: error text: $(head -3 "$T/cr-gone.out")"
grep -q "app-3-test-grpc does not carry the opt-out" "$T/cr-two.out" && ok "  two: the error names the second test pod, which the ledger row does not name and which is in the mesh" || bad "  two: error text: $(head -3 "$T/cr-two.out")"
eq "  two: the record holds both test pods" "$(jq -c '[.testPodMesh[].pod]' "$T/v-two-1001/exp-1001-app-3-W1/app-3.W1.run1.json")" '["app-3-test","app-3-test-grpc"]'
eq "  gone: the record says helm test ran and its pods could not be listed" "$(jq -c '[.testPodMesh, .testPodMeshStatus, .obs["helm:test"]]' "$T/v-gone-1001/exp-1001-app-3-W1/app-3.W1.run1.json")" '[[],"no-new-pods","fail"]'

# rule 3 with a combined fix (docs/compat-experiment.md section 2): app-2 needs two keys' fixes; the combined entry is
# app-2-fix (certified in runs 1003 and 1004), and each part is run alone in two certification runs of its own, with
# the real observe job steps and difftable. Part pa alone returns W3 to W0's value, parts pb and pc leave it differing.
# The parts are not certified (a part that leaves the cell cannot be); credit follows the parts' results.
jq '. + [(.[] | select(.id == "app-2-fix") | .id = "app-2-pa"), (.[] | select(.id == "app-2-fix") | .id = "app-2-pb-stuck"),
  (.[] | select(.id == "app-2-fix") | .id = "app-2-pc-stuck")]' "$T/plan-certify/charts.json" >"$T/charts-parts.json"
printf 'cell_worlds=W3\nignore=\n' >"$T/plan-parts.out"
part_run() { # RUN_ID ENTRY
  observe_set "$1" "$T/res-$1" "$T/charts-parts.json" 1.31.3 app-2 "$2" && report_job "$1" "$T/res-$1" "$T/plan-parts.out"
}
part_run 1006 app-2-pa; part_run 1007 app-2-pa
part_run 1008 app-2-pb-stuck; part_run 1009 app-2-pb-stuck
part_run 1010 app-2-pc-stuck; part_run 1011 app-2-pc-stuck
eq "rule 3: difftable certifies the part that clears W3 and refuses the ones that leave it differing" \
  "$(for r in 1006 1008 1010; do jq -r '.charts[] | select(.fixOf == "app-2") | .certificate.certified' "$T/report-$r.json"; done | tr '\n' ' ')" "true false false "
parts_inputs() { # OUT PA-ENTRY PA-RUNS PA-CLEARS PB-ENTRY PB-RUNS CREDIT
  local out="$1" pa="$2" par="$3" pac="$4" pb="$5" pbr="$6" cause="$7"
  jq --argjson pa "$par" --argjson pb "$pbr" --argjson pac "$pac" --arg pae "$pa" --arg pbe "$pb" --arg cause "$cause" '
    .rows |= map(if .chart == "app-2" then
      .cause = $cause | .fix.entry = "app-2-fix" | .combined = {entry: "app-2-fix", certification: ["1003", "1004"]}
      | .parts = [{cause: "ambient-l7", entry: $pae, runs: $pa, clearsAlone: $pac}, {cause: "protocol", entry: $pbe, runs: $pb, clearsAlone: false}]
    else . end)' "$T/ledger.json" >"$out.ledger.json"
  "$B/ledger" check -ledger "$out.ledger.json" -causes "$repo/hack/compat-exp/causes.json" >"$out.lc" 2>&1 || { bad "ledger check of the parts ledger"; head -3 "$out.lc"; }
  local certs
  certs="$(for r in "${@:8}"; do jq -n --arg id "$r" --arg d "$T" '{id: $id, report: ($d + "/report-" + $id + ".json"), istio: "1.31.3"}'; done | jq -s -c .)"
  jq --arg l "$out.ledger.json" --argjson certs "$certs" '.ledger = $l | .certification += $certs' "$T/analysis/inputs.json" >"$out.inputs.json"
  "$B/crossrun" -inputs "$out.inputs.json" -json "$out.json" -md "$out.md" >"$out.err" 2>&1
}
parts_inputs "$T/parts-a" app-2-pa '["1006","1007"]' true app-2-pb-stuck '["1008","1009"]' ambient-l7 1006 1007 1008 1009
eq "rule 3: the part that alone clears W3 gets the cells (ambient-l7, though protocol ranks first), certified by the combined entry" "$?:$(jq -c '[.cells[] | select(.chart == "app-2" and .status == "counted") | .credit]' "$T/parts-a.json")" '0:["ambient-l7","ambient-l7"]'
parts_inputs "$T/parts-b" app-2-pa '["1006","1007"]' true app-2-pb-stuck '["1008","1009"]' protocol 1006 1007 1008 1009
eq "rule 3: a ledger that credits the first key although another part alone clears the cell is refused" "$?" 1
grep -q 'rule 3 credits the cell to "ambient-l7"' "$T/parts-b.err" && ok "  and says rule 3 credits ambient-l7" || bad "  error text: $(head -3 "$T/parts-b.err")"
parts_inputs "$T/parts-c" app-2-pc-stuck '["1010","1011"]' false app-2-pb-stuck '["1008","1009"]' protocol 1008 1009 1010 1011
eq "rule 3: neither part alone clears the cells: the first key in the list, protocol, with the combined certificate" "$?:$(jq -c '[.cells[] | select(.chart == "app-2" and .status == "counted") | .credit]' "$T/parts-c.json")" '0:["protocol","protocol"]'

# The replacement rule (crossrun/INTERPRETATIONS.md 22). Variant runs 1101 and 1102 are runs 1001 and 1002 with the
# reserve app-51 added, and in run 1102 the W0 of app-6 and app-7 cannot pull its image (real observe jobs, the registry
# refusal of the pilots). Each is rerun once in a separate observe run restricted to the chart (1012, 1013): app-6's W0
# is fine on the rerun, app-7's fails again. So app-6 is NOT replaced (its rerun records stand in), app-7 is replaced by
# app-51, the next reserve, and nothing else is a choice.
variant() { # OLD NEW: res-OLD with the directories renamed exp-NEW-*, and the reserve app-51 copied from app-1
  local old="$1" new="$2" d w f base
  rm -rf "$T/res-$new"; mkdir -p "$T/res-$new"
  for d in "$T/res-$old/exp-$old"-*; do base="${d##*/}"; base="${base#"exp-$old-"}"; cp -R "$d" "$T/res-$new/exp-$new-$base"; done
  for w in W0 W1 W3; do
    mkdir -p "$T/res-$new/exp-$new-app-51-$w"
    for f in "$T/res-$new/exp-$new-app-1-$w"/*.json; do
      jq '.chart = "app-51"' "$f" >"$T/res-$new/exp-$new-app-51-$w/app-51.${f##*/app-1.}"
    done
  done
}
variant 1001 1101; variant 1002 1102
PULL_W0="app-6 app-7" observe_set 1102 "$T/res-1102-pull" "$T/plan-observe/charts.json" 1.31.3 app-6 app-7 && ok "observe job steps: W0 of app-6 and app-7 cannot pull in run 1102"
for c in app-6 app-7; do for w in W0 W1 W3; do rm -rf "$T/res-1102/exp-1102-$c-$w"; cp -R "$T/res-1102-pull/exp-1102-$c-$w" "$T/res-1102/"; done; done
observe_set 1012 "$T/res-1012" "$T/plan-observe/charts.json" 1.31.3 app-6 && ok "observe job steps: the rerun of app-6, restricted to the chart"
PULL_W0="app-7" observe_set 1013 "$T/res-1013" "$T/plan-observe/charts.json" 1.31.3 app-7 && ok "observe job steps: the rerun of app-7, its W0 fails again"
for r in 1101 1102 1012 1013; do report_job $r "$T/res-$r"; done
eq "replacement rule: the pull failure leaves W0 of app-6 and app-7 with no usable run in run 1102, installs 'ok' in the stub" \
  "$(jq -c '[.charts[] | select(.chart | IN("app-6","app-7")) | [.chart, (.worlds[0].oracleRunsUsed | length), (.worlds[0].oracleRunsLeftOut | length)]]' "$T/report-1102.json")" '[["app-6",0,3],["app-7",0,3]]'
eq "replacement rule: the rerun reports hold one chart each; app-6's W0 is 3 of 3 there, app-7's is not" \
  "$(for r in 1012 1013; do jq -c '[(.charts | length), .charts[0].chart, (.charts[0].worlds[0].oracleRunsUsed | length)]' "$T/report-$r.json"; done | tr '\n' ' ')" '[1,"app-6",3] [1,"app-7",0] '
repl_inputs() { # NAME REPLACEMENTS-JSON
  echo "$2" >"$T/$1.repl.json"
  jq --arg r1 "$T/res-1101" --arg r2 "$T/res-1102" --arg p1 "$T/report-1101.json" --arg p2 "$T/report-1102.json" \
     --arg a "$T/report-1012.json" --arg b "$T/report-1013.json" --arg ra "$T/res-1012" --arg rb "$T/res-1013" --arg repl "$T/$1.repl.json" '
    .runs[0] += {id: "1101", report: $p1, records: $r1} | .runs[1] += {id: "1102", report: $p2, records: $r2}
    | .reruns = [{id: "1012", report: $a, records: $ra, started: "2026-11-21T03:00:00Z", ended: "2026-11-21T03:40:00Z"},
                 {id: "1013", report: $b, records: $rb, started: "2026-11-21T03:00:00Z", ended: "2026-11-21T03:40:00Z"}]
    | .replacements = $repl' "$T/analysis/inputs.json" >"$T/$1.inputs.json"
  # the ledger names the variant runs
  jq '.rows |= map(if .runs then .runs.run1 = "1101" | .runs.run2 = "1102" else . end)' "$T/ledger.json" >"$T/$1.ledger.json"
  jq --arg l "$T/$1.ledger.json" '.ledger = $l' "$T/$1.inputs.json" >"$T/x" && mv "$T/x" "$T/$1.inputs.json"
  jq --arg f "$T/charts-exp.json" '.frame = $f' "$T/$1.inputs.json" >"$T/x" && mv "$T/x" "$T/$1.inputs.json"
  # static results for the reserve (the chart app-51 is as same as app-1)
  jq '. + [.[] | select(.chart == "app-1") | .chart = "app-51"]' "$T/analysis/static.json" >"$T/$1.static.json"
  jq --arg st "$T/$1.static.json" '.static = $st' "$T/$1.inputs.json" >"$T/x" && mv "$T/x" "$T/$1.inputs.json"
  "$B/crossrun" -inputs "$T/$1.inputs.json" -json "$T/$1.json" -md "$T/$1.md" >"$T/$1.out" 2>&1
}
GOOD='{"schema":1,"reruns":[{"chart":"app-6","run":"1102","rerun":"1012"}],"replacements":[{"chart":"app-7","reserve":"app-51","reason":"W0 image pull refused in run 1102 and in its rerun","run":"1102","rerun":"1013"}]}'
repl_inputs repl-good "$GOOD" && rcg=0 || rcg=$?
[[ -s "$T/repl-good.out" ]] && head -5 "$T/repl-good.out" >&2
eq "replacement rule: app-7 is replaced by app-51 and app-6 is not (exit 0)" "$rcg:$(jq -c '[(.sample.replacements | map([.chart, .reserve])), (.sample.reruns | sort_by(.chart) | map([.chart, .rerun, .failedAgain]))]' "$T/repl-good.json")" \
  '0:[[["app-7","app-51"]],[["app-6","1012",false],["app-7","1013",true]]]'
eq "replacement rule: the sample holds app-51 and app-6, not app-7" "$(jq -c '[.sample.charts | (index("app-51") != null), (index("app-6") != null), (index("app-7") != null)]' "$T/repl-good.json")" '[true,true,false]'
repl_inputs repl-choice '{"schema":1,"reruns":[{"chart":"app-7","run":"1102","rerun":"1013"}],"replacements":[{"chart":"app-6","reserve":"app-51","reason":"chosen","run":"1102","rerun":"1012"}]}'
eq "replacement rule: replacing app-6 (W0 fine on its rerun) instead of app-7 is refused (exit 1)" "$?" 1
grep -q "the rule does not replace it" "$T/repl-choice.out" && ok "  and says the rule does not replace app-6" || bad "  error text: $(head -3 "$T/repl-choice.out")"
repl_inputs repl-none '{"schema":1,"reruns":[{"chart":"app-6","run":"1102","rerun":"1012"},{"chart":"app-7","run":"1102","rerun":"1013"}],"replacements":[]}'
eq "replacement rule: app-7 failing again but not replaced is refused (exit 1)" "$?" 1
grep -q "no such replacement is in the replacements file" "$T/repl-none.out" && ok "  and says the replacement is missing" || bad "  error text: $(head -3 "$T/repl-none.out")"
repl_inputs repl-norerun '{"schema":1,"reruns":[],"replacements":[{"chart":"app-7","reserve":"app-51","reason":"x","run":"1102","rerun":"1013"}]}'
eq "replacement rule: app-6 without a named rerun is refused (exit 1)" "$?" 1
grep -q 'chart "app-6": W0 was not 3 of 3 in run 1102' "$T/repl-norerun.out" && ok "  and names the chart and the run" || bad "  error text: $(head -3 "$T/repl-norerun.out")"

# test mode: the overrides work, and every output says so
env PATH="$T/bin:$PATH" MMC_STATE="$T/state" MMC_ANALYZE_TEST=1 CROSSRUN="$B/crossrun" LEDGER="$B/ledger" STATICCHECK="$B/staticcheck" \
  "$R/hack/compat-exp/analyze.sh" --out "$T/analysis-t2" --run1 1001 --run2 1002 --cert 1003 --cert 1004 --recheck 1005 >"$T/analyze2.out" 2>"$T/analyze2.err"
eq "test mode with the tools given: exit 0" "$?" 0
eq "test mode: result.json, inputs.json, provenance.json and the static meta say testMode" "$(for f in result.json inputs.json provenance.json static.json.meta.json; do jq -r '.testMode' "$T/analysis-t2/$f"; done | tr '\n' ' ')" "true true true true "
eq "test mode: the markdown starts with the not-valid line" "$(head -c 26 "$T/analysis-t2/result.md")" "**THIS RESULT IS NOT VALID"
[[ -f "$T/analysis-t2/NOT-VALID-TEST-MODE.txt" ]] && ok "test mode: a marker file is written" || bad "test mode: a marker file is written"
env PATH="$T/bin:$PATH" CROSSRUN="$B/crossrun" "$R/hack/compat-exp/analyze.sh" --out "$T/analysis-t3" --run1 1001 >/dev/null 2>&1
eq "a CROSSRUN override outside test mode is refused (exit 2)" "$?" 2

# run 1 only: the futility check after run 1
analyze "$T/analysis-r1" --run1 1001 --ledger "$R/hack/compat-exp/ledger-run1.json"
r1rc=$?
eq "analyze.sh with run 1 only: exit 0" "$r1rc" 0
[[ $r1rc -eq 0 ]] || tail -3 "$T/analyze.err"
eq "run 1 only: not final, no region, the output says so" "$(jq -c '[.final, .region.region]' "$T/analysis-r1/result.json")" '[false,"none"]'
grep -q "RUN 1 ONLY" "$T/analysis-r1/result.md" && ok "run 1 only: the markdown says run 1 only" || bad "run 1 only: the markdown says run 1 only"
eq "run 1 only: app-2's two findings and app-3's failing helm test are pending toward N_max and P_max" "$(jq -c '.futility | [.nMax, .pMax, .cellsNeitherCreditedNorExcluded]' "$T/analysis-r1/result.json")" '[3,2,3]'
grep -q "run 1 only" "$T/analyze.out" && ok "run 1 only: analyze.sh says so" || bad "run 1 only: analyze.sh says so"

# a run 2 that started too early is an error, not a Drop: its first attempt started 11 h 30 min after run 1 ended,
# although the rerun that the latest-attempt view shows started much later
mkview 1002 2 2026-11-20T21:50:00Z 2026-11-20T23:20:00Z 2026-11-21T00:10:00Z 2026-11-21T01:00:00Z
analyze "$T/analysis-early" --run1 1001 --run2 1002 --cert 1003 --cert 1004 --recheck 1005
eq "a run 2 whose first attempt started under 12 h after run 1 ended: analyze.sh fails" "$?" 1
grep -q "12 h" "$T/analyze.err" && ok "  and the error names the gap" || bad "  and the error names the gap: $(tail -2 "$T/analyze.err")"
grep -q "2026-11-20T21:50:00Z" "$T/analyze.err" && grep -q "2026-11-20T10:20:00Z" "$T/analyze.err" && ok "  and both times" || bad "  and both times"
[[ ! -e "$T/analysis-early/result.json" ]] && ok "  and no result file (no region, no futility verdict)" || bad "  and no result file"
mkview 1002 2 2026-11-20T22:30:00Z 2026-11-20T23:20:00Z 2026-11-21T00:10:00Z 2026-11-21T01:00:00Z

# the sample must be complete: a chart missing from run 2 is an error naming chart and world
jq '.charts |= map(select(.chart != "app-6"))' "$G/1002/report-1002.json" >"$T/x" && mv "$T/x" "$G/1002/report-1002.json"
analyze "$T/analysis-missing" --run1 1001 --run2 1002 --cert 1003 --cert 1004 --recheck 1005
mrc=$?
eq "a chart missing from run 2: analyze.sh fails" "$mrc" 1
[[ $mrc -ne 0 ]] || tail -3 "$T/analyze.out"
grep -q 'run 1002: chart "app-6" is missing' "$T/analyze.err" && ok "  and names the run and the chart" || bad "  and names the run and the chart: $(tail -3 "$T/analyze.err")"
cp "$T/report-1002.json" "$G/1002/report-1002.json"

# analyze.sh refuses a bad run id before it calls gh
: >"$GH_LOG"
env PATH="$T/bin:$PATH" "$R/hack/compat-exp/analyze.sh" --out "$T/a2" --run1 '10;id' --run2 1002 >/dev/null 2>&1
eq "a run id with a metacharacter is refused (exit 2)" "$?" 2
eq "and gh is not called" "$(wc -l <"$GH_LOG" | tr -d ' ')" 0

echo
echo "$pass passed, $fail failed"
[[ $fail -eq 0 ]]
