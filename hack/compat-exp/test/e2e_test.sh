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
#      helm template returns, and gh's run metadata (start and end times).
#   5. the second workflow run's records are a copy of the first's (the stub is
#      deterministic), and so are the second certification run's.
#
# shellcheck disable=SC2015,SC2016
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
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
must "select.sh" "$repo/hack/compat-exp/select.sh" "$T/frame/candidates.json" "$T/qual" "$T/charts-exp.json"
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
esac
exit 0
STUB
cat >"$T/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
a=" $* "
case "$a" in
  *" config get-contexts "*) echo "$STUB_CONTEXT" ;;
  *" get pods -o json "*) cat "$STUB_PODS" ;;
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
# observe_set RESULTS_DIR CHARTS_FILE ISTIO ID...: W0, W1, W3 x runs 1 to 3 of each id. A chart whose
# id is app-2 (or its fix) is not Ready in W3, unless the id ends in -fix.
observe_set() {
  local dir="$1" cf="$2" istio="$3" id w c run
  shift 3
  for w in W0 W1 W3; do
    c="mmc-$(tr '[:upper:]' '[:lower:]' <<<"$w")"
    : >"$T/state/$c.kubeconfig"
    printf 'world=%s\nistio=%s\nkubernetes=1.36\n' "$w" "$istio" >"$T/state/$c.world"
  done
  for id in "$@"; do
    for w in W0 W1 W3; do
      local ready=true
      [[ "$id" == app-2 && "$w" == W3 ]] && ready=false
      mkpods "$ready" "${id%-fix}" >"$S/pods.json"
      for run in 1 2 3; do
        env PATH="$T/bin:$PATH" STUB_CONTEXT="kind-mmc-$(tr '[:upper:]' '[:lower:]' <<<"$w")" STUB_PODS="$S/pods.json" \
          MMC_STATE="$T/state" MMC_SETTLE=0 MMC_LATE=0 CHARTS_FILE="$cf" \
          bash "$repo/hack/compat-pilot/observe.sh" "$w" "$id" "$run" "$dir" >/dev/null 2>"$T/observe.err" || {
          bad "observe.sh $w $id run$run"; head -3 "$T/observe.err"; return 1; }
      done
    done
  done
}

# --- 5. run 1 and run 2 -----------------------------------------------------------------------------

observe_set "$T/res-1001" "$T/plan-observe/charts.json" 1.31.3 $id1 $id2 $id3 && ok "observe.sh: 3 charts x 3 worlds x 3 runs"
eq "records: 27 files named <id>.<world>.run<N>.json" "$(find "$T/res-1001" -name "app-[123].W[013].run[123].json" | wc -l | tr -d " ")" 27
eq "ids: the record's chart field is the list's id" "$(jq -s -c 'map(.chart) | unique' "$T/res-1001"/*.json)" '["app-1","app-2","app-3"]'
cp -R "$T/res-1001" "$T/res-1002"   # hand-made: a second run with the same stub behaviour
for r in 1001 1002; do
  "$B/difftable" -json -worlds W1,W3 "$T/res-$r" >"$T/report-$r.json" 2>"$T/dt.err" && ok "difftable -json run $r" || { bad "difftable -json run $r"; head -3 "$T/dt.err"; }
done
eq "ids: the report's charts are the records' charts" "$(jq -c '[.charts[].chart]' "$T/report-1001.json")" '["app-1","app-2","app-3"]'
eq "run 1: app-2 differs in W3 only" "$(jq -c '[.charts[] | {c: .chart, w: [.worlds[] | {(.world): .verdict}] | add}]' "$T/report-1001.json")" \
  '[{"c":"app-1","w":{"W1":"same","W3":"same"}},{"c":"app-2","w":{"W1":"same","W3":"differs"}},{"c":"app-3","w":{"W1":"same","W3":"same"}}]'
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
observe_set "$T/res-1003" "$T/plan-certify/charts.json" 1.31.3 app-2 app-2-fix && ok "observe.sh for the base chart and its fix"
eq "ids: the fix's record carries fixOf = the base id" "$(jq -r '.fixOf' "$T/res-1003/app-2-fix.W3.run1.json")" "app-2"
"$B/difftable" -json -worlds W1,W3 -cell-worlds W3 -ignore "$R/hack/compat-exp/ignore/app-2.json" "$T/res-1003" >"$T/report-1003.json" 2>"$T/dt.err" &&
  ok "difftable -json -cell-worlds on the certification records" || { bad "difftable certification"; head -3 "$T/dt.err"; }
eq "certificate: earned, W3 cleared" "$(jq -c '.charts[] | select(.fixOf == "app-2") | .certificate | {certified, cleared}' "$T/report-1003.json")" '{"certified":true,"cleared":["W3"]}'
cp -R "$T/res-1003" "$T/res-1004"   # hand-made: the second certification run
cp "$T/report-1003.json" "$T/report-1004.json"

# --- 7. recheck on the second version -----------------------------------------------------------------------

observe_set "$T/res-1005" "$T/plan-observe/charts.json" 1.32.0 $id2 && ok "observe.sh for the recheck"
"$B/difftable" -json -worlds W1,W3 "$T/res-1005" >"$T/report-1005.json" 2>/dev/null && ok "difftable -json recheck"

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
"$B/ledger" check -ledger "$T/ledger.json" -causes "$repo/hack/compat-exp/causes.json" >"$T/lc.out" 2>&1 && ok "ledger check on the real causes.json" || { bad "ledger check"; cat "$T/lc.out"; }

# --- 9. analyze.sh with a gh stub: download, ledger check, static.sh, crossrun --------------------------------------

G="$T/gh"
for r in 1001 1002 1003 1004 1005; do
  mkdir -p "$G/$r"
  cp "$T/report-$r.json" "$G/$r/report-$r.json"
  "$B/difftable" -worlds W1,W3 "$T/res-$r" >"$G/$r/table.md" 2>/dev/null
  cp -R "$T/res-$r" "$G/$r/results"
done
# run metadata: run 2 starts 13 h after run 1 ended
echo '{"status":"completed","conclusion":"success","startedAt":"2026-11-20T08:00:00Z","updatedAt":"2026-11-20T09:10:00Z"}' >"$G/1001.view.json"
echo '{"status":"completed","conclusion":"success","startedAt":"2026-11-20T22:10:00Z","updatedAt":"2026-11-20T23:20:00Z"}' >"$G/1002.view.json"
for r in 1003 1004 1005; do echo '{"status":"completed","conclusion":"success","startedAt":"2026-11-21T08:00:00Z","updatedAt":"2026-11-21T08:30:00Z"}' >"$G/$r.view.json"; done
cat >"$T/bin/gh" <<'STUB'
#!/usr/bin/env bash
echo "gh $*" >>"$GH_LOG"
[[ "$1 $2" == "run download" || "$1 $2" == "run view" ]] || { echo "gh stub: only run download and run view are allowed" >&2; exit 9; }
id="$3"
if [[ "$2" == download ]]; then
  while [[ $# -gt 0 ]]; do case "$1" in -D) dir="$2"; shift 2 ;; -n) name="$2"; shift 2 ;; *) shift ;; esac; done
  [[ "$name" == "compat-exp-report-$id" && -d "$GH_FIX/$id" ]] || { echo "no artifact $name" >&2; exit 1; }
  cp -R "$GH_FIX/$id/." "$dir/"
else
  cat "$GH_FIX/$id.view.json"
fi
STUB
chmod +x "$T/bin/gh"
# what helm template returns: app-2 has L7 config without a waypoint, the others are clean
cp "$repo/hack/compat-exp/staticcheck/testdata/protocol-consistent.yaml" "$S/render/default.yaml"
cp "$repo/hack/compat-exp/staticcheck/testdata/l7-routes.yaml" "$S/render/app-2.yaml"

export GH_LOG="$T/gh.log" GH_FIX="$G"
: >"$GH_LOG"
env PATH="$T/bin:$PATH" CROSSRUN="$B/crossrun" LEDGER="$B/ledger" STATICCHECK="$B/staticcheck" MMC_STATE="$T/state" \
  "$repo/hack/compat-exp/analyze.sh" --out "$T/analysis" --run1 1001 --run2 1002 --cert 1003 --cert 1004 --recheck 1005 \
  --charts "$T/charts-exp.json" --ledger "$T/ledger.json" >"$T/analyze.out" 2>"$T/analyze.err"
arc=$?
eq "analyze.sh exits 0" "$arc" 0
[[ $arc -eq 0 ]] || head -10 "$T/analyze.err"
eq "gh was used for run download and run view only" "$(grep -cv '^gh run \(download\|view\) ' "$GH_LOG")" 0
res="$T/analysis/result.json"
eq "manifest: run times come from gh run view" "$(jq -c '[.runs[] | {id, started, ended}]' "$T/analysis/inputs.json")" \
  '[{"id":"1001","started":"2026-11-20T08:00:00Z","ended":"2026-11-20T09:10:00Z"},{"id":"1002","started":"2026-11-20T22:10:00Z","ended":"2026-11-20T23:20:00Z"}]'
eq "manifest: recheck istio comes from the records" "$(jq -c '.recheck | {id, istio}' "$T/analysis/inputs.json")" '{"id":"1005","istio":"1.32.0"}'
eq "static.json: rows for the 3 charts x 2 worlds x the checks" "$(jq length "$T/analysis/static.json")" "$((3 * 2 * 8))"
eq "static: only app-2 is flagged by the L7 check, in W3" "$(jq -c '[.[] | select(.flagged)] | map([.chart, .world, .check])' "$T/analysis/static.json")" '[["app-2","W3","l7-without-waypoint"]]'
eq "static: the L7 check is not applicable to W1 (not 'not flagged')" "$(jq -c '.[] | select(.chart == "app-2" and .world == "W1" and .check == "l7-without-waypoint") | {applicable, flagged}' "$T/analysis/static.json")" '{"applicable":false,"flagged":false}'
eq "crossrun: two cells of one chart, both counted and credited to ambient-l7" "$(jq -c '[.cells[] | select(.status == "counted") | {chart, world, credit}]' "$res")" '[{"chart":"app-2","world":"W3","credit":"ambient-l7"},{"chart":"app-2","world":"W3","credit":"ambient-l7"}]'
eq "crossrun: metrics" "$(jq -c '.metrics' "$res")" '{"nNew":1,"pNew":1,"d":1,"pD":1,"s":1}'
eq "crossrun: S was computed from static.json" "$(jq -c '[.sComputed, (.staticChecks[0] | {key, check, flagsEveryCountedFinding, qualifies})]' "$res")" '[true,{"key":"ambient-l7","check":"l7-without-waypoint","flagsEveryCountedFinding":true,"qualifies":true}]'
eq "crossrun: region E (N_new 1)" "$(jq -r '.region.region' "$res")" E
eq "crossrun: run gap is 14 h and fine" "$(jq -c '[.gapOk, .gapHours]' "$res")" '[true,13]'
eq "ids: every cell's chart is a chart of the frozen list" "$(jq -r --slurpfile l "$T/charts-exp.json" '[.cells[].chart] | unique - ([$l[0].charts[].id, $l[0].reserves[].id]) | length' "$res")" 0
eq "crossrun: no warnings" "$(jq -c '.warnings' "$res")" '[]'
must "result.md written" test -s "$T/analysis/result.md"

# analyze.sh refuses a bad run id before it calls gh
: >"$GH_LOG"
env PATH="$T/bin:$PATH" "$repo/hack/compat-exp/analyze.sh" --out "$T/a2" --run1 '10;id' --run2 1002 >/dev/null 2>&1
eq "a run id with a metacharacter is refused (exit 2)" "$?" 2
eq "and gh is not called" "$(wc -l <"$GH_LOG" | tr -d ' ')" 0

echo
echo "$pass passed, $fail failed"
[[ $fail -eq 0 ]]
