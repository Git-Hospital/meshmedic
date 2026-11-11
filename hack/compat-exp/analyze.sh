#!/usr/bin/env bash
#
# The analysis procedure (docs/compat-experiment.md sections 1, 2 and 7) as one
# command, for a maintainer's machine after the workflow runs have finished:
# check that the analysis is bound to the freeze, download what the runs stored
# (read only), join it, check the ledger, run the static checks and crossrun, and
# write the result files. Nothing here runs on Actions.
#
# Usage:
#   hack/compat-exp/analyze.sh --out DIR --run1 ID [--run2 ID] [--cert ID]... [--recheck ID]
#                              [--repo OWNER/REPO] [--charts FILE] [--ledger FILE]
#                              [--replacements FILE]
#
#   --out DIR     where everything goes; must not exist or be empty
#   --run1, --run2  workflow run ids of the two observe runs (run 2 at least 12 h
#                 after run 1 ended; crossrun stops with an error otherwise). With
#                 --run1 only the result is for the futility check after run 1: cells
#                 wait for run 2, no region is final, the output says "run 1 only"
#   --cert ID     a certify run id; repeat for each (a fix needs two)
#   --recheck ID  the recheck run id
#   --repo        OWNER/REPO for gh (default: gh's own, from the git remote)
#   --charts      the frozen chart list (default hack/compat-exp/charts-exp.json);
#                 must be a tracked, committed file of this repository that names
#                 its qualification run (select.sh writes it); the acceptance list
#                 is refused
#   --ledger      default hack/compat-exp/ledger.json (tracked, committed)
#   --replacements  default hack/compat-exp/replacements.json (tracked, committed)
#
# Run ids are digits only. gh is used for `gh run view` and `gh run download` only.
#
# What the analysis is bound to (every check stops the script; nothing is skipped):
#  1. The tag compat-exp-freeze exists locally, HEAD is the tag or a descendant, every
#     frozen instrument file is byte-identical to the tag's (guard.sh's own check,
#     taken from the tag's copy of guard.sh: `guard.sh freeze`), also in the working
#     tree, and the data files read (the chart list, ledger.json, replacements.json,
#     fixes/, ignore/, patches/) have no uncommitted change.
#  2. The tools (crossrun, ledger, staticcheck) are built from the checked tree. The
#     variables CROSSRUN, LEDGER, STATICCHECK, STATIC_CAUSES and STATIC_WORKFLOW are
#     refused unless MMC_ANALYZE_TEST=1; with it every JSON output carries
#     "testMode": true, the markdown result starts with a line saying it is not
#     valid, and NOT-VALID-TEST-MODE.txt is written. The tests use that mode.
#  3. Every run id (qualification, run 1, run 2, certification, recheck) is checked
#     against gh and its own metadata: its head commit (`headSha`) is the tag commit
#     or a descendant whose frozen files equal the tag's, it ran the workflow
#     compat-exp.yml on workflow_dispatch, it completed, its mode fits its role (run 1
#     and run 2: observe; certification: certify; recheck: recheck; the qualification
#     run named by the chart list: qualify) and its Istio version is the pinned one
#     for that role (ISTIO_PRIMARY of the tag's workflow, except recheck: the second
#     option of the istio input, decided at the freeze).
#     gh run view --json gives headSha, workflowName, event, status, attempt and the
#     times; it does not give the dispatch inputs. Mode and Istio version therefore
#     come from the run metadata, which the workflow's guard job (the only writer; it
#     never runs a chart) uploads as its own artifact run-metadata-ID-ATTEMPT, one file
#     run-metadata.json: schema (1), mode, istio, chartsFile, chartsSha256, fixesFile,
#     chartIds, fixId, headSha, freezeTagSha, workflowRef, eventName, runId, runAttempt.
#     The file is only an artifact, so the script downloads the artifact of every
#     attempt (attempt 1 must exist; a rerun of failed jobs does not run the guard job
#     again, so a later attempt may have none), cross-checks each file against `gh run
#     view` (run id, headSha, workflow name against the workflow ref, event), and stops
#     unless all files agree on everything but runAttempt. It also checks freezeTagSha
#     against the tag and, for the observe-type roles, chartsFile and chartsSha256
#     against the chart list analysed, and istio against the records' environment.
#     Layout of what is downloaded. The report artifact holds report-ID.json, table.md and
#     results/ with one directory per observe artifact, exp-ID-CHART-WORLD/, each holding
#     CHART.WORLD.runN.json (N 1 to 3): the chart is one the frozen list or a committed fix
#     entry names, the world W0, W1 or W3, and each record says the chart, world and run of
#     its directory and file name. A flat results/ (the old layout), a missing or empty one, an
#     extra or unexpected file, and zero records for a report chart or (runs 1 and 2) for a
#     chart of the frozen list stop the script. Before anything reads a download the whole
#     download is refused if it holds a symbolic link, anything but regular files and
#     directories, a name outside [A-Za-z0-9._-], a name starting with a dot or containing "..";
#     the qualification artifact holds only out/charts-exp.json and qual/ID.qualify.json.
#  4. The sample is not choosable after data exists: the qualification run's records
#     (artifact charts-exp-ID, qual/) are downloaded, select.sh is run on them and on the
#     committed candidates.json, and the result must be byte-identical to the chart list.
#  5. Times, conservatively against reruns: the end of a run is the latest `completedAt`
#     of any job in any attempt (gh run view ID --attempt N --json jobs, for every
#     attempt up to the `attempt` field), the start is the earliest `startedAt` of any
#     job or attempt. A run that is not completed is refused.
#  6. The result records the freeze tag commit, HEAD, every run id with headSha, mode,
#     Istio version, start and end, and the sha256 of the chart list, ledger.json,
#     causes.json and replacements.json that were read (DIR/provenance.json, and in
#     result.json under "provenance").
#
# Steps, in order, each stopping the script when it fails: 1 and 2 above; the chart
# list is a frozen experiment list; 3 and the downloads for every run id; 4; the
# ledger check (DIR/ledger-check.txt); static.sh over the charts the reports hold
# (DIR/static.json); DIR/inputs.json, crossrun's manifest; crossrun: DIR/result.json
# and DIR/result.md.
#
# Needs: git, gh, jq, go (unless the test overrides are on), helm (static.sh), and
# what static.sh reads (MMC_STATE, STATIC_ALLOW_HELM_MISMATCH: recorded in
# static.json.meta.json).
#
# Exit codes: 0 done, 1 a step failed (crossrun exits 1 when the ledger and the
# reports disagree or run 2 started too early), 2 usage or a refused input.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
tag=compat-exp-freeze

die() { echo "analyze.sh: $*" >&2; exit "${2:-1}"; }
usage() { sed -n '/^# Usage:/,/^# Run ids/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2; exit 2; }

out="" run1="" run2="" recheck="" repo=""
charts="$root/hack/compat-exp/charts-exp.json" ledger_file="$root/hack/compat-exp/ledger.json"
replacements_file="$root/hack/compat-exp/replacements.json"
certs=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) out="${2:-}"; shift 2 ;;
    --run1) run1="${2:-}"; shift 2 ;;
    --run2) run2="${2:-}"; shift 2 ;;
    --cert) certs+=("${2:-}"); shift 2 ;;
    --recheck) recheck="${2:-}"; shift 2 ;;
    --repo) repo="${2:-}"; shift 2 ;;
    --charts) charts="${2:-}"; shift 2 ;;
    --ledger) ledger_file="${2:-}"; shift 2 ;;
    --replacements) replacements_file="${2:-}"; shift 2 ;;
    -h | --help) usage ;;
    *) echo "analyze.sh: unknown argument '$1'" >&2; usage ;;
  esac
done

[[ -n "$out" && -n "$run1" ]] || usage
isid() { [[ "$1" =~ ^[0-9]{1,20}$ ]]; }
for v in "$run1" ${run2:+"$run2"} ${recheck:+"$recheck"} ${certs[@]+"${certs[@]}"}; do
  isid "$v" || die "run id '$v' is not digits only" 2
done
[[ -z "$repo" || "$repo" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] || die "--repo must be OWNER/REPO" 2
[[ -f "$charts" ]] || die "no chart list $charts" 2
[[ -f "$ledger_file" ]] || die "no ledger $ledger_file" 2
[[ -f "$replacements_file" ]] || die "no replacements file $replacements_file (commit one, {\"schema\":1,\"replacements\":[]} when nothing was replaced)" 2
if [[ -e "$out" ]]; then
  [[ -d "$out" && -z "$(ls -A "$out")" ]] || die "$out exists and is not an empty directory" 2
fi
for t in gh jq git; do command -v "$t" >/dev/null || die "$t is needed" 2; done

ids=("$run1")
[[ -z "$run2" ]] || ids+=("$run2")
ids+=(${certs[@]+"${certs[@]}"})
[[ -z "$recheck" ]] || ids+=("$recheck")
[[ "$(printf '%s\n' "${ids[@]}" | sort | uniq -d | wc -l | tr -d ' ')" == 0 ]] || die "a run id is given twice" 2

# --- 2. the tools come from the checked tree ----------------------------------------

test_mode=0
case "${MMC_ANALYZE_TEST:-}" in
  1) test_mode=1 ;;
  "") ;;
  *) die "MMC_ANALYZE_TEST must be 1 or unset" 2 ;;
esac
if [[ $test_mode -eq 0 ]]; then
  for v in CROSSRUN LEDGER STATICCHECK STATIC_CAUSES STATIC_WORKFLOW; do
    [[ -z "${!v:-}" ]] || die "$v is set: tool and file overrides are refused unless MMC_ANALYZE_TEST=1 (the tools are built from the checked tree)" 2
  done
fi

mkdir -p "$out" || die "cannot create $out" 2
out="$(cd "$out" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
git_() { git -C "$root" "$@"; }
gh_() { if [[ -n "$repo" ]]; then gh "$@" -R "$repo"; else gh "$@"; fi; }

# --- 1. the freeze, and the data files -------------------------------------------------

git_ rev-parse -q --verify "refs/tags/$tag^{commit}" >/dev/null || die "tag $tag does not exist locally (git fetch --tags); the analysis is bound to the freeze"
tag_sha="$(git_ rev-parse "refs/tags/$tag^{commit}")"
head_sha="$(git_ rev-parse HEAD)"
# guard.sh as the tag has it: the definition of "frozen" cannot be edited after the tag.
git_ show "$tag_sha:hack/compat-exp/guard.sh" >"$tmp/guard-at-tag.sh" 2>/dev/null || die "hack/compat-exp/guard.sh is not in tag $tag"
freeze_check() { # [SHA]
  env -i PATH="$PATH" HOME="${HOME:-/}" GUARD_ROOT="$root" FREEZE_TAG="$tag" bash "$tmp/guard-at-tag.sh" freeze "$@" 2>"$tmp/guard.err" ||
    die "$(tr '\n' ' ' <"$tmp/guard.err")"
}
freeze_check

relpath() { # PATH: the path inside the repository, or die
  local d b top rel
  d="$(cd "$(dirname "$1")" 2>/dev/null && pwd -P)" || die "cannot resolve $1" 2
  b="$(basename "$1")"
  top="$(cd "$root" && pwd -P)"
  [[ "$d/$b" == "$top"/* ]] || die "$1 is outside the repository: the data files must be committed" 2
  rel="$d/$b"
  echo "${rel#"$top"/}"
}
charts_rel="$(relpath "$charts")" || exit $?
ledger_rel="$(relpath "$ledger_file")" || exit $?
repl_rel="$(relpath "$replacements_file")" || exit $?
for f in "$charts_rel" "$ledger_rel" "$repl_rel"; do
  git_ ls-files --error-unmatch -- "$f" >/dev/null 2>&1 || die "$f is not a tracked file; commit it before the analysis"
done
dirty="$(git_ status --porcelain --untracked-files=all -- "$charts_rel" "$ledger_rel" "$repl_rel" \
  hack/compat-exp/fixes hack/compat-exp/ignore hack/compat-exp/patches hack/compat-exp/causes.json | head -5 | tr '\n' ' ')"
[[ -z "$dirty" ]] || die "uncommitted changes to files the analysis reads: $dirty"

sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
charts_sha="$(sha "$charts")"
ledger_sha="$(sha "$ledger_file")"
causes_sha="$(sha "$root/hack/compat-exp/causes.json")"
repl_sha="$(sha "$replacements_file")"

# --- the chart list is a frozen experiment list ------------------------------------------

jq -e '(.charts | type == "array") and (.reserves | type == "array") and (.sha256 | type == "string")' "$charts" >/dev/null 2>&1 ||
  die "$charts_rel is not a charts-exp.json (charts, reserves, sha256)" 2
qual_run="$(jq -r '.qualification.run // empty' "$charts")"
isid "$qual_run" || die "$charts_rel names no qualification run id: it is not a frozen experiment list (the acceptance list and candidate lists cannot be analysed)" 2
stored="$(jq -r .sha256 "$charts")"
actual="$(jq -S -c 'del(.sha256)' "$charts" | { if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi; } | cut -d' ' -f1)"
[[ "$stored" == "$actual" ]] || die "$charts_rel: stored sha256 does not match its content"
jq -e 'all((.charts + .reserves)[]; .qualified == true)' "$charts" >/dev/null || die "$charts_rel lists a chart that is not qualified"

# --- the pinned versions (the tag's workflow) ---------------------------------------------------

git_ show "$tag_sha:.github/workflows/compat-exp.yml" >"$tmp/workflow.yml" || die ".github/workflows/compat-exp.yml is not in tag $tag"
primary="$(sed -n 's/^[[:space:]]*ISTIO_PRIMARY:[[:space:]]*"\{0,1\}\([0-9][0-9.]*\)"\{0,1\}.*$/\1/p' "$tmp/workflow.yml" | head -1)"
[[ "$primary" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "no ISTIO_PRIMARY in the tag's workflow"
recheck_version="$(awk '
  /^[a-z]/ { in_i = 0; in_o = 0 }
  /^      istio:/ { in_i = 1; next }
  in_i && /^      [a-z_]+:/ { in_i = 0 }
  in_i && /options:/ { in_o = 1; next }
  in_o && /^[[:space:]]*- / { n++; v = $0; sub(/^[[:space:]]*- /, "", v); gsub(/"/, "", v); if (n == 2) { print v; exit } }
' "$tmp/workflow.yml")"
if [[ -n "$recheck" ]]; then
  [[ "$recheck_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "the tag's workflow offers no decided recheck version ('$recheck_version')"
fi

# --- builds -----------------------------------------------------------------------------------------

build() { # ENVVAR PACKAGE: a binary path; built from the tree unless a test override is given
  local var="$1" pkg="$2" bin
  bin="${!var:-}"
  if [[ -z "$bin" ]]; then
    command -v go >/dev/null || die "go is needed" 2
    bin="$tmp/$(basename "$pkg")"
    (cd "$root" && go build -o "$bin" "$pkg") || die "$pkg did not build"
  fi
  echo "$bin"
}
crossrun="$(build CROSSRUN ./hack/compat-exp/crossrun)" || exit $?
ledger_bin="$(build LEDGER ./hack/compat-exp/ledger/cmd/ledger)" || exit $?
STATICCHECK="$(build STATICCHECK ./hack/compat-exp/staticcheck)" || exit $?
export STATICCHECK
[[ $test_mode -eq 0 ]] || export MMC_ANALYZE_TEST=1   # static.sh reads its own overrides only behind this switch

# --- what a download may hold -------------------------------------------------------------------

# check_download DIR LABEL: right after every download, before anything reads it. The
# whole download is refused if it holds a symbolic link, anything that is not a regular
# file or a directory, or a name (any path component) that does not fit [A-Za-z0-9._-],
# starts with a dot or contains "..".
check_download() {
  local dir="$1" label="$2" bad
  bad="$(find "$dir" -type l -print -quit)"
  [[ -z "$bad" ]] || die "$label: the download holds a symbolic link ($(printf '%s' "$bad" | tr -c '[:print:]' '?')); refused"
  bad="$(find "$dir" ! -type f ! -type d -print -quit)"
  [[ -z "$bad" ]] || die "$label: the download holds something that is not a regular file ($(printf '%s' "$bad" | tr -c '[:print:]' '?')); refused"
  bad="$(LC_ALL=C find "$dir" -mindepth 1 \( -name '.*' -o -name '*..*' -o -name '*[!A-Za-z0-9._-]*' \) -print -quit)"
  [[ -z "$bad" ]] || die "$label: the download holds a file name outside the strict pattern ([A-Za-z0-9._-], no leading dot, no ..): $(printf '%s' "$bad" | tr -c '[:print:]' '?'); refused"
}

# The chart ids the frozen list predicts: charts and reserves, and the fix entries of the
# committed fixes files (certification runs record those under the fix id).
allowed_ids="$tmp/allowed-ids"
{
  jq -r '(.charts + .reserves)[].id' "$charts"
  for f in "$root"/hack/compat-exp/fixes/*.json; do [[ -f "$f" ]] && jq -r '.[]?.id' "$f"; done
} 2>/dev/null | sort -u >"$allowed_ids"
[[ -s "$allowed_ids" ]] || die "$charts_rel lists no chart"

# check_toplevel ROLE ID DIR: the artifact holds only what the workflow's upload step names.
check_toplevel() {
  local role="$1" id="$2" dir="$3" want e
  shopt -s nullglob
  if [[ "$role" == qualification ]]; then want="out qual"; else want="report-$id.json table.md results"; fi
  for e in "$dir"/*; do
    e="${e##*/}"
    [[ " $want " == *" $e "* ]] || die "run $id: its artifact holds an unexpected top-level entry '$e' (expected: $want)"
  done
  if [[ "$role" == qualification ]]; then
    [[ -d "$dir/out" && -d "$dir/qual" ]] || die "run $id: its artifact holds no out/ and qual/"
    [[ "$(find "$dir/out" -type f | wc -l | tr -d ' ')" == 1 && -f "$dir/out/charts-exp.json" && -z "$(find "$dir/out" -mindepth 1 -type d -print -quit)" ]] ||
      die "run $id: out/ must hold charts-exp.json and nothing else"
    local f n
    while IFS= read -r f; do
      n="$(basename "$f")"
      [[ -f "$f" && "$n" == *.qualify.json && "$(dirname "$f")" == "$dir/qual" ]] || die "run $id: unexpected entry in qual/: ${f#"$dir"/}"
      [[ "$(jq -r .id "$f" 2>/dev/null)" == "${n%.qualify.json}" ]] || die "run $id: qual/$n does not say it is the record of ${n%.qualify.json}"
    done < <(find "$dir/qual" -mindepth 1)
  fi
}

# check_records ROLE ID DIR: results/ has the layout of the workflow's report artifact:
# exp-<run id>-<chart>-<world>/<chart>.<world>.run<1-3>.json, charts the frozen list (or a
# committed fix entry) names, worlds W0 W1 W3, every record saying the same chart, world and
# run as its directory and file name. Zero records for a chart of the frozen list (the
# report's charts, and for run 1 and run 2 every primary chart) is an error.
check_records() {
  local role="$1" id="$2" dir="$3/results" e m chart world f bad c files
  shopt -s nullglob
  [[ -d "$dir" ]] || die "run $id: no results/ (observation records) in its artifact"
  : >"$tmp/recdirs"
  for e in "$dir"/*; do
    e="${e##*/}"
    [[ -d "$dir/$e" ]] || die "run $id: results/$e is not a directory; results/ must hold exp-$id-<chart>-<world>/ directories (a flat directory of records is not the layout of the report artifact)"
    if ! [[ "$e" =~ ^exp-$id-([a-z0-9][a-z0-9-]*)-(W[013])$ ]]; then
      die "run $id: results/$e is not named exp-$id-<chart>-<world> with a world of W0, W1, W3"
    fi
    chart="${BASH_REMATCH[1]}" world="${BASH_REMATCH[2]}"
    grep -qxF -- "$chart" "$allowed_ids" || die "run $id: results/$e is a chart the frozen list and the fix entries do not predict ($chart)"
    echo "$e" >>"$tmp/recdirs"
    files=("$dir/$e"/*)
    [[ ${#files[@]} -gt 0 ]] || die "run $id: results/$e holds no record"
    for f in "${files[@]}"; do
      f="${f##*/}"
      [[ "$f" =~ ^$chart\.$world\.run[1-3]\.json$ ]] || die "run $id: unexpected file results/$e/$f (only $chart.$world.run<1-3>.json)"
    done
  done
  [[ -s "$tmp/recdirs" ]] || die "run $id: results/ holds no records at all (zero records for the charts of the frozen list)"
  bad="$(find "$dir" -mindepth 2 -type f -exec jq -r '
      (input_filename | capture("results/exp-[0-9]+-(?<c>[a-z0-9-]+)-(?<w>W[0-9])/[^/]+\\.run(?<n>[0-9])\\.json$")) as $m
      | select((.chart? // "") != $m.c or (.world? // "") != $m.w or ((.run? // "") | tostring) != $m.n) | input_filename' {} + 2>&1 | head -3)"
  [[ -z "$bad" ]] || die "run $id: a record does not say the chart, world and run of its directory and file name: $bad"
  while IFS= read -r c; do
    grep -Eq "^exp-$id-$c-W[013]\$" "$tmp/recdirs" || die "run $id: the report holds chart $c but results/ holds no record of it"
  done < <(jq -r '.charts[].chart' "$3/report-$id.json")
  if [[ "$role" == run1 || "$role" == run2 ]]; then
    while IFS= read -r c; do
      grep -Eq "^exp-$id-$c-W[013]\$" "$tmp/recdirs" || die "run $id: no record of the frozen chart $c (zero records for a chart of the list)"
    done < <(jq -r '.charts[].id' "$charts")
  fi
}

# --- 3. every run id --------------------------------------------------------------------------------------

rfc3339z='^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$'
prov_runs='[]'      # for provenance.json
times_of=()          # "id started ended" lines

# fetch_run ROLE ID ARTIFACT WANT_MODE WANT_ISTIO: checks, downloads into $out/run-ID
fetch_run() {
  local role="$1" id="$2" artifact="$3" want_mode="$4" want_istio="$5" view head attempt n j d
  d="$out/run-$id"
  mkdir -p "$d"
  view="$(gh_ run view "$id" --json attempt,conclusion,databaseId,event,headSha,startedAt,status,updatedAt,workflowName 2>"$tmp/view.log")" ||
    die "gh run view $id failed: $(tail -2 "$tmp/view.log" | tr '\n' ' ')"
  jq -e . >/dev/null 2>&1 <<<"$view" || die "run $id: gh run view did not return JSON"
  [[ "$(jq -r .status <<<"$view")" == completed ]] || die "run $id is not completed (status $(jq -r .status <<<"$view"))"
  [[ "$(jq -r '.databaseId | tostring' <<<"$view")" == "$id" ]] || die "run $id: gh returned another run"
  [[ "$(jq -r .event <<<"$view")" == workflow_dispatch ]] || die "run $id was triggered by '$(jq -r .event <<<"$view")', not workflow_dispatch"
  [[ "$(jq -r .workflowName <<<"$view")" == compat-exp ]] || die "run $id belongs to the workflow '$(jq -r .workflowName <<<"$view")', not compat-exp"
  head="$(jq -r .headSha <<<"$view")"
  [[ "$head" =~ ^[0-9a-f]{40}$ ]] || die "run $id: headSha '$head' is not a commit sha"
  freeze_check "$head" || exit $?
  attempt="$(jq -r '.attempt // 1' <<<"$view")"
  [[ "$attempt" =~ ^[0-9]{1,3}$ ]] || die "run $id: attempt '$attempt' is not a number"

  gh_ run download "$id" -n "$artifact" -D "$d" >"$tmp/dl.log" 2>&1 ||
    die "gh run download $id failed: $(tail -2 "$tmp/dl.log" | tr '\n' ' ')"
  check_download "$d" "run $id"
  check_toplevel "$role" "$id" "$d"

  # the run metadata: one artifact per attempt, written by the guard job only
  local meta mode istio recs_istio m ref first="" nm=0 mw
  mw='.schema == 1 and (.runId | type == "number") and (.runAttempt | type == "number")
      and (.headSha | type == "string") and (.eventName | type == "string")
      and (.workflowRef | type == "string") and (.freezeTagSha | type == "string")
      and (.chartsSha256 | type == "string" and test("^[0-9a-f]{64}$"))
      and all(.mode, .istio, .chartsFile, .fixesFile, .chartIds, .fixId; type == "string")
      and (keys == ["chartIds","chartsFile","chartsSha256","eventName","fixId","fixesFile","freezeTagSha","headSha","istio","mode","runAttempt","runId","schema","workflowRef"])'
  for ((n = 1; n <= attempt; n++)); do
    m="$d/run-metadata-$n"
    mkdir -p "$m"
    if ! gh_ run download "$id" -n "run-metadata-$id-$n" -D "$m" >"$tmp/dl.log" 2>&1; then
      [[ $n -gt 1 ]] || die "run $id: no run-metadata-$id-1 artifact (the guard job uploads it; fields: schema 1, mode, istio, chartsFile, chartsSha256, fixesFile, chartIds, fixId, headSha, freezeTagSha, workflowRef, eventName, runId, runAttempt): $(tail -2 "$tmp/dl.log" | tr '\n' ' ')"
      rm -rf "$m"
      continue
    fi
    check_download "$m" "run $id (run-metadata-$id-$n)"
    meta="$m/run-metadata.json"
    [[ -f "$meta" && ! -L "$meta" && -z "$(find "$m" -mindepth 1 ! -name run-metadata.json -print -quit)" ]] ||
      die "run $id: run-metadata-$id-$n must hold the one file run-metadata.json"
    jq -e "$mw" "$meta" >/dev/null 2>&1 || die "run $id: run-metadata-$id-$n/run-metadata.json does not have the fields and types of the guard job's file"
    jq -e --arg id "$id" --argjson n "$n" --arg head "$head" --arg ev "$(jq -r .event <<<"$view")" \
      '(.runId | tostring) == $id and .runAttempt == $n and .headSha == $head and .eventName == $ev and .eventName == "workflow_dispatch"' "$meta" >/dev/null ||
      die "run $id: run-metadata-$id-$n does not describe this run (runId, runAttempt, headSha and eventName must equal what gh run view reports; the event must be workflow_dispatch)"
    ref="$(jq -r .workflowRef "$meta")"
    [[ "$ref" == */.github/workflows/"$(jq -r .workflowName <<<"$view")".yml@* ]] ||
      die "run $id: run-metadata-$id-$n names the workflow '$ref', but gh run view reports the workflow '$(jq -r .workflowName <<<"$view")'"
    jq -e --arg tag "$tag_sha" '.freezeTagSha == $tag' "$meta" >/dev/null ||
      die "run $id: run-metadata-$id-$n says the freeze tag was at '$(jq -r .freezeTagSha "$meta")', not at $tag_sha"
    if [[ -z "$first" ]]; then first="$meta"; elif ! cmp -s <(jq -S -c 'del(.runAttempt)' "$first") <(jq -S -c 'del(.runAttempt)' "$meta"); then
      die "run $id: the run metadata of attempt $n differs from attempt 1 in more than runAttempt"
    fi
    nm=$((nm + 1))
  done
  meta="$first"
  mode="$(jq -r .mode "$meta")"
  istio="$(jq -r .istio "$meta")"
  [[ "$mode" == "$want_mode" ]] || die "run $id was dispatched with mode '$mode', but it is given as $role, which needs mode $want_mode"
  [[ "$istio" == "$want_istio" ]] || die "run $id ran Istio $istio, but the pinned version for $role is $want_istio"
  if [[ "$role" != qualification ]]; then
    [[ "$(jq -r .chartsFile "$meta")" == "$charts_rel" ]] || die "run $id used the chart list '$(jq -r .chartsFile "$meta")', not $charts_rel"
    [[ "$(jq -r .chartsSha256 "$meta")" == "$charts_sha" ]] || die "run $id used a chart list with another sha256 than the one analysed ($charts_rel)"
    jq -e '.schema == 1 and (.charts | type == "array")' "$d/report-$id.json" >/dev/null 2>&1 || die "run $id: no usable report-$id.json in its artifact"
    check_records "$role" "$id" "$d"
    recs_istio="$(find "$d/results" -mindepth 2 -maxdepth 2 -name '*.json' -exec jq -r '.environment.istio // empty' {} + | sort -u | tr '\n' ' ')"
    [[ -z "${recs_istio// /}" || "${recs_istio% }" == "$istio" ]] || die "run $id: its records say Istio '${recs_istio% }', its metadata says $istio"
  fi

  # times: all attempts, conservatively
  : >"$tmp/attempts.jsonl"
  for ((n = 1; n <= attempt; n++)); do
    j="$(gh_ run view "$id" --attempt "$n" --json status,startedAt,updatedAt,jobs 2>"$tmp/view.log")" ||
      die "gh run view $id --attempt $n failed: $(tail -2 "$tmp/view.log" | tr '\n' ' ')"
    [[ "$(jq -r .status <<<"$j")" == completed ]] || die "run $id attempt $n is not completed"
    jq -c . <<<"$j" >>"$tmp/attempts.jsonl" || die "run $id attempt $n: not JSON"
  done
  local se started ended
  se="$(jq -s -r --arg re "$rfc3339z" '
    def real: select(type == "string" and test($re) and . > "2000");
    ([.[] | (.startedAt | real), (.jobs[]? | .startedAt | real)] | min) as $s
    | ([.[] | (.jobs[]? | .completedAt | real)] | max) as $e
    | if $s == null or $e == null then "none" else "\($s) \($e)" end' "$tmp/attempts.jsonl")"
  [[ "$se" != none ]] || die "run $id: gh reports no job start and completion times (need UTC RFC 3339 times in jobs[].startedAt and completedAt)"
  read -r started ended <<<"$se"
  times_of+=("$id $started $ended")

  prov_runs="$(jq -c --arg role "$role" --arg id "$id" --arg head "$head" --arg mode "$mode" --arg istio "$istio" \
    --arg s "$started" --arg e "$ended" --argjson attempts "$attempt" --arg conclusion "$(jq -r .conclusion <<<"$view")" \
    '. + [{role: $role, id: $id, headSha: $head, mode: $mode, istio: $istio, started: $s, ended: $e, attempts: $attempts, conclusion: $conclusion}]' <<<"$prov_runs")"
}

# the qualification run first: the sample is chosen from its records
fetch_run qualification "$qual_run" "charts-exp-$qual_run" qualify "$primary" || exit $?
[[ -d "$out/run-$qual_run/qual" ]] || die "run $qual_run: no qual/ (qualification records) in its artifact charts-exp-$qual_run"
bash "$here/select.sh" "$root/hack/compat-exp/candidates.json" "$out/run-$qual_run/qual" "$tmp/charts-reselected.json" "$qual_run" >"$tmp/select.log" 2>&1 ||
  die "select.sh on the qualification run's records failed: $(tail -2 "$tmp/select.log" | tr '\n' ' ')"
cmp -s "$tmp/charts-reselected.json" "$charts" ||
  die "the chart list $charts_rel is not what select.sh makes from the records of qualification run $qual_run and the frozen candidates.json: the sample does not reproduce, so it is not the frozen one"

fetch_run run1 "$run1" "compat-exp-report-$run1" observe "$primary" || exit $?
[[ -z "$run2" ]] || fetch_run run2 "$run2" "compat-exp-report-$run2" observe "$primary" || exit $?
for id in ${certs[@]+"${certs[@]}"}; do fetch_run certification "$id" "compat-exp-report-$id" certify "$primary" || exit $?; done
[[ -z "$recheck" ]] || fetch_run recheck "$recheck" "compat-exp-report-$recheck" recheck "$recheck_version" || exit $?

tm() { # ID started|ended
  local l a b c
  for l in "${times_of[@]}"; do
    read -r a b c <<<"$l"
    if [[ "$a" == "$1" ]]; then [[ "$2" == started ]] && echo "$b" || echo "$c"; return; fi
  done
}

# --- 4. ledger check ------------------------------------------------------------------------------------------

"$ledger_bin" check -ledger "$ledger_file" -causes "$root/hack/compat-exp/causes.json" >"$out/ledger-check.txt" 2>&1 || {
  cat "$out/ledger-check.txt" >&2
  die "ledger check failed (see $out/ledger-check.txt)"
}

# --- 5. static checks -------------------------------------------------------------------------------------------

# The charts the reports hold: base charts of every observe run, not fix entries.
chart_ids=()
while IFS= read -r c; do chart_ids+=("$c"); done < <(
  for id in "$run1" ${run2:+"$run2"}; do jq -r '.charts[] | select((.fixOf // "") == "") | .chart' "$out/run-$id/report-$id.json"; done | sort -u)
[[ ${#chart_ids[@]} -gt 0 ]] || die "the reports hold no charts"
"$here/static.sh" "$charts" "$out/static.json" "${chart_ids[@]}" || die "static.sh failed"

# --- the manifest ---------------------------------------------------------------------------------------------------------

runs='[]'
for id in "$run1" ${run2:+"$run2"}; do
  runs="$(jq -c --arg id "$id" --arg s "$(tm "$id" started)" --arg e "$(tm "$id" ended)" --arg d "$out/run-$id" \
    '. + [{id: $id, report: ($d + "/report-" + $id + ".json"), records: ($d + "/results"), started: $s, ended: $e}]' <<<"$runs")"
done
istio_of() { jq -r --arg id "$1" '.[] | select(.id == $id) | .istio' <<<"$prov_runs"; }
cert_json='[]'
for id in ${certs[@]+"${certs[@]}"}; do
  cert_json="$(jq -c --arg id "$id" --arg d "$out/run-$id" --arg i "$(istio_of "$id")" \
    '. + [{id: $id, report: ($d + "/report-" + $id + ".json"), istio: $i}]' <<<"$cert_json")" || exit $?
done
recheck_json=null
if [[ -n "$recheck" ]]; then
  recheck_json="$(jq -c -n --arg id "$recheck" --arg d "$out/run-$recheck" --arg i "$(istio_of "$recheck")" \
    '{id: $id, report: ($d + "/report-" + $id + ".json"), istio: $i}')" || exit $?
fi
static_meta="$(jq -c '{helmVersion, helmPinned, helmMatchesPinned}' "$out/static.json.meta.json")" || die "static.json.meta.json is unreadable"
provenance="$(jq -c -n --arg tag "$tag" --arg tagsha "$tag_sha" --arg head "$head_sha" --argjson runs "$prov_runs" --argjson static "$static_meta" \
  --arg charts "$charts_rel" --arg chartsSha "$charts_sha" --arg ledger "$ledger_rel" --arg ledgerSha "$ledger_sha" \
  --arg causesSha "$causes_sha" --arg repl "$repl_rel" --arg replSha "$repl_sha" --argjson test "$test_mode" \
  '{freezeTag: $tag, freezeCommit: $tagsha, head: $head, runs: $runs, static: $static,
    files: {chartList: {path: $charts, sha256: $chartsSha}, ledger: {path: $ledger, sha256: $ledgerSha},
            causes: {path: "hack/compat-exp/causes.json", sha256: $causesSha}, replacements: {path: $repl, sha256: $replSha}}}
   + (if $test == 1 then {testMode: true} else {} end)')" || die "could not build the provenance"
echo "$provenance" | jq . >"$out/provenance.json"
jq -n --argjson runs "$runs" --argjson cert "$cert_json" --argjson recheck "$recheck_json" --argjson prov "$provenance" --argjson test "$test_mode" \
  --arg causes "$root/hack/compat-exp/causes.json" --arg ledger "$ledger_file" --arg frame "$charts" --arg static "$out/static.json" --arg repl "$replacements_file" \
  '{runs: $runs, certification: $cert, causes: $causes, ledger: $ledger, frame: $frame, static: $static, replacements: $repl, provenance: $prov}
   + (if $recheck == null then {} else {recheck: $recheck} end)
   + (if $test == 1 then {testMode: true} else {} end)' >"$out/inputs.json" || die "could not write the manifest"
if [[ $test_mode -eq 1 ]]; then
  jq '. + {testMode: true}' "$out/static.json.meta.json" >"$tmp/meta.json" && mv "$tmp/meta.json" "$out/static.json.meta.json"
  echo "This directory was made with MMC_ANALYZE_TEST=1 (tool overrides). Nothing in it is a valid analysis." >"$out/NOT-VALID-TEST-MODE.txt"
fi

# --- 6. crossrun ---------------------------------------------------------------------------------------------------------------

"$crossrun" -inputs "$out/inputs.json" -json "$out/result.json" -md "$out/result.md"
rc=$?
if [[ $rc -ne 0 ]]; then die "crossrun exited $rc (1: the ledger and the reports disagree, or run 2 started under 12 h after run 1 ended, or the sample is incomplete; 2: unreadable input)"; fi

# the files read must be the files hashed
[[ "$(sha "$charts")" == "$charts_sha" && "$(sha "$ledger_file")" == "$ledger_sha" && "$(sha "$root/hack/compat-exp/causes.json")" == "$causes_sha" &&
  "$(sha "$replacements_file")" == "$repl_sha" ]] || die "a file the analysis reads changed while it ran; the result is discarded"

echo "analyze.sh: done: $out/result.json, $out/result.md"
[[ $test_mode -eq 0 ]] || echo "analyze.sh: MMC_ANALYZE_TEST=1: this result is NOT valid" >&2
jq -r 'if .final then "region \(.region.region), N_new \(.metrics.nNew), P_new \(.metrics.pNew), D \(.metrics.d), P_D \(.metrics.pD), S \(.metrics.s)"
       else "run 1 only (no final region), N_max \(.futility.nMax), P_max \(.futility.pMax), stopDrop \(.futility.stopDrop)" end' "$out/result.json" || true
