#!/usr/bin/env bash
#
# hack/compat-exp/acceptance-check.sh on fixtures in the layout of the report artifact
# (report-<run id>.json and results/exp-<run id>-<chart>-<world>/<chart>.<world>.runN.json),
# and a check that acceptance-expected.json and the results table of docs/compat-pilot.md agree.
#
#   hack/compat-exp/test/acceptance_check_test.sh
#
# The ok-or-fail one-liners are deliberate: ok and bad always succeed.
# shellcheck disable=SC2015,SC2016
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
tool="$repo/hack/compat-exp/acceptance-check.sh"
expected="$repo/hack/compat-exp/acceptance-expected.json"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
fails=0
ok() { echo "ok: $*"; }
bad() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }
eq() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1: got '$2', want '$3'"; fi; }
says() { if grep -q -- "$2" "$T/out"; then ok "$1"; else bad "$1: output lacks '$2'"; fi; }
lacks() { if grep -q -- "$2" "$T/out"; then bad "$1: output has '$2'"; else ok "$1"; fi; }
command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }

# --- the expected file and the document agree -----------------------------------------------

# the results table of docs/compat-pilot.md: | chart | W1 | W2 | W3 |, cell text to a verdict
awk -F'|' '
  /^\| chart \| W1 sidecar\+init/ { t = 1; next }
  t && /^\|---/ { next }
  t && /^\|/ {
    split("", c)
    for (i = 2; i <= 5; i++) { gsub(/^ +| +$/, "", $i) }
    name = $2; w1 = $3; w3 = $5
    printf "%s\t%s\t%s\n", name, w1, w3
    next
  }
  t { exit }' "$repo/docs/compat-pilot.md" >"$T/doc.tsv"
verdict() { # cell text
  case "$1" in
    same*) echo same ;;
    "**differs**"*) echo differs ;;
    '`helm test` fails'*) echo differs ;;
    *) echo "unreadable:$1" ;;
  esac
}
: >"$T/doc.json"
while IFS=$'\t' read -r name w1 w3; do
  c="$name"
  [[ "$c" != "redis (Bitnami)" ]] || c=redis
  jq -nc --arg c "$c" --arg a "$(verdict "$w1")" --arg b "$(verdict "$w3")" '{chart: $c, W1: $a, W3: $b}' >>"$T/doc.json"
done <"$T/doc.tsv"
eq "the document's table has the ten charts" "$(wc -l <"$T/doc.json" | tr -d ' ')" 10
eq "no cell of the table is unreadable" "$(grep -c unreadable "$T/doc.json" || true)" 0
eq "acceptance-expected.json agrees with the results table of docs/compat-pilot.md (W1 and W3 per chart)" \
  "$(jq -s -c 'map({chart, W1, W3}) | sort_by(.chart)' "$T/doc.json")" \
  "$(jq -c '.charts | map({chart, W1, W3}) | sort_by(.chart)' "$expected")"

# --- fixtures in the real layout ---------------------------------------------------------------

# the report of an acceptance run that matches pilot 1 and passes
mkreport() { # FILE
  jq -n --slurpfile e "$expected" '
    def key($k; $class; $v): {key: $k, class: $class, verdict: $v};
    def world($w; $v; $keys): {world: $w, verdict: $v, findings: 0, flakyKeys: 0, runsUsed: [1,2,3], runsLeftOut: [], oracleRunsUsed: [1,2,3], oracleRunsLeftOut: [], analyzeCodes: [],
      keys: ([key("helm:install"; "helm"; "same")] + $keys)};
    def chart($c; $w1; $w3): {chart: $c, chartVersion: "1", environment: "x", oracleInstall: {runs: 3, ok: 3, failed: 0, status: "ok"},
      worlds: [world("W1"; (if $w1 == "differs" then "differs" else "same" end); (if $w1 == "differs" then [key("helm:test"; "helm"; "differs")] else [] end)),
               world("W3"; (if $w3 == "differs" then "differs" else "same" end); (if $w3 == "differs" then [key("port:\($c):80:http"; "port"; "differs")] else [] end))]};
    {schema: 1, oracle: "W0", worlds: ["W1", "W3"], runsRequired: 3,
     charts: ([$e[0].charts[] | chart(.chart; .W1; .W3)]
       + [$e[0].fixes[] | chart(.fix; "same"; "same") + {fixOf: .fixOf, certificate: {fix: .fix, base: .fixOf, certified: true, cleared: ["W3"], problems: []}}])}' >"$1"
}
# records of podinfo W1: the test pod is opted out (or not)
mkrecords() { # DIR MODE: optout | plain | gone | mesh
  local d="$1" mode="$2" n
  for n in 1 2 3; do
    mkdir -p "$d/results/exp-100-podinfo-W1"
    case "$mode" in
      optout) tp='[{"pod":"podinfo-test-x","client":false,"testHook":true,"meshMetadata":[{"kind":"annotation","key":"sidecar.istio.io/inject","value":"false"}]}]'; st=listed ;;
      plain) tp='[{"pod":"podinfo-test-x","client":false,"testHook":true,"meshMetadata":[]}]'; st=listed ;;
      gone) tp='[]'; st=no-new-pods ;;
      mesh) tp='[{"pod":"podinfo-test-x","client":false,"testHook":true,"meshMetadata":[{"kind":"annotation","key":"sidecar.istio.io/inject","value":"false"}]},{"pod":"podinfo-test-y","client":false,"testHook":true,"meshMetadata":[]}]'; st=listed ;;
    esac
    jq -n --argjson tp "$tp" --arg st "$st" --argjson n "$n" '{chart: "podinfo", world: "W1", run: $n, podMesh: [], testPodMesh: $tp, testPodMeshStatus: $st}' \
      >"$d/results/exp-100-podinfo-W1/podinfo.W1.run$n.json"
  done
}
fixture() { # NAME [MODE]
  local d="$T/$1"
  rm -rf "$d"; mkdir -p "$d/results"
  mkreport "$d/report-100.json"
  mkrecords "$d" "${2:-optout}"
}
run() { # NAME: rc in $rc, output in $T/out
  "$tool" "$T/$1" >"$T/out" 2>"$T/err"
  rc=$?
}
edit() { jq "$2" "$T/$1/report-100.json" >"$T/x" && mv "$T/x" "$T/$1/report-100.json"; } # NAME FILTER

fixture good
run good
eq "a run that matches pilot 1 passes (exit 0)" "$rc" 0
says "  redis W3 is reported as differing" "redis W3 differs: yes"
says "  both fixes are reported as certifying" "redis-np-15008-patch (fix of redis) certifies: yes"
says "  podinfo W1 is c-optout" "c-optout holds"
says "  nothing to settle" "0 difference(s) to settle"

fixture m1; edit m1 '(.charts[] | select(.chart == "redis") | .worlds[] | select(.world == "W3")) |= (.verdict = "same" | .keys = [])'
run m1; eq "redis W3 not differing: mismatch (exit 1)" "$rc" 1; says "  and says so" "MISMATCH: redis W3: pilot 1 differs, now same"; says "  and redis W3 differs: no" "redis W3 does not differ"
fixture m2; edit m2 '(.charts[] | select(.chart == "grafana") | .worlds[] | select(.world == "W1")) |= (.verdict = "differs" | .keys += [{key: "port:g:80:http", class: "port", verdict: "differs"}])'
run m2; eq "a chart pilot 1 called same that now differs: mismatch (exit 1)" "$rc" 1; says "  and names it" "MISMATCH: grafana W1: pilot 1 same, now differs"
fixture m3; edit m3 '(.charts[] | select(.chart == "nats") | .worlds[] | select(.world == "W3")) |= (.verdict = "undecided")'
run m3; eq "an undecided world is not a match (exit 1)" "$rc" 1; says "  and says so" "MISMATCH: nats W3: pilot 1 same, now undecided"
fixture m4; edit m4 '.charts |= map(select(.chart != "rabbitmq"))'
run m4; eq "a chart missing from the report: mismatch (exit 1)" "$rc" 1; says "  and says so" "rabbitmq W1: pilot 1 same, now absent"
fixture m5; edit m5 '.charts += [{chart: "extra", worlds: [], oracleInstall: {status: "ok"}}]'
run m5; eq "a chart that is not one of the ten: mismatch (exit 1)" "$rc" 1; says "  and says so" "extra is in the report"

# E1 and E2 differences: listed on charts pilot 1 called same, never failed, not listed where pilot 1 saw a difference
fixture e1
edit e1 '(.charts[] | select(.chart == "grafana") | .worlds[] | select(.world == "W1")).keys += [{key: "apiserver:webhook", class: "apiserver", verdict: "differs"}]
         | (.charts[] | select(.chart == "nats") | .worlds[] | select(.world == "W3")).keys += [{key: "late:restarts", class: "late", verdict: "flaky"}]
         | (.charts[] | select(.chart == "redis") | .worlds[] | select(.world == "W3")).keys += [{key: "apiserver:webhook", class: "apiserver", verdict: "differs"}]'
run e1
eq "apiserver: and late: differences do not fail the check (exit 0)" "$rc" 0
says "  grafana W1 apiserver: is listed to settle" "grafana W1 apiserver:webhook (differs)"
says "  nats W3 late: is listed to settle" "nats W3 late:restarts (flaky)"
lacks "  redis W3, which pilot 1 saw differ, is not listed" "redis W3 apiserver"
says "  the count is two" "2 difference(s) to settle"
fixture e2
edit e2 '(.charts[] | select(.chart == "grafana") | .worlds[] | select(.world == "W1")) |= (.verdict = "differs" | .keys += [{key: "apiserver:webhook", class: "apiserver", verdict: "differs"}])'
run e2; eq "a world whose only difference is an apiserver: key still matches the pre-existing classes (exit 0)" "$rc" 0

# the fixes
fixture f1; edit f1 '(.charts[] | select(.chart == "redis-np-15008") | .certificate) |= (.certified = false | .problems = ["still differs in W3"])'
run f1; eq "a fix that does not certify (exit 1)" "$rc" 1; says "  and says why" "fix redis-np-15008 of redis: not certified: still differs in W3"
fixture f2; edit f2 '.charts |= map(select(.chart != "redis-np-15008-patch"))'
run f2; eq "a missing fix entry (exit 1)" "$rc" 1; says "  and says so" "fix redis-np-15008-patch of redis: absent"

# podinfo W1 and c-optout
for mode in plain gone mesh; do
  fixture "c-$mode"; mkrecords "$T/c-$mode" "$mode"
  run "c-$mode"; eq "podinfo W1 with test pods $mode: the c-optout condition fails (exit 1)" "$rc" 1; says "  and names the record" "podinfo.W1.run1.json does not meet the c-optout condition"
done
fixture c-key; edit c-key '(.charts[] | select(.chart == "podinfo") | .worlds[] | select(.world == "W1")).keys += [{key: "port:podinfo:9898:http", class: "port", verdict: "differs"}]'
run c-key; eq "podinfo W1 differing on a key besides helm:test (exit 1)" "$rc" 1; says "  and says so" "c-optout can explain 'helm:test' only"
fixture c-none; rm -rf "$T/c-none/results"; mkdir "$T/c-none/results"
run c-none; eq "podinfo W1 with no records (exit 1)" "$rc" 1; says "  and says so" "no observation records"

# W0 installs that did not succeed
fixture v1; edit v1 '(.charts[] | select(.chart == "valkey") | .oracleInstall) |= {runs: 3, ok: 0, failed: 3, status: "failed"}'
run v1; eq "a chart whose W0 never installed still matches (exit 0)" "$rc" 0
says "  and the output says plainly that the match is vacuous" "VACUOUS: valkey: W0 installs 0 of 3 ok (status failed)"
says "  and the per-chart line is marked" "valkey W1: pilot 1 same, now same: match (VACUOUS"
says "  and the summary counts it" "1 chart(s) vacuous"

# usage and bad inputs
"$tool" >"$T/out" 2>&1; eq "no directory: usage (exit 2)" "$?" 2
"$tool" "$T/nonexistent" >"$T/out" 2>&1; eq "no such directory (exit 2)" "$?" 2
mkdir -p "$T/empty/results"; "$tool" "$T/empty" >"$T/out" 2>&1; eq "a directory without a report (exit 2)" "$?" 2
fixture two; cp "$T/two/report-100.json" "$T/two/report-101.json"; "$tool" "$T/two" >"$T/out" 2>&1; eq "two reports (exit 2)" "$?" 2
fixture bs; echo '{"schema":2}' >"$T/bs/report-100.json"; "$tool" "$T/bs" >"$T/out" 2>&1; eq "a report that is not a difftable -json report (exit 2)" "$?" 2
"$tool" --expected "$T/nonexistent.json" "$T/good" >"$T/out" 2>&1; eq "an unreadable expected file (exit 2)" "$?" 2

if [[ $fails -eq 0 ]]; then echo "all acceptance-check tests passed"; else echo "$fails FAILED" >&2; exit 1; fi
