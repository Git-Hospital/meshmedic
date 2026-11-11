#!/usr/bin/env bash
#
# The analysis procedure (docs/compat-experiment.md sections 1, 2 and 7) as one
# command, for a maintainer's machine after the workflow runs have finished:
# download what the runs stored (read only), join it, check the ledger, run the
# static checks and crossrun, and write the result files. The workflow needs no
# extra permission for this: nothing here runs on Actions.
#
# Usage:
#   hack/compat-exp/analyze.sh --out DIR --run1 ID [--run2 ID] [--cert ID]... [--recheck ID]
#                              [--repo OWNER/REPO] [--charts FILE] [--ledger FILE]
#
#   --out DIR     where everything goes; must not exist or be empty
#   --run1, --run2  workflow run ids of the two observe runs (run 2 at least 12 h
#                 after run 1 ended; crossrun checks the gap from the run times)
#   --cert ID     a certify run id; repeat for each (a fix needs two)
#   --recheck ID  the recheck run id
#   --repo        OWNER/REPO for gh (default: gh's own, from the git remote)
#   --charts      the frozen chart list, crossrun's frame and static.sh's list
#                 (default hack/compat-exp/charts-exp.json)
#   --ledger      default hack/compat-exp/ledger.json
#
# Run ids are digits only. gh is used for `gh run download` and `gh run view`
# only. Per run it downloads the artifact compat-exp-report-ID (report-ID.json,
# table.md, results/ with the observation records) into DIR/run-ID/.
#
# Steps, in order, each stopping the script when it fails:
#   1. download the reports and records of every run id
#   2. start and end time of each observe run (gh run view: startedAt, updatedAt of
#      a completed run) and the Istio version of each certify/recheck run (from its
#      records' environment)
#   3. DIR/inputs.json, crossrun's manifest
#   4. ledger check (hack/compat-exp/ledger), DIR/ledger-check.txt
#   5. static.sh over the charts the reports hold, DIR/static.json
#   6. crossrun: DIR/result.json and DIR/result.md
#
# Environment: CROSSRUN, LEDGER, STATICCHECK (binaries; default: go build into a
# temporary directory), and what static.sh reads (MMC_STATE,
# STATIC_ALLOW_HELM_MISMATCH).
#
# Exit codes: 0 done, 1 a step failed (crossrun exits 1 when the ledger and the
# reports disagree), 2 usage or a refused input.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"

die() { echo "analyze.sh: $*" >&2; exit "${2:-1}"; }
usage() { sed -n '/^# Usage:/,/^# Run ids/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2; exit 2; }

out="" run1="" run2="" recheck="" repo=""
charts="$root/hack/compat-exp/charts-exp.json" ledger_file="$root/hack/compat-exp/ledger.json"
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
if [[ -e "$out" ]]; then
  [[ -d "$out" && -z "$(ls -A "$out")" ]] || die "$out exists and is not an empty directory" 2
fi
for t in gh jq; do command -v "$t" >/dev/null || die "$t is needed" 2; done

mkdir -p "$out" || die "cannot create $out" 2
out="$(cd "$out" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

gh_() { if [[ -n "$repo" ]]; then gh "$@" -R "$repo"; else gh "$@"; fi; }

build() { # ENVVAR PACKAGE: a binary path, built unless given
  local var="$1" pkg="$2" bin="${!1:-}"
  if [[ -z "$bin" ]]; then
    command -v go >/dev/null || die "go (or $var) is needed" 2
    bin="$tmp/$(basename "$pkg")"
    (cd "$root" && go build -o "$bin" "$pkg") || die "$pkg did not build"
  fi
  echo "$bin"
}
crossrun="$(build CROSSRUN ./hack/compat-exp/crossrun)" || exit $?
ledger_bin="$(build LEDGER ./hack/compat-exp/ledger/cmd/ledger)" || exit $?
STATICCHECK="$(build STATICCHECK ./hack/compat-exp/staticcheck)" || exit $?
export STATICCHECK

# --- 1. download ----------------------------------------------------------------

# Everything downloaded is data: it lands in its own directory and is only read
# by jq and crossrun, never executed or sourced.
ids=("$run1")
[[ -z "$run2" ]] || ids+=("$run2")
ids+=(${certs[@]+"${certs[@]}"})
[[ -z "$recheck" ]] || ids+=("$recheck")
[[ "$(printf '%s\n' "${ids[@]}" | sort | uniq -d | wc -l | tr -d ' ')" == 0 ]] || die "a run id is given twice" 2

for id in "${ids[@]}"; do
  d="$out/run-$id"
  mkdir -p "$d"
  gh_ run download "$id" -n "compat-exp-report-$id" -D "$d" >"$tmp/dl.log" 2>&1 ||
    die "gh run download $id failed: $(tail -2 "$tmp/dl.log" | tr '\n' ' ')"
  jq -e '.schema == 1 and (.charts | type == "array")' "$d/report-$id.json" >/dev/null 2>&1 ||
    die "run $id: no usable report-$id.json in its artifact"
  [[ -d "$d/results" ]] || die "run $id: no results/ (observation records) in its artifact"
done

# --- 2. times and versions ----------------------------------------------------------

rfc3339='^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$'
times() { # ID -> "started ended"
  local j
  j="$(gh_ run view "$1" --json status,conclusion,startedAt,updatedAt 2>"$tmp/view.log")" ||
    die "gh run view $1 failed: $(tail -2 "$tmp/view.log" | tr '\n' ' ')"
  [[ "$(jq -r .status <<<"$j")" == completed ]] || die "run $1 is not completed (status $(jq -r .status <<<"$j"))"
  local s e
  s="$(jq -r .startedAt <<<"$j")"
  e="$(jq -r .updatedAt <<<"$j")"
  [[ "$s" =~ $rfc3339 && "$e" =~ $rfc3339 ]] || die "run $1: startedAt/updatedAt are not RFC 3339 times"
  echo "$s $e"
}
istio_of() { # ID: the Istio version in the records' environment, W1 first
  local f v
  for f in "$out/run-$1"/results/*.W1.run1.json "$out/run-$1"/results/*.W3.run1.json; do
    [[ -f "$f" ]] || continue
    v="$(jq -r '.environment.istio // empty' "$f")"
    [[ -n "$v" ]] && { echo "$v"; return 0; }
  done
  die "run $1: no record states the Istio version"
}

# --- 3. the manifest ----------------------------------------------------------------

runs='[]'
for id in "$run1" ${run2:+"$run2"}; do
  read -r s e < <(times "$id") || exit $?
  runs="$(jq -c --arg id "$id" --arg s "$s" --arg e "$e" --arg d "$out/run-$id" \
    '. + [{id: $id, report: ($d + "/report-" + $id + ".json"), records: ($d + "/results"), started: $s, ended: $e}]' <<<"$runs")"
done
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
jq -n --argjson runs "$runs" --argjson cert "$cert_json" --argjson recheck "$recheck_json" \
  --arg causes "$root/hack/compat-exp/causes.json" --arg ledger "$ledger_file" --arg frame "$charts" --arg static "$out/static.json" \
  '{runs: $runs, certification: $cert, causes: $causes, ledger: $ledger, frame: $frame, static: $static}
   + (if $recheck == null then {} else {recheck: $recheck} end)' >"$out/inputs.json" || die "could not write the manifest"

# --- 4. ledger check ------------------------------------------------------------------

"$ledger_bin" check -ledger "$ledger_file" -causes "$root/hack/compat-exp/causes.json" >"$out/ledger-check.txt" 2>&1 || {
  cat "$out/ledger-check.txt" >&2
  die "ledger check failed (see $out/ledger-check.txt)"
}

# --- 5. static checks --------------------------------------------------------------------

# The charts the reports hold: base charts of every observe run, not fix entries.
chart_ids=()
while IFS= read -r c; do chart_ids+=("$c"); done < <(
  for id in "$run1" ${run2:+"$run2"}; do jq -r '.charts[] | select((.fixOf // "") == "") | .chart' "$out/run-$id/report-$id.json"; done | sort -u)
[[ ${#chart_ids[@]} -gt 0 ]] || die "the reports hold no charts"
"$here/static.sh" "$charts" "$out/static.json" "${chart_ids[@]}" || die "static.sh failed"

# --- 6. crossrun ---------------------------------------------------------------------------

"$crossrun" -inputs "$out/inputs.json" -json "$out/result.json" -md "$out/result.md"
rc=$?
if [[ $rc -ne 0 ]]; then die "crossrun exited $rc (1: the ledger and the reports disagree; 2: unreadable input)"; fi
echo "analyze.sh: done: $out/result.json, $out/result.md"
jq -r '"region \(.region.region), N_new \(.metrics.nNew), P_new \(.metrics.pNew), D \(.metrics.d), P_D \(.metrics.pD), S \(.metrics.s)"' "$out/result.json" || true
