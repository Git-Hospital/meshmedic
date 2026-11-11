#!/usr/bin/env bash
#
# The pass condition of work item 9 (docs/compat-experiment.md section 8), checked by a tool:
# "It passes only if every chart's W1 and W3 verdict matches pilot 1, and any E1 or E2 difference
# on a chart pilot 1 called same is settled under rule 5 before the freeze is tagged", plus "redis
# W3 differs, both fixes certify, podinfo W1 is C-optout". analyze.sh refuses the acceptance list,
# so nothing else reads an acceptance run.
#
#   hack/compat-exp/acceptance-check.sh [--expected FILE] DIR
#
# DIR is the report artifact of an acceptance run, as downloaded: report-<run id>.json (the
# difftable -json report; see DIFFTABLE-JSON.md) and results/exp-<run id>-<chart>-<world>/
# <chart>.<world>.runN.json (the observation records, RECORD.md). FILE is the expected verdicts,
# default acceptance-expected.json next to this script: per chart the W1 and W3 verdict of pilot 1,
# transcribed from the results table of docs/compat-pilot.md (a test keeps the two in step).
#
# What it prints, per chart and world: the pilot-1 verdict, the verdict on the classes pilot 1
# already had (every key class but apiserver and late), match or MISMATCH. Then, listed and not
# failed: the apiserver: and late: (E1, E2) differences on a chart and world pilot 1 called same,
# which are to be settled under rule 5 before the tag. Then whether redis W3 differs, whether each
# fix certifies, whether podinfo's W1 difference meets the c-optout condition (crossrun's check:
# testPodMeshStatus listed, every helm test hook pod opted out, in every record), and a plain
# statement for each chart whose W0 installs did not succeed that its match is vacuous.
#
# Exit codes: 0 every check holds (settling and vacuous charts are listed, not failures), 1 a
# mismatch or a failed condition, 2 usage or an unreadable input.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
expected="$here/acceptance-expected.json"
dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --expected) expected="${2:-}"; shift 2 ;;
    -h | --help) sed -n '2,/^set -/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//; /^set -/d'; exit 0 ;;
    -*) echo "acceptance-check.sh: unknown argument '$1'" >&2; exit 2 ;;
    *) [[ -z "$dir" ]] || { echo "acceptance-check.sh: one directory only" >&2; exit 2; }; dir="$1"; shift ;;
  esac
done
die() { echo "acceptance-check.sh: $*" >&2; exit 2; }
[[ -n "$dir" && -d "$dir" ]] || die "usage: acceptance-check.sh [--expected FILE] DIR (the report artifact of an acceptance run)"
command -v jq >/dev/null || die "jq is needed"
jq -e '.schema == 1 and (.charts | type == "array") and (.fixes | type == "array") and (.newClasses | type == "array") and (.optOutChart | type == "object")' "$expected" >/dev/null 2>&1 ||
  die "$expected is not an expected-verdicts file"

reports=("$dir"/report-*.json)
[[ ${#reports[@]} -eq 1 && -f "${reports[0]}" ]] || die "$dir must hold exactly one report-<run id>.json"
report="${reports[0]}"
jq -e '.schema == 1 and .oracle == "W0" and (.charts | type == "array") and (.worlds | index("W1")) and (.worlds | index("W3"))' "$report" >/dev/null 2>&1 ||
  die "$report is not a difftable -json report (schema 1, oracle W0, worlds including W1 and W3)"
[[ -d "$dir/results" ]] || die "$dir has no results/ (the observation records)"

bad=0
mismatch() { bad=$((bad + 1)); echo "  MISMATCH: $*"; }
newclasses="$(jq -c .newClasses "$expected")"

echo "== per chart: the verdicts on the classes pilot 1 already had (not apiserver:, late:) against pilot 1"
while IFS= read -r c; do
  for w in W1 W3; do
    want="$(jq -r --arg c "$c" --arg w "$w" '.charts[] | select(.chart == $c) | .[$w]' "$expected")"
    got="$(jq -r --arg c "$c" --arg w "$w" --argjson nc "$newclasses" '
      ([.charts[] | select(.chart == $c)][0].worlds // []) | map(select(.world == $w))[0] as $wr
      | if $wr == null then "absent"
        elif any($wr.keys[]?; (.class as $k | $nc | index($k) | not) and .verdict == "differs") then "differs"
        elif $wr.verdict == "undecided" then "undecided"
        else "same" end' "$report")"
    vac="$(jq -r --arg c "$c" '[.charts[] | select(.chart == $c)][0].oracleInstall.status // "unknown"' "$report")"
    if [[ "$got" == "$want" ]]; then
      echo "  $c $w: pilot 1 $want, now $got: match$([[ "$vac" == ok ]] || echo " (VACUOUS, see below)")"
    else
      mismatch "$c $w: pilot 1 $want, now $got"
    fi
  done
done < <(jq -r '.charts[].chart' "$expected")
unexpected="$(jq -r --slurpfile e "$expected" '[.charts[] | select(.fixOf == null or .fixOf == "") | .chart] - [$e[0].charts[].chart] | .[]' "$report")"
for c in $unexpected; do mismatch "$c is in the report and not among the ten charts of pilot 1"; done

echo "== apiserver: and late: differences (E1, E2) on a chart and world pilot 1 called same: to settle under rule 5 before the tag"
n=0
while IFS=$'\t' read -r c w k v; do
  [[ -n "$c" ]] || continue
  echo "  $c $w $k ($v)"
  n=$((n + 1))
done < <(jq -r --slurpfile e "$expected" --argjson nc "$newclasses" '
  $e[0] as $e
  | .charts[] | . as $ch | select(.fixOf == null or .fixOf == "")
  | $ch.worlds[] | . as $wr
  | select(($e.charts[] | select(.chart == $ch.chart) | .[$wr.world]) == "same")
  | $wr.keys[] | select((.class as $k | $nc | index($k)) and (.verdict == "differs" or .verdict == "flaky"))
  | [$ch.chart, $wr.world, .key, .verdict] | @tsv' "$report")
[[ $n -gt 0 ]] || echo "  none"

echo "== redis W3"
redis_w3="$(jq -r --arg c redis '[.charts[] | select(.chart == $c)][0].worlds // [] | map(select(.world == "W3"))[0].verdict // "absent"' "$report")"
if [[ "$redis_w3" == differs ]]; then echo "  redis W3 differs: yes"; else mismatch "redis W3 does not differ (verdict $redis_w3)"; fi

echo "== the fixes"
while IFS=$'\t' read -r f base; do
  got="$(jq -r --arg f "$f" --arg b "$base" '[.charts[] | select(.chart == $f and .fixOf == $b)][0] | if . == null then "absent" elif .certificate == null then "no certificate" elif .certificate.certified == true then "certified" else "not certified: " + (.certificate.problems | join("; ")) end' "$report")"
  if [[ "$got" == certified ]]; then echo "  $f (fix of $base) certifies: yes"; else mismatch "fix $f of $base: $got"; fi
done < <(jq -r '.fixes[] | [.fix, .fixOf] | @tsv' "$expected")

echo "== c-optout"
oc="$(jq -r '.optOutChart.chart' "$expected")"
ow="$(jq -r '.optOutChart.world' "$expected")"
ok_key="$(jq -r '.optOutChart.key' "$expected")"
diff_keys="$(jq -r --arg c "$oc" --arg w "$ow" --argjson nc "$newclasses" '
  [.charts[] | select(.chart == $c)][0].worlds // [] | map(select(.world == $w))[0].keys // []
  | map(select((.class as $k | $nc | index($k) | not) and .verdict == "differs") | .key) | join(" ")' "$report")"
if [[ "$diff_keys" != "$ok_key" ]]; then
  mismatch "$oc $ow: the differing keys are '${diff_keys:-none}', c-optout can explain '$ok_key' only"
else
  # crossrun's check (crossrun/check.go checkCallers), applied to every record of the chart and world
  shopt -s nullglob
  recs=("$dir"/results/exp-*-"$oc"-"$ow"/"$oc"."$ow".run*.json)
  shopt -u nullglob
  if [[ ${#recs[@]} -eq 0 ]]; then
    mismatch "$oc $ow: no observation records in $dir/results"
  else
    optmeta='.key == "sidecar.istio.io/inject" and .value == "false"'
    [[ "$ow" != W3 ]] || optmeta='.key == "istio.io/dataplane-mode" and .value == "none"'
    fail=0
    for r in "${recs[@]}"; do
      if ! jq -e "(.testPodMeshStatus == \"listed\") and ([.testPodMesh[]? | select(.testHook == true)] | length > 0)
          and all(.testPodMesh[]? | select(.testHook == true); (.client | not) and any(.meshMetadata[]?; $optmeta))" "$r" >/dev/null 2>&1; then
        mismatch "$oc $ow: $(basename "$r") does not meet the c-optout condition (testPodMeshStatus listed, at least one helm test hook pod, every one opted out and not the client)"
        fail=1
      fi
    done
    [[ $fail -eq 1 ]] || echo "  $oc $ow differs on $ok_key only, and in all ${#recs[@]} records every helm test pod is opted out: c-optout holds"
  fi
fi

echo "== charts whose W0 installs did not succeed"
v=0
while IFS=$'\t' read -r c st ok runs; do
  [[ -n "$c" ]] || continue
  echo "  VACUOUS: $c: W0 installs $ok of $runs ok (status $st). Every world is compared with a W0 that never ran the chart, so a same verdict for $c proves nothing."
  v=$((v + 1))
done < <(jq -r '.charts[] | select(.oracleInstall.status != "ok") | [.chart, (.oracleInstall.status // "unknown"), (.oracleInstall.ok // 0), (.oracleInstall.runs // 0)] | @tsv' "$report")
[[ $v -gt 0 ]] || echo "  none"

echo "== result"
if [[ $bad -eq 0 ]]; then
  echo "  every check holds; $n difference(s) to settle under rule 5, $v chart(s) vacuous"
  exit 0
fi
echo "  $bad mismatch(es): the acceptance run does not pass"
exit 1
