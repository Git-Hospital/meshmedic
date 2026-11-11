#!/usr/bin/env bash
#
# Static checks of .github/workflows/compat-exp.yml. The workflow is parsed
# (yq to JSON, then jq) and the security properties asserted. If actionlint is
# installed it runs too; if not, that is said and nothing is installed.
#
#   hack/compat-exp/test/workflow_test.sh
#
# Nothing here runs the workflow. It has not run on Actions.
# The ok-or-fail one-liners are deliberate: ok and fail always succeed.
# shellcheck disable=SC2015,SC2016
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
wf="$repo/.github/workflows/compat-exp.yml"
guard="$repo/hack/compat-exp/guard.sh"

fails=0
fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }
ok() { echo "ok: $*"; }
eq() { # name got want
  if [[ "$2" == "$3" ]]; then ok "$1"; else fail "$1: got '$2', want '$3'"; fi
}

for tool in yq jq; do command -v "$tool" >/dev/null || { echo "need $tool" >&2; exit 2; }; done
j="$(mktemp)"
trap 'rm -f "$j"' EXIT
yq -o=json '.' "$wf" >"$j" || { echo "the workflow does not parse" >&2; exit 1; }
q() { jq -c "$1" "$j"; }

# triggers
eq "dispatch is the only trigger" "$(q '.on | keys')" '["workflow_dispatch"]'
eq "no pull_request_target anywhere" "$(grep -c 'pull_request_target' "$wf" || true)" 0
eq "no pull_request trigger" "$(q '.on | has("pull_request")')" false
eq "no push trigger" "$(q '.on | has("push")')" false

# inputs
eq "mode choices" "$(q '.on.workflow_dispatch.inputs.mode | [.type, .options]')" '["choice",["acceptance","qualify","observe","certify","recheck"]]'
eq "istio is a choice of exactly two values" "$(q '.on.workflow_dispatch.inputs.istio | [.type, (.options | length)]')" '["choice",2]'
eq "istio first value is the pinned primary" "$(q '.on.workflow_dispatch.inputs.istio.options[0]')" '"1.31.3"'
eq "ISTIO_PRIMARY matches" "$(q '.env.ISTIO_PRIMARY')" '"1.31.3"'
eq "second istio value is the freeze placeholder" "$(q '.on.workflow_dispatch.inputs.istio.options[1]')" '"recheck-version-undecided"'
eq "the placeholder line is marked for the freeze" "$(grep -c 'FREEZE EDIT' "$wf")" 1
eq "chart list, fixes, ids and fix id are strings" "$(q '[.on.workflow_dispatch.inputs | to_entries[] | select(.key != "mode" and .key != "istio") | .value.type] | unique')" '["string"]'

# permissions and secrets
eq "top-level permissions are contents: read" "$(q '.permissions')" '{"contents":"read"}'
eq "no job sets its own permissions" "$(q '[.jobs[] | has("permissions")] | any')" false
eq "no secrets are referenced" "$(grep -c 'secrets\.' "$wf" || true)" 0
eq "no GITHUB_TOKEN is referenced" "$(grep -c 'GITHUB_TOKEN\|github\.token' "$wf" || true)" 0
eq "the warning against secrets and write permissions is at the top" "$(sed -n '1,40p' "$wf" | grep -c 'must never be given a secret')" 1
eq "no self-hosted runner" "$(q '[.jobs[] | ."runs-on"] | unique')" '["ubuntu-latest"]'

# pinned actions
bad="$(jq -r '[.jobs[].steps[]? | .uses // empty | select(test("@[0-9a-f]{40}$") | not)] | join(", ")' "$j")"
eq "every action is pinned to a full commit SHA" "$bad" ""
eq "only the pilot's four actions are used" "$(jq -c '[.jobs[].steps[]? | .uses // empty | sub("@.*"; "")] | unique' "$j")" '["actions/checkout","actions/download-artifact","actions/setup-go","actions/upload-artifact"]'
eq "no local or docker actions" "$(jq -c '[.jobs[].steps[]? | .uses // empty | select(startswith("./") or startswith("docker://"))] | length' "$j")" 0
eq "checkout never persists credentials" "$(jq -c '[.jobs[].steps[]? | select((.uses // "") | startswith("actions/checkout")) | .with["persist-credentials"]] | unique' "$j")" '[false]'

# no input or other expression reaches a script
inlined="$(jq -r '[.jobs | to_entries[] | .key as $job | .value.steps[]? | select(.run != null) | select(.run | test("\\$\\{\\{")) | $job + ": " + (.name // .id // "step")] | join("; ")' "$j")"
eq "no \${{ }} expression inside any run block" "$inlined" ""
eq "inputs. expressions appear only under an env: key" "$(jq -c '. as $r | [paths(scalars) | select(. as $p | ($r | getpath($p) | tostring | test("inputs\\.")))
  | select(index("env") | not)] | length' "$j")" 0
eq "guard steps get their inputs through env" "$(jq -c '[.jobs.guard.steps[] | select(.name == "guard") | .env | keys] | .[0]' "$j")" '["GUARD_CHARTS_FILE","GUARD_CHART_IDS","GUARD_FIXES_FILE","GUARD_FIX_ID","GUARD_ISTIO","GUARD_MODE"]'
eq "matrix values reach scripts through env" "$(jq -c '[.jobs[].steps[]? | select(.run != null) | select(.run | test("matrix\\.")) ] | length' "$j")" 0

# structure: guard before everything, matrix limits, mirror, timeouts
eq "plan needs guard" "$(q '.jobs.plan.needs')" '"guard"'
eq "observe, qualify need plan" "$(q '[.jobs.observe.needs, .jobs.qualify.needs]')" '["plan","plan"]'
eq "observe: 3 worlds come from the plan, 15 parallel, 200 minutes" "$(q '.jobs.observe | [.strategy."max-parallel", .["timeout-minutes"], .strategy."fail-fast"]')" '[15,200,false]'
eq "qualify: 15 parallel" "$(q '.jobs.qualify.strategy."max-parallel"')" 15
eq "the registry mirror is on for every job" "$(q '.env.MMC_REGISTRY_MIRROR')" '"1"'
eq "no job turns the mirror off" "$(q '[.jobs[] | .env.MMC_REGISTRY_MIRROR? // empty] | length')" 0
eq "every job has a timeout" "$(q '[.jobs[] | has("timeout-minutes")] | all')" true
eq "qualify installs W0 only" "$(grep -c 'world.sh W0' "$wf")" 1
eq "observe records go to artifacts named with the run id" "$(jq -r '.jobs.observe.steps[] | select(.uses // "" | startswith("actions/upload-artifact")) | .with.name' "$j")" 'exp-${{ github.run_id }}-${{ matrix.chart }}-${{ matrix.world }}'
eq "the report job runs difftable with -json" "$(jq -r '.jobs.report.steps[] | select(.name == "difftable") | .run' "$j" | grep -c -- 'difftable -json')" 1
eq "report passes -cell-worlds and -ignore" "$(jq -r '.jobs.report.steps[] | select(.name == "difftable") | .run' "$j" | grep -c -- '-cell-worlds "\$CELL_WORLDS"\|-ignore "\$IGNORE_FILE"')" 2
eq "report compares W1 and W3 only" "$(jq -r '.jobs.report.steps[] | select(.name == "difftable") | .run' "$j" | grep -c -- '-worlds W1,W3')" 1
eq "select job runs select.sh" "$(jq -r '.jobs.select.steps[] | select(.name == "select") | .run' "$j" | grep -c 'select.sh')" 1
eq "the qualify and observe jobs read the plan's chart list" "$(q '[.jobs.qualify.env.CHARTS_FILE, .jobs.observe.env.CHARTS_FILE] | unique | length')" 1

# the post-run tools: ledger check in the guard job for certify and recheck, nothing else on the runner
eq "the guard job sets up Go for the ledger tool and uploads the run metadata" "$(jq -c '[.jobs.guard.steps[] | .uses // empty | sub("@.*"; "")]' "$j")" '["actions/checkout","actions/setup-go","actions/upload-artifact"]'
eq "the ledger check step runs after the guard step, the metadata after both" "$(jq -c '[.jobs.guard.steps[] | .name // empty]' "$j")" '["guard","ledger check","run metadata"]'
eq "the ledger check runs the ledger tool in certify and recheck only" "$(jq -c '.jobs.guard.steps[] | select(.name == "ledger check") | .run | [contains("= certify"), contains("= recheck"), contains("go run ./hack/compat-exp/ledger/cmd/ledger check")]' "$j")" '[true,true,true]'
eq "the ledger check takes the mode through env" "$(jq -c '.jobs.guard.steps[] | select(.name == "ledger check") | .env | keys' "$j")" '["GUARD_MODE"]'
eq "the notice step for the other tools is gone" "$(grep -c 'post-run tools present\|not invoked here' "$wf" || true)" 0
eq "no run block calls crossrun, static.sh or staticcheck" "$(jq -r '[.jobs[].steps[]? | .run // empty] | join(" ")' "$j" | grep -c 'crossrun\|static\.sh\|staticcheck' || true)" 0
eq "ledger is called only in the guard job" "$(jq -c '[.jobs | to_entries[] | select(.value.steps | map(.run // "") | join(" ") | test("ledger")) | .key]' "$j")" '["guard"]'

# C2 (a): no cache of any kind that a later, more trusted job could restore
eq "every setup-go step sets cache: false" "$(jq -c '[.jobs[].steps[]? | select((.uses // "") | startswith("actions/setup-go")) | .with.cache] | unique' "$j")" '[false]'
eq "there is at least one setup-go step" "$(jq -c '[.jobs[].steps[]? | select((.uses // "") | startswith("actions/setup-go"))] | length > 0' "$j")" true
eq "no actions/cache anywhere" "$(grep -c 'actions/cache' "$wf" || true)" 0

# C2 (b): the report job is its own job and treats the records as data
eq "report is a job of its own, after observe, with no step shared with observe" "$(q '[.jobs.report.needs, (.jobs.report | has("strategy"))]')" '[["plan","observe"],false]'
eq "report downloads every observe artifact into its own directory (no merge-multiple anywhere in it)" "$(jq -c '[.jobs.report.steps[] | select((.uses // "") | startswith("actions/download-artifact")) | .with | has("merge-multiple")]' "$j")" '[false]'
eq "observe artifact name is fixed by chart and world" "$(jq -r '.jobs.observe.steps[] | select((.uses // "") | startswith("actions/upload-artifact")) | .with.name' "$j")" 'exp-${{ github.run_id }}-${{ matrix.chart }}-${{ matrix.world }}'
eq "the records are checked before difftable runs" "$(jq -c '[.jobs.report.steps[] | .name // empty]' "$j")" '["check records","difftable"]'
eq "nothing in report sources, evals or executes a downloaded file" "$(jq -r '.jobs.report.steps[] | .run // empty' "$j" | grep -Ec '(^|[^[:alnum:]_])(source|eval|exec|chmod|bash|sh|python[0-9]*|node)[[:space:]]+[^|]*results|(^|[[:space:];&|])\.[[:space:]]+[^ ]*results|\./results|\$\(.*results/[^)]*\.sh' || true)" 0
eq "the only thing report runs from the tree is difftable" "$(jq -r '.jobs.report.steps[] | .run // empty' "$j" | grep -c 'go run \./hack/compat-pilot/difftable' )" 2
eq "nothing but report reads exp- artifacts" "$(jq -c '[.jobs | to_entries[] | select(.value.steps | map(.with.pattern // "") | any(startswith("exp-"))) | .key]' "$j")" '["report"]'

# The check step, run for real on fixtures: a record that does not name its
# own artifact's chart and world stops the report.
step_run() { jq -r --arg job "$1" --arg step "$2" '.jobs[$job].steps[] | select(.name == $step) | .run' "$j"; }
check_run="$(step_run report "check records")"
work="$(mktemp -d)"
trap 'rm -f "$j"; rm -rf "$work"' EXIT
mkrec() { # dir chart world
  mkdir -p "$1"; printf '{"chart":"%s","world":"%s","run":1,"obs":{}}\n' "$2" "$3" >"$1/$2.$3.run1.json"
}
fresh() { rm -rf "$work/c"; mkdir -p "$work/c"; }
run_check() { (cd "$work/c" && RUN_ID=77 bash -c "$check_run") >/dev/null 2>&1; }
fresh; mkrec "$work/c/results/exp-77-redis-W1" redis W1; mkrec "$work/c/results/exp-77-redis-fix-W3" redis-fix W3
if run_check; then ok "check records: matching records pass (a chart id with hyphens too)"; else fail "check records: matching records failed"; fi
fresh; mkrec "$work/c/results/exp-77-redis-W1" redis W1; mkrec "$work/c/results/exp-77-valkey-W1" redis W1
if run_check; then fail "check records: another chart's record in an artifact passed"; else ok "check records: a record of another chart fails"; fi
fresh; mkrec "$work/c/results/exp-77-redis-W1" redis W3
if run_check; then fail "check records: a record of another world passed"; else ok "check records: a record of another world fails"; fi
fresh; mkrec "$work/c/results/exp-77-redis-W1" redis W1; echo '[1]' >"$work/c/results/exp-77-redis-W1/x.json"
if run_check; then fail "check records: a json file that is no record passed"; else ok "check records: a json file that is no record fails"; fi
fresh; mkrec "$work/c/results/exp-77-redis-W1" redis W1; ln -s /etc/hosts "$work/c/results/exp-77-redis-W1/link.json"
if run_check; then fail "check records: a symbolic link passed"; else ok "check records: a symbolic link fails"; fi
fresh; mkdir -p "$work/c/results"
if run_check; then fail "check records: no artifacts passed"; else ok "check records: no artifacts fails"; fi
fresh; mkrec "$work/c/results/exp-77-redis-W1" redis W1; mkdir -p "$work/c/results/exp-77-redis-W1/debug"; echo 'not json' >"$work/c/results/exp-77-redis-W1/debug/pods.json"
if run_check; then ok "check records: results/*/debug is dropped, not parsed"; else fail "check records: debug files were parsed"; fi

# C2 (c): the run metadata, written by the guard job from workflow context only
meta_run="$(step_run guard "run metadata")"
eq "the metadata step takes everything through env" "$(jq -c '.jobs.guard.steps[] | select(.name == "run metadata") | .env | keys' "$j")" '["MD_CHARTS_FILE","MD_CHART_IDS","MD_EVENT_NAME","MD_FIXES_FILE","MD_FIX_ID","MD_HEAD_SHA","MD_ISTIO","MD_MODE","MD_RUN_ATTEMPT","MD_RUN_ID","MD_WORKFLOW_REF"]'
eq "the metadata artifact is named by run id and attempt" "$(jq -r '.jobs.guard.steps[] | select((.uses // "") | startswith("actions/upload-artifact")) | [.with.name, .with.path, .with["if-no-files-found"]] | join(" ")' "$j")" 'run-metadata-${{ github.run_id }}-${{ github.run_attempt }} run-metadata/run-metadata.json error'
eq "only the guard job writes run metadata (no other job has such a step or artifact)" "$(jq -c '[.jobs | to_entries[] | select(.value.steps | map((.name // "") == "run metadata" or ((.with.name // "") | startswith("run-metadata"))) | any) | .key]' "$j")" '["guard"]'
eq "no job that runs a chart needs or uploads run metadata" "$(jq -c '[.jobs.observe, .jobs.qualify, .jobs.report, .jobs.select | [.steps[] | (.with.name // "") | select(startswith("run-metadata"))] | length] | add' "$j")" 0
eq "the metadata step reads no result, record or plan file" "$(grep -c 'results\|plan/\|charts\.json"' <<<"$meta_run" || true)" 0
fresh
(
  cd "$work/c"
  git init -q . && git config user.email t@t && git config user.name t
  mkdir -p hack/compat-exp && echo '[{"id":"a"}]' >hack/compat-exp/acceptance-charts.json && echo '[]' >hack/compat-exp/charts-exp.json
  git add . && git commit -q -m c && git tag compat-exp-freeze
) >/dev/null 2>&1
meta() { # mode charts_file fix_id
  (cd "$work/c" && MD_MODE="$1" MD_ISTIO=1.31.3 MD_CHARTS_FILE="$2" MD_FIX_ID="$3" MD_FIXES_FILE=hack/compat-exp/fixes/f.json MD_CHART_IDS="a b" MD_HEAD_SHA=abc123 \
    MD_WORKFLOW_REF=o/r/.github/workflows/compat-exp.yml@refs/heads/main MD_EVENT_NAME=workflow_dispatch MD_RUN_ID=77 MD_RUN_ATTEMPT=2 bash -c "$meta_run" >/dev/null 2>&1 && cat run-metadata/run-metadata.json)
}
head_tag="$(git -C "$work/c" rev-parse HEAD)"
want_sha="$(sha256sum "$work/c/hack/compat-exp/acceptance-charts.json" | cut -d' ' -f1)"
eq "metadata, acceptance: exact fields" "$(meta acceptance "" "" | jq -c --arg sha "$want_sha" --arg tag "$head_tag" '. == {schema:1, mode:"acceptance", istio:"1.31.3", chartsFile:"hack/compat-exp/acceptance-charts.json", chartsSha256:$sha, fixesFile:"hack/compat-exp/fixes/f.json", chartIds:"a b", fixId:"", headSha:"abc123", freezeTagSha:$tag, workflowRef:"o/r/.github/workflows/compat-exp.yml@refs/heads/main", eventName:"workflow_dispatch", runId:77, runAttempt:2}')" true
eq "metadata, certify: default charts file and the fix id" "$(meta certify "" fix-a | jq -c '[.chartsFile, .fixId, .mode]')" '["hack/compat-exp/charts-exp.json","fix-a","certify"]'
eq "metadata has exactly the fourteen fields analyze.sh reads" "$(meta observe "" "" | jq -c 'keys')" '["chartIds","chartsFile","chartsSha256","eventName","fixId","fixesFile","freezeTagSha","headSha","istio","mode","runAttempt","runId","schema","workflowRef"]'
git -C "$work/c" tag -d compat-exp-freeze >/dev/null
eq "metadata: freezeTagSha is empty before the tag exists" "$(meta acceptance "" "" | jq -r '.freezeTagSha')" ""

# the workflow is in the guard's frozen list, and the guard exists
eq "the workflow is a frozen file" "$(grep -c '^  \.github/workflows/compat-exp\.yml$' "$guard")" 1

# the pilots are untouched by this change (their text is not edited here)
for f in compat-pilot.yml compat-pilot-2.yml; do
  if git -C "$repo" diff --quiet origin/main -- ".github/workflows/$f" 2>/dev/null; then ok "$f unchanged against origin/main"; else
    git -C "$repo" rev-parse -q --verify origin/main >/dev/null 2>&1 && fail "$f differs from origin/main" || ok "$f: no origin/main to compare, skipped"
  fi
done

if command -v actionlint >/dev/null 2>&1; then
  if actionlint "$wf"; then ok "actionlint"; else fail "actionlint"; fi
else
  echo "skip: actionlint is not installed (not installing it)"
fi

if [[ $fails -ne 0 ]]; then echo "$fails failure(s)" >&2; exit 1; fi
echo "all workflow tests passed"
