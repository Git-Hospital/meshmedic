#!/usr/bin/env bash
#
# Turn the qualification records into charts-exp.json (docs/compat-experiment.md
# section 4): the first 50 qualified candidates in rank order plus the next 15
# as reserves, the install deadline, the gate-bias table, and a sha256 over the
# canonical content.
#
# This is shell and jq rather than Go on purpose: it is a join and a few sums
# over JSON, the guard (guard.sh) has to recompute the same hash on a runner
# that has not built anything, and one jq canonicalisation shared by both is
# easier to audit than a second implementation.
#
# Usage:
#   hack/compat-exp/select.sh CANDIDATES_JSON RECORDS_DIR OUT [QUALIFICATION_RUN_ID]
#
#   CANDIDATES_JSON  frame build's candidates.json (rank order)
#   RECORDS_DIR      holds ID.qualify.json files written by qualify.sh
#   OUT              charts-exp.json to write
#   QUALIFICATION_RUN_ID  the workflow run id of the qualification run the
#                    records came from (digits); default $GITHUB_RUN_ID, which
#                    the select job of the workflow has. Required: a list
#                    without it is refused (exit 2).
#
# The list records where it came from, inside the hashed content:
#   qualification: {run, candidatesSha256, recordsSha256}
# run is the qualification run id; candidatesSha256 is the sha256 of
# `jq -S -c .` of CANDIDATES_JSON; recordsSha256 is the sha256 of
# `jq -S -c -s 'sort_by(.id)'` over every *.qualify.json in RECORDS_DIR. This is
# what binds the sample to the qualification run: analyze.sh downloads that run's
# records, runs this script again and refuses to go on unless the result is
# byte-identical to the committed list, so a list chosen after data exists, or
# from other records, does not reproduce.
#
# Entries carry the publisher (from candidates.json) and `qualified: true` (every
# listed chart qualified), because crossrun reads this file as its frame.
#
# Chart ids follow `frame charts`: the package name, or <repository>-<name>
# when two candidates share a name.
#
# Selection. Candidates are walked in rank order. A candidate with no record
# before the 65th qualified one makes the selection incomplete (exit 4), since
# a missing record could be a qualified chart that outranks one chosen.
# Fewer than 40 qualified (all candidates recorded) is the "infeasible" stop of
# section 7 (exit 1). Between 40 and 49 qualified the list is all of them and
# there are no reserves.
#
# Install deadline: max(600 s, 3 x the slowest qualification install of any
# chart in the list or the reserves), in whole seconds, the same for all. It is
# written into every entry as "deadline" ("<n>s", a Go duration helm accepts),
# which is the field observe.sh reads.
#
# Hash: sha256 of `jq -S -c 'del(.sha256)'` of the finished file, so it covers
# the charts, the reserves, the deadline and the bias table. guard.sh computes
# it the same way.
#
# Bias: for the four fields qualify.sh records, the share of qualified charts
# and the share of excluded charts (any record that is not qualified) that
# have it, charts with an unknown value (the template step failed) left out of
# both counts. The gap is the larger of the two ratios; above 1.5 (guess in
# the pre-registration) it is "exceeds". A share of 0 against a share above 0
# is an unbounded gap and exceeds.
#
# Exit codes: 0 written, 1 infeasible, 2 usage, a missing run id or unreadable input,
# 4 incomplete.
set -uo pipefail

cand="${1:?usage: select.sh CANDIDATES_JSON RECORDS_DIR OUT [QUALIFICATION_RUN_ID]}"
dir="${2:?records dir}"
out="${3:?output file}"
qual_run="${4:-${GITHUB_RUN_ID:-}}"
[[ "$qual_run" =~ ^[0-9]{1,20}$ ]] || { echo "select.sh: the qualification run id (4th argument or GITHUB_RUN_ID) must be digits, got '$qual_run'" >&2; exit 2; }

first=50
reserves=15
minimum=40

command -v jq >/dev/null || { echo "jq is needed" >&2; exit 2; }
[[ -f "$cand" ]] || { echo "no $cand" >&2; exit 2; }
[[ -d "$dir" ]] || { echo "no $dir" >&2; exit 2; }
shopt -s nullglob
files=("$dir"/*.qualify.json)
[[ ${#files[@]} -gt 0 ]] || { echo "no qualification records in $dir" >&2; exit 2; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
jq -s . "${files[@]}" >"$tmp/records.json" || { echo "unreadable record" >&2; exit 2; }

sha() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi
}

# shellcheck disable=SC2016 # a jq program, not shell
program='
  def share($n; $d): if $d == 0 then null else (($n / $d * 10000 | round) / 10000) end;
  def entry: {id, chart, version, why, rank, publisher, qualified: true} + (if .repo then {repo} else {} end);

  ($cand[0].candidates) as $c
  | ($c | map(.package) | group_by(.) | map({key: .[0], value: length}) | from_entries) as $count
  | [$c[] | {
      id: (if $count[.package] > 1 then .repository + "-" + .package else .package end),
      rank, repo: (.repo // null), chart, version, publisher,
      why: ("Artifact Hub top 300 by stars, rank \(.rank), \(.stars) stars, publisher \(.publisher)")
    }] as $entries
  | ($recs[0] | map({key: .id, value: .}) | from_entries) as $r
  | [$entries[] | . + {rec: $r[.id]}] as $rows
  | [$rows | to_entries[] | select(.value.rec != null and .value.rec.qualified == true) | .key] as $qidx
  | (if ($qidx | length) >= ($first + $reserves) then $qidx[$first + $reserves - 1] else ($rows | length) - 1 end) as $last
  | [$rows[0:$last + 1][] | select(.rec == null) | .id] as $missing
  | [$rows[] | select(.rec != null and .rec.qualified == true)] as $qual
  | [$rows[] | select(.rec != null and .rec.qualified != true)] as $excl
  | ($qual[0:$first]) as $main
  | ($qual[$first:$first + $reserves]) as $resv
  | (([$main[], $resv[]] | map(.rec.installs[]?.durationSeconds) | max) // 0) as $slowest
  | ([600, 3 * $slowest] | max) as $deadline
  | def finish: del(.rec) | del(.repo | select(. == null)) | . + {deadline: "\($deadline)s"};
    def field_bias($f):
      ([$qual[] | select(.rec.bias[$f] != null)]) as $qk
      | ([$excl[] | select(.rec.bias[$f] != null)]) as $ek
      | (($qk | map(select(.rec.bias[$f] == true)) | length)) as $qn
      | (($ek | map(select(.rec.bias[$f] == true)) | length)) as $en
      | share($qn; $qk | length) as $qs
      | share($en; $ek | length) as $es
      | {
          field: $f,
          qualified: {with: $qn, of: ($qk | length), share: $qs},
          excluded: {with: $en, of: ($ek | length), share: $es},
          unknownExcluded: (($excl | length) - ($ek | length)),
          ratio: (if $qs == null or $es == null or ($qs == 0 and $es == 0) then null
                  elif $qs == 0 or $es == 0 then null
                  else ((([$qs / $es, $es / $qs] | max) * 100 | round) / 100) end),
          unbounded: ($qs != null and $es != null and (($qs == 0) != ($es == 0))),
          exceeds: (if $qs == null or $es == null or ($qs == 0 and $es == 0) then false
                    elif $qs == 0 or $es == 0 then true
                    else ([$qs / $es, $es / $qs] | max) > 1.5 end)
        };
    (["hasStatefulSet","hasNetworkPolicy","hasNonHttpPort","hasHeadlessService"] | map(field_bias(.))) as $bias
  | {
      status: (if ($missing | length) > 0 then "incomplete"
               elif ($qual | length) < $minimum then "infeasible" else "ok" end),
      missing: $missing,
      qualifiedCount: ($qual | length),
      candidateCount: ($rows | length),
      recordedCount: ($rows | map(select(.rec != null)) | length),
      bias: $bias,
      file: {
        schema: 1,
        installDeadlineSeconds: $deadline,
        counts: {
          candidates: ($rows | length),
          recorded: ($rows | map(select(.rec != null)) | length),
          qualified: ($qual | length),
          excluded: ($excl | length),
          excludedByReason: ($excl | map(.rec.firstFailure.reason) | group_by(.) | map({key: .[0], value: length}) | from_entries),
          charts: ($main | length),
          reserves: ($resv | length)
        },
        charts: ($main | map(entry | finish)),
        reserves: ($resv | map(entry | finish)),
        bias: {fields: $bias, anyGapAbove1_5x: ($bias | any(.exceeds))}
      }
    }'

jq -n --slurpfile cand "$cand" --slurpfile recs "$tmp/records.json" \
  --argjson first "$first" --argjson reserves "$reserves" --argjson minimum "$minimum" \
  "$program" >"$tmp/result.json" || { echo "could not read the candidates or records" >&2; exit 2; }

status="$(jq -r .status "$tmp/result.json")"
case "$status" in
  incomplete)
    echo "incomplete: no qualification record for $(jq -r '.missing | length' "$tmp/result.json") candidate(s) ranked before the selection ends, first: $(jq -r '.missing[0:5] | join(", ")' "$tmp/result.json")" >&2
    exit 4
    ;;
  infeasible)
    echo "infeasible: only $(jq -r .qualifiedCount "$tmp/result.json") of $(jq -r .candidateCount "$tmp/result.json") candidates qualified, fewer than $minimum (docs/compat-experiment.md section 7: stop, the ADR stays narrowed)" >&2
    echo "bias table: $(jq -c .bias "$tmp/result.json")" >&2
    exit 1
    ;;
esac

cand_sha="$(jq -S -c . "$cand" | sha)"
recs_sha="$(jq -S -c 'sort_by(.id)' "$tmp/records.json" | sha)"
jq -S --arg run "$qual_run" --arg c "$cand_sha" --arg r "$recs_sha" \
  '.file + {qualification: {run: $run, candidatesSha256: $c, recordsSha256: $r}}' "$tmp/result.json" >"$tmp/body.json"
hash="$(jq -S -c . "$tmp/body.json" | sha)"
jq -S --arg sha "$hash" '. + {sha256: $sha}' "$tmp/body.json" >"$out"
echo "wrote $out: $(jq -r '"\(.counts.charts) charts, \(.counts.reserves) reserves, deadline \(.installDeadlineSeconds) s, bias gap above 1.5x: \(.bias.anyGapAbove1_5x)"' "$out"), sha256 $hash"
