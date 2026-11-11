#!/usr/bin/env bash
#
# Refusals and failures of hack/compat-exp/analyze.sh, with a stub gh and a
# throwaway repository that holds the real instrument. The successful path is in
# e2e_test.sh.
#
#   hack/compat-exp/test/analyze_test.sh
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
says() { if grep -q -- "$2" "$T/err"; then ok "$1"; else bad "$1: stderr lacks '$2': $(head -3 "$T/err" | tr '\n' ' ')"; fi; }

# shellcheck source=lib-runmeta.sh
source "$here/lib-runmeta.sh"

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.test GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.test

# --- the qualification: 60 candidates, all qualified, selected by select.sh -------------

mkdir -p "$T/qual" "$T/bin" "$T/fix"
jq -n '{candidates: [range(1; 61) as $i | {rank: $i, stars: (10000 - $i), package: "app-\($i)", repository: "repo\($i)",
  publisher: "pub\($i)", repo: "https://example.test/\($i)", chart: "app-\($i)", version: "1.0.\($i)"}]}' >"$T/candidates.json"
for i in $(seq 1 60); do
  jq -n --arg id "app-$i" '{id: $id, qualified: true, firstFailure: null,
    installs: [{n:1,durationSeconds:120,ready:true,helmRc:0},{n:2,durationSeconds:118,ready:true,helmRc:0},{n:3,durationSeconds:119,ready:true,helmRc:0}],
    bias: {hasStatefulSet: false, hasNetworkPolicy: false, hasNonHttpPort: false, hasHeadlessService: false}}' >"$T/qual/app-$i.qualify.json"
done
"$repo/hack/compat-exp/select.sh" "$T/candidates.json" "$T/qual" "$T/charts-exp.json" 7001 >/dev/null || { echo "select.sh failed"; exit 1; }

# --- a throwaway repository with the real instrument, frozen -----------------------------------------

mkrepo() { # DIR: tagged instrument (the recheck version decided, as the freeze commit does), then the frozen list committed
  local D="$1"
  mkdir -p "$D/docs" "$D/.github"
  cp -R "$repo/hack" "$D/hack"
  cp -R "$repo/.github/workflows" "$D/.github/workflows"
  sed -i.bak 's/"recheck-version-undecided"/"1.32.0"/' "$D/.github/workflows/compat-exp.yml" && rm -f "$D/.github/workflows/compat-exp.yml.bak"
  cp "$repo/docs/compat-experiment.md" "$D/docs/"
  cp "$repo/go.mod" "$repo/go.sum" "$D/"
  cp "$T/candidates.json" "$D/hack/compat-exp/candidates.json"
  git -C "$D" init -q -b main && git -C "$D" add -A && git -C "$D" commit -q -m instrument && git -C "$D" tag compat-exp-freeze
  cp "$T/charts-exp.json" "$D/hack/compat-exp/charts-exp.json"
  git -C "$D" add hack/compat-exp/charts-exp.json && git -C "$D" commit -q -m "frozen list"
}
R="$T/repo"
mkrepo "$R"
tagsha="$(git -C "$R" rev-parse compat-exp-freeze)"
recheck_v=1.32.0
# where the workflow's metadata step runs: a clone of the instrument with the tag
git clone -q "$R" "$T/gen" || { echo "clone failed"; exit 1; }

# --- the gh stub ---------------------------------------------------------------------------------------------

cat >"$T/bin/gh" <<'STUB'
#!/usr/bin/env bash
echo "gh $*" >>"$GH_LOG"
[[ "$1 $2" == "run download" || "$1 $2" == "run view" ]] || exit 9
id="$3"
if [[ "$2" == download ]]; then
  while [[ $# -gt 0 ]]; do case "$1" in -D) dir="$2"; shift 2 ;; -n) name="$2"; shift 2 ;; *) shift ;; esac; done
  if [[ "$name" =~ ^run-metadata-$id-([0-9]+)$ ]]; then
    [[ -d "$GH_FIX/$id.meta-${BASH_REMATCH[1]}" ]] || { echo "no such artifact $name" >&2; exit 1; }
    cp -R "$GH_FIX/$id.meta-${BASH_REMATCH[1]}/." "$dir/"
    exit 0
  fi
  [[ -d "$GH_FIX/$id" && "$name" == "$(cat "$GH_FIX/$id.artifact")" ]] || { echo "no such artifact $name" >&2; exit 1; }
  cp -R "$GH_FIX/$id/." "$dir/"
else
  att=""
  while [[ $# -gt 0 ]]; do case "$1" in --attempt) att="$2"; shift 2 ;; *) shift ;; esac; done
  if [[ -n "$att" && -f "$GH_FIX/$id.attempt-$att.json" ]]; then cat "$GH_FIX/$id.attempt-$att.json"; else cat "$GH_FIX/$id.view.json"; fi
fi
STUB
chmod +x "$T/bin/gh"
export GH_LOG="$T/gh.log" GH_FIX="$T/fix"

# mkrun ID MODE ISTIO [HEAD]: a completed compat-exp run and its artifact
mkrun() {
  local id="$1" mode="$2" istio="$3" head="${4:-$tagsha}" d="$T/fix/$1" art
  rm -rf "$d"; mkdir -p "$d"
  if [[ "$mode" == qualify ]]; then
    art="charts-exp-$id"
    mkdir -p "$d/qual" "$d/out"
    cp "$T/qual"/*.qualify.json "$d/qual/"
    cp "$T/charts-exp.json" "$d/out/"
  else
    art="compat-exp-report-$id"
    mkdir -p "$d/results"
    echo '{"schema":1,"oracle":"W0","worlds":["W1","W3"],"runsRequired":3,"charts":[]}' >"$d/report-$id.json"
  fi
  echo "$art" >"$T/fix/$id.artifact"
  # the run metadata is what the workflow's guard job writes (lib-runmeta.sh runs that step), one artifact per attempt
  rm -rf "$T/fix/$id".meta-* "$T/fix/$id".attempt-*
  gen_run_metadata "$repo/.github/workflows/compat-exp.yml" "$T/gen" "$T/fix/$id.meta-1/run-metadata.json" "$mode" "$istio" "$head" "$id" 1 || { echo "metadata step failed"; exit 1; }
  jq -n --arg id "$id" --arg sha "$head" '{attempt: 1, conclusion: "success", databaseId: ($id | tonumber), event: "workflow_dispatch", headSha: $sha,
    startedAt: "2026-11-20T08:00:00Z", status: "completed", updatedAt: "2026-11-20T09:00:00Z", workflowName: "compat-exp",
    jobs: [{name: "observe", startedAt: "2026-11-20T08:01:00Z", completedAt: "2026-11-20T08:50:00Z"}]}' >"$T/fix/$id.view.json"
}
setview() { # ID JQ-FILTER: edit the stored run view
  jq "$2" "$T/fix/$1.view.json" >"$T/x" && mv "$T/x" "$T/fix/$1.view.json"
}
setmeta() { jq "$2" "$T/fix/$1.meta-${3:-1}/run-metadata.json" >"$T/x" && mv "$T/x" "$T/fix/$1.meta-${3:-1}/run-metadata.json"; }
# a second attempt of run ID: its view says attempt 2, its guard job ran again (a metadata artifact of its own)
addattempt() {
  setview "$1" '.attempt = 2'
  cp "$T/fix/$1.view.json" "$T/fix/$1.attempt-2.json"
  jq '.attempt = 1' "$T/fix/$1.view.json" >"$T/fix/$1.attempt-1.json"
  gen_run_metadata "$repo/.github/workflows/compat-exp.yml" "$T/gen" "$T/fix/$1.meta-2/run-metadata.json" "$2" "$3" "$tagsha" "$1" 2 || { echo "metadata step failed"; exit 1; }
}
resetall() {
  mkrun 7001 qualify 1.31.3
  mkrun 11 observe 1.31.3
  mkrun 12 observe 1.31.3
  mkrun 13 certify 1.31.3
  mkrun 14 certify 1.31.3
  mkrun 15 recheck "$recheck_v"
}
resetall

(cd "$repo" && go build -o "$T/ledger" ./hack/compat-exp/ledger/cmd/ledger) && LEDGER_BIN="$T/ledger"

an() { # args...: rc, stderr text; runs the analysis of the throwaway repository in test mode with dead tools
  : >"$GH_LOG"
  rm -rf "$T/o"
  env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=1 CROSSRUN=/nonexistent/crossrun LEDGER="${LEDGER_BIN:-/nonexistent/ledger}" STATICCHECK=/nonexistent/sc \
    "$R/hack/compat-exp/analyze.sh" --out "$T/o" "$@" >"$T/out" 2>"$T/err"
  rc=$?
}
gh_calls() { wc -l <"$GH_LOG" | tr -d ' '; }
A=(--run1 11 --run2 12)

# --- usage and run ids: refused before anything else, gh is not called -----------------------------------------

for bad_id in 'abc' '10;id' '1 2' '-5' '1e5' '../x' '' '$(id)' '99999999999999999999999'; do
  an --run1 "$bad_id" --run2 12
  eq "run1 '$bad_id' is refused" "$rc" 2
  eq "  and gh is not called" "$(gh_calls)" 0
done
an --run1 11 --run2 12x;           eq "run2 with a letter is refused" "$rc" 2
an "${A[@]}" --cert 1a;            eq "a cert id with a letter is refused" "$rc" 2
an "${A[@]}" --recheck '1;2';      eq "a recheck id with a metacharacter is refused" "$rc" 2
an --run1 11 --run2 11;            eq "the same id twice is refused" "$rc" 2
an "${A[@]}" --repo 'a b/c';       eq "a malformed --repo is refused" "$rc" 2
mkdir -p "$T/full" && touch "$T/full/x"
: >"$GH_LOG"
env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=1 "$R/hack/compat-exp/analyze.sh" --out "$T/full" --run1 11 --run2 12 >/dev/null 2>&1
eq "an output directory that is not empty is refused" "$?" 2
eq "  without calling gh" "$(gh_calls)" 0
env PATH="$T/bin:$PATH" "$R/hack/compat-exp/analyze.sh" --run1 11 >/dev/null 2>&1
eq "no --out is a usage error" "$?" 2

# --- the tools come from the tree (F2) --------------------------------------------------------------------------

for v in CROSSRUN LEDGER STATICCHECK STATIC_CAUSES STATIC_WORKFLOW; do
  : >"$GH_LOG"
  env PATH="$T/bin:$PATH" "$v=/x" "$R/hack/compat-exp/analyze.sh" --out "$T/o1$v" --run1 11 --run2 12 >"$T/out" 2>"$T/err"
  eq "$v without MMC_ANALYZE_TEST=1 is refused" "$?" 2
  says "  and says why" "MMC_ANALYZE_TEST"
done
env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=yes "$R/hack/compat-exp/analyze.sh" --out "$T/o2" --run1 11 >/dev/null 2>&1
eq "MMC_ANALYZE_TEST must be exactly 1" "$?" 2

# --- the freeze --------------------------------------------------------------------------------------------------

# no tag
N="$T/notag"; mkdir -p "$N"; cp -R "$R/." "$N/"; git -C "$N" tag -d compat-exp-freeze >/dev/null
: >"$GH_LOG"
env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=1 CROSSRUN=/x LEDGER=/x STATICCHECK=/x "$N/hack/compat-exp/analyze.sh" --out "$T/on" "${A[@]}" >/dev/null 2>"$T/err"
eq "no tag: refused" "$?" 1
says "  and says the tag is missing" "does not exist locally"
eq "  gh was not called" "$(gh_calls)" 0

# HEAD is not a descendant of the tag
N="$T/notdesc"; rm -rf "$N"; cp -R "$R" "$N"
git -C "$N" checkout -q -b side compat-exp-freeze
git -C "$N" commit -q --allow-empty -m "a commit main does not have"
git -C "$N" tag -f compat-exp-freeze side >/dev/null
git -C "$N" checkout -q main
: >"$GH_LOG"
env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=1 CROSSRUN=/x LEDGER=/x STATICCHECK=/x "$N/hack/compat-exp/analyze.sh" --out "$T/on2" "${A[@]}" >/dev/null 2>"$T/err"
eq "HEAD that is not a descendant of the tag: refused" "$?" 1
says "  and says so" "not tag compat-exp-freeze or a descendant"

# a frozen file changed, committed or not
N="$T/dirtyfrozen"; rm -rf "$N"; cp -R "$R" "$N"
echo "// tampered" >>"$N/hack/compat-exp/crossrun/main.go"
: >"$GH_LOG"
env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=1 CROSSRUN=/x LEDGER=/x STATICCHECK=/x "$N/hack/compat-exp/analyze.sh" --out "$T/on3" "${A[@]}" >/dev/null 2>"$T/err"
eq "an uncommitted change to a frozen file: refused" "$?" 1
says "  and names the file" "crossrun/main.go"
eq "  gh was not called" "$(gh_calls)" 0
git -C "$N" add -A && git -C "$N" commit -q -m "change crossrun"
env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=1 CROSSRUN=/x LEDGER=/x STATICCHECK=/x "$N/hack/compat-exp/analyze.sh" --out "$T/on4" "${A[@]}" >/dev/null 2>"$T/err"
eq "a committed change to a frozen file: refused" "$?" 1

# data files: uncommitted changes
N="$T/dirtydata"; rm -rf "$N"; cp -R "$R" "$N"
echo '{"schema":1,"rows":[],"newKeys":[],"x":1}' >"$N/hack/compat-exp/ledger.json"
env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=1 CROSSRUN=/x LEDGER=/x STATICCHECK=/x "$N/hack/compat-exp/analyze.sh" --out "$T/on5" "${A[@]}" >/dev/null 2>"$T/err"
eq "an uncommitted change to ledger.json: refused" "$?" 1
says "  and names the file" "ledger.json"
git -C "$N" checkout -q hack/compat-exp/ledger.json
echo "x" >"$N/hack/compat-exp/patches/new.patch"
env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=1 CROSSRUN=/x LEDGER=/x STATICCHECK=/x "$N/hack/compat-exp/analyze.sh" --out "$T/on6" "${A[@]}" >/dev/null 2>"$T/err"
eq "an untracked patch: refused" "$?" 1
rm "$N/hack/compat-exp/patches/new.patch"
echo "x" >>"$N/hack/compat-exp/charts-exp.json"
env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=1 CROSSRUN=/x LEDGER=/x STATICCHECK=/x "$N/hack/compat-exp/analyze.sh" --out "$T/on7" "${A[@]}" >/dev/null 2>"$T/err"
eq "an uncommitted change to the chart list: refused" "$?" 1
git -C "$N" checkout -q hack/compat-exp/charts-exp.json
# a file outside the repository
cp "$R/hack/compat-exp/ledger.json" "$T/outside-ledger.json"
an "${A[@]}" --ledger "$T/outside-ledger.json"
eq "a ledger outside the repository: refused" "$rc" 2
says "  and says why" "outside the repository"

# --- the chart list must be a frozen experiment list --------------------------------------------------------------------

an "${A[@]}" --charts hack/compat-exp/acceptance-charts.json
eq "the acceptance list: refused" "$rc" 2
eq "  without calling gh" "$(gh_calls)" 0
N="$T/noqual"; rm -rf "$N"; cp -R "$R" "$N"
jq -S 'del(.qualification) | del(.sha256)' "$N/hack/compat-exp/charts-exp.json" >"$T/x"
jq -S --arg h "$(jq -S -c . "$T/x" | shasum -a 256 | cut -d' ' -f1)" '. + {sha256: $h}' "$T/x" >"$N/hack/compat-exp/charts-exp.json"
git -C "$N" add -A && git -C "$N" commit -q -m "list without qualification"
env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=1 CROSSRUN=/x LEDGER=/x STATICCHECK=/x "$N/hack/compat-exp/analyze.sh" --out "$T/on8" "${A[@]}" >/dev/null 2>"$T/err"
eq "a list without a qualification run id: refused" "$?" 2
says "  and says it is not a frozen experiment list" "not a frozen experiment list"

# --- every run id is checked (F2) ------------------------------------------------------------------------------------------

an "${A[@]}" --repo Git-Hospital/meshmedic
eq "(setup) the good fixtures reach the ledger check" "$rc" 1
grep -q -- "-R Git-Hospital/meshmedic" "$GH_LOG" && ok "--repo is passed to gh as -R" || bad "--repo is passed to gh as -R"
eq "gh was only asked to download and view" "$(grep -cv '^gh run \(download\|view\) ' "$GH_LOG")" 0
says "  (the stub reports are empty, so it stops at the static checks)" "reports hold no charts"


an "${A[@]}"; eq "a run on a descendant of the tag with unchanged frozen files is fine" "$(grep -c 'headSha\|refused' "$T/err")" 0
setview 12 '.headSha = "'"$(printf 'c%.0s' $(seq 1 40))"'"'; setmeta 12 '.headSha = "'"$(printf 'c%.0s' $(seq 1 40))"'"'
an "${A[@]}"; eq "run 2 on a commit that is not here: refused" "$rc" 1; says "  and says to fetch it" "not in this repository"
resetall
# a commit that changed a frozen file
N="$R"
git -C "$N" checkout -q -b other compat-exp-freeze
echo "// other" >>"$N/hack/compat-exp/analyze.sh"
git -C "$N" commit -qam "other branch edits analyze.sh"
bsha="$(git -C "$N" rev-parse HEAD)"
git -C "$N" checkout -q main
setview 12 '.headSha = "'"$bsha"'"'; setmeta 12 '.headSha = "'"$bsha"'"'
an "${A[@]}"; eq "run 2 on a commit with a changed frozen file: refused" "$rc" 1; says "  and names the file" "frozen instrument files at"
resetall
git -C "$N" checkout -q -b before compat-exp-freeze~1 2>/dev/null
git -C "$N" checkout -q main
# a commit that does not descend from the tag
git -C "$N" checkout -q --orphan loose
git -C "$N" rm -rfq . >/dev/null 2>&1; echo x >"$N/loose.txt"; git -C "$N" add loose.txt; git -C "$N" commit -qm loose
lsha="$(git -C "$N" rev-parse HEAD)"
git -C "$N" checkout -qf main
setview 11 '.headSha = "'"$lsha"'"'; setmeta 11 '.headSha = "'"$lsha"'"'
an "${A[@]}"; eq "run 1 on a commit that does not descend from the tag: refused" "$rc" 1; says "  and says so" "not tag compat-exp-freeze or a descendant"
resetall

setview 12 '.workflowName = "other"';        an "${A[@]}"; eq "a run of another workflow: refused" "$rc" 1; says "  and says so" "not compat-exp"; resetall
setview 12 '.event = "push"';                an "${A[@]}"; eq "a run not dispatched by hand: refused" "$rc" 1; says "  and says so" "not workflow_dispatch"; resetall
setview 12 '.status = "in_progress"';        an "${A[@]}"; eq "a run that has not completed: refused" "$rc" 1; says "  and says so" "not completed"; resetall
setview 12 '.databaseId = 99';               an "${A[@]}"; eq "gh returning another run: refused" "$rc" 1; resetall
rm -rf "$T/fix/12.meta-1";                   an "${A[@]}"; eq "a run without its run-metadata artifact: refused" "$rc" 1; says "  and names the artifact" "run-metadata-12-1"; resetall
setmeta 12 'del(.headSha) | . + {sha: "x"}'; an "${A[@]}"; eq "run metadata in the old analyze.sh shape (sha, no headSha): refused" "$rc" 1; says "  and says the fields are wrong" "fields and types"; resetall
setmeta 12 '. + {extra: 1}';                 an "${A[@]}"; eq "run metadata with a field the guard job does not write: refused" "$rc" 1; says "  and says the fields are wrong" "fields and types"; resetall
setmeta 12 '.headSha = "'"$(printf 'd%.0s' $(seq 1 40))"'"'; an "${A[@]}"; eq "run metadata of another commit than gh reports: refused" "$rc" 1; says "  and says so" "does not describe this run"; resetall
setmeta 12 '.runId = 99';                    an "${A[@]}"; eq "run metadata of another run: refused" "$rc" 1; says "  and says so" "does not describe this run"; resetall
setmeta 12 '.eventName = "push"';            an "${A[@]}"; eq "run metadata of another event than gh reports: refused" "$rc" 1; says "  and says so" "does not describe this run"; resetall
setmeta 12 '.runAttempt = 2';                an "${A[@]}"; eq "run metadata of another attempt than its artifact name: refused" "$rc" 1; says "  and says so" "does not describe this run"; resetall
setmeta 12 '.workflowRef = "o/r/.github/workflows/other.yml@refs/heads/main"'; an "${A[@]}"; eq "run metadata from another workflow file: refused" "$rc" 1; says "  and says so" "names the workflow"; resetall
setmeta 12 '.freezeTagSha = "'"$(printf 'e%.0s' $(seq 1 40))"'"'; an "${A[@]}"; eq "run metadata with another freeze tag commit: refused" "$rc" 1; says "  and says so" "freeze tag was at"; resetall
setmeta 12 '.chartsSha256 = "'"$(printf 'a%.0s' $(seq 1 64))"'"'; an "${A[@]}"; eq "run metadata with another chart list hash: refused" "$rc" 1; says "  and says so" "another sha256"; resetall
# several attempts: every attempt's guard job wrote its own artifact, and they agree on everything but runAttempt
addattempt 12 observe 1.31.3
an "${A[@]}"; eq "two attempts with equal metadata: accepted (stops later at the empty reports)" "$rc" 1; says "  and gets as far as the static checks" "reports hold no charts"
eq "  and both artifacts were downloaded" "$(grep -c 'run download 12 -n run-metadata-12-' "$GH_LOG")" 2
setmeta 12 '.fixId = "fix-a"' 2;             an "${A[@]}"; eq "attempts that differ in the fix id: refused" "$rc" 1; says "  and says so" "differs from attempt 1"; resetall
addattempt 12 observe 1.31.3; setmeta 12 '.istio = "1.32.0"' 2; an "${A[@]}"; eq "attempts that differ in the Istio version: refused" "$rc" 1; says "  and says so" "differs from attempt 1"; resetall
addattempt 12 observe 1.31.3; setmeta 12 '.chartIds = "app-1"' 2; an "${A[@]}"; eq "attempts that differ in the chart ids: refused" "$rc" 1; says "  and says so" "differs from attempt 1"; resetall
addattempt 12 observe 1.31.3; rm -rf "$T/fix/12.meta-2"
an "${A[@]}"; eq "a later attempt without a metadata artifact (the guard job was not rerun): accepted" "$rc" 1; says "  and gets as far as the static checks" "reports hold no charts"; resetall
addattempt 12 observe 1.31.3; rm -rf "$T/fix/12.meta-1"
an "${A[@]}"; eq "attempt 1 without its metadata artifact: refused" "$rc" 1; says "  and says so" "run-metadata-12-1"; resetall
setmeta 12 '.mode = "certify"';              an "${A[@]}"; eq "run 2 dispatched as certify: refused" "$rc" 1; says "  and names the role" "run2"; resetall
setmeta 11 '.mode = "recheck"';              an "${A[@]}"; eq "run 1 dispatched as recheck: refused" "$rc" 1; resetall
setmeta 12 '.istio = "1.32.0"';              an "${A[@]}"; eq "run 2 on another Istio version: refused" "$rc" 1; says "  and says the pinned one" "pinned version for run2 is 1.31.3"; resetall
setmeta 12 '.chartsFile = "hack/compat-exp/other.json"'; an "${A[@]}"; eq "run 2 on another chart list: refused" "$rc" 1; resetall
echo '{"environment":{"istio":"1.30.0"}}' >"$T/fix/12/results/x.json"
an "${A[@]}"; eq "records that disagree with the metadata on Istio: refused" "$rc" 1; says "  and says so" "its records say Istio"; resetall
an "${A[@]}" --cert 13 --cert 14
eq "certification runs fit their role" "$(grep -c 'refused\|dispatched' "$T/err")" 0
setmeta 14 '.mode = "observe"';              an "${A[@]}" --cert 13 --cert 14; eq "a certification run dispatched as observe: refused" "$rc" 1; resetall
setmeta 15 '.istio = "1.31.3"';              an "${A[@]}" --recheck 15; eq "a recheck on the primary version: refused" "$rc" 1; says "  and says the pinned one" "pinned version for recheck is 1.32.0"; resetall
setmeta 15 '.mode = "observe"';              an "${A[@]}" --recheck 15; eq "a recheck run dispatched as observe: refused" "$rc" 1; resetall
setmeta 7001 '.mode = "observe"';            an "${A[@]}"; eq "a qualification run that was not dispatched as qualify: refused" "$rc" 1; resetall

# the qualification: the list must reproduce from the run's records
jq '.chart = "app-1"' "$T/fix/7001/qual/app-5.qualify.json" >/dev/null
jq '.qualified = false | .firstFailure = {step: "install", install: 1, reason: "not-ready", detail: ""} | .installs = []' "$T/fix/7001/qual/app-3.qualify.json" >"$T/x" && mv "$T/x" "$T/fix/7001/qual/app-3.qualify.json"
an "${A[@]}"; eq "records that differ from the ones the list was selected from: refused" "$rc" 1; says "  and says the sample does not reproduce" "does not reproduce"; resetall
rm "$T/fix/7001/qual/app-9.qualify.json"
an "${A[@]}"; eq "a missing qualification record: refused" "$rc" 1; says "  and says so" "select.sh"; resetall
rm -rf "$T/fix/7001/qual"
an "${A[@]}"; eq "no qual/ in the qualification artifact: refused" "$rc" 1; resetall

# ledger failure: after all of the above checks pass
N="$T/badledger"; rm -rf "$N"; cp -R "$R" "$N"
echo '{"schema":1,"rows":[{"chart":"x"}],"newKeys":[]}' >"$N/hack/compat-exp/ledger.json"
git -C "$N" add -A && git -C "$N" commit -q -m "bad ledger"
rm -rf "$T/o9"
env PATH="$T/bin:$PATH" MMC_ANALYZE_TEST=1 CROSSRUN=/nonexistent LEDGER="$LEDGER_BIN" STATICCHECK=/nonexistent "$N/hack/compat-exp/analyze.sh" --out "$T/o9" "${A[@]}" >/dev/null 2>"$T/err"
eq "a ledger that fails its check stops before static.sh and crossrun" "$?" 1
says "  and says so" "ledger check failed"
[[ ! -e "$T/o9/static.json" && ! -e "$T/o9/result.json" ]] && ok "  and writes no result" || bad "  and writes no result"

echo
echo "$pass passed, $fail failed"
[[ $fail -eq 0 ]]
