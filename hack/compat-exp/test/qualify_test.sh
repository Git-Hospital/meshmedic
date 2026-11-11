#!/usr/bin/env bash
#
# Tests for hack/compat-exp/qualify.sh and select.sh. No cluster: helm and
# kubectl are stubs that read their behaviour from a scenario directory; yq and
# jq are the real ones.
#
#   hack/compat-exp/test/qualify_test.sh
#
# Nothing here has run against a real cluster, a real helm or real Actions.
# The ok-or-fail one-liners are deliberate: ok and fail always succeed.
# shellcheck disable=SC2015,SC2016,SC2012
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
qualify="$repo/hack/compat-exp/qualify.sh"
select_sh="$repo/hack/compat-exp/select.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fails=0
fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }
ok() { echo "ok: $*"; }
eq() { # name got want
  if [[ "$2" == "$3" ]]; then ok "$1"; else fail "$1: got '$2', want '$3'"; fi
}

for tool in jq yq python3; do command -v "$tool" >/dev/null || { echo "need $tool" >&2; exit 2; }; done

# --- stubs ---------------------------------------------------------------------
bin="$tmp/bin"
mkdir -p "$bin"
cat >"$bin/helm" <<'STUBEND'
#!/usr/bin/env bash
# Behaviour comes from $STUB: template (ok|fail), rendered.yaml, install-N
# (ok|timeout|error), sleep-N (seconds), counter (installs so far).
echo "helm $*" >>"$STUB/calls"
case "$1" in
  template)
    if [[ "$(cat "$STUB/template" 2>/dev/null || echo ok)" == fail ]]; then
      echo "Error: chart \"nope\" matching 9.9.9 not found" >&2
      exit 1
    fi
    cat "$STUB/rendered.yaml"
    ;;
  install)
    n=$(($(cat "$STUB/counter" 2>/dev/null || echo 0) + 1))
    echo "$n" >"$STUB/counter"
    [[ -f "$STUB/sleep-$n" ]] && sleep "$(cat "$STUB/sleep-$n")"
    case "$(cat "$STUB/install-$n" 2>/dev/null || echo ok)" in
      ok) echo "STATUS: deployed" ;;
      timeout) echo "Error: INSTALLATION FAILED: context deadline exceeded" >&2; exit 1 ;;
      error) echo "Error: INSTALLATION FAILED: Deployment.apps \"x\" is invalid" >&2; exit 1 ;;
    esac
    ;;
  uninstall) ;;
esac
STUBEND
cat >"$bin/kubectl" <<'STUBEND'
#!/usr/bin/env bash
echo "kubectl $*" >>"$STUB/calls"
args="$*"
case "$args" in
  "config get-contexts -o name") echo "kind-mmc-w0" ;;
  "create namespace"*) echo "namespace/pilot created" ;;
  "delete namespace"*) ;;
  apply\ --dry-run=server*)
    case "$(cat "$STUB/dry" 2>/dev/null || echo ok)" in
      ok) echo "ok (server dry run)" ;;
      *) cat "$STUB/dry.err" >&2; exit 1 ;;
    esac
    ;;
  *"get pods -o json"*)
    n="$(cat "$STUB/counter" 2>/dev/null || echo 0)"
    if [[ -f "$STUB/pods-$n.json" ]]; then cat "$STUB/pods-$n.json"; else cat "$STUB/pods.json"; fi
    ;;
  *) echo "unexpected kubectl $args" >&2; exit 9 ;;
esac
STUBEND
chmod +x "$bin/helm" "$bin/kubectl"

pods_ready='{"items":[{"metadata":{"name":"a-1","ownerReferences":[{"kind":"ReplicaSet","name":"a-5f7"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"c","ready":true,"restartCount":0,"state":{"running":{}}}]}}]}'
pods_unready='{"items":[{"metadata":{"name":"a-1","ownerReferences":[{"kind":"ReplicaSet","name":"a-5f7"}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}],"containerStatuses":[{"name":"c","ready":false,"restartCount":0,"state":{"running":{}}}]}}]}'
pods_pull='{"items":[{"metadata":{"name":"a-1","ownerReferences":[{"kind":"ReplicaSet","name":"a-5f7"}]},"status":{"phase":"Pending","conditions":[],"containerStatuses":[{"name":"c","image":"docker.io/gone/img:1","ready":false,"restartCount":0,"state":{"waiting":{"reason":"ImagePullBackOff"}}}]}}]}'
pods_cap='{"items":[{"metadata":{"name":"a-1","ownerReferences":[{"kind":"ReplicaSet","name":"a-5f7"}]},"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable","message":"0/1 nodes are available: 1 Insufficient memory."}]}}]}'

plain_yaml='---
apiVersion: v1
kind: Service
metadata: {name: web}
spec:
  ports:
    - {name: http, port: 80, targetPort: 8080}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec: {replicas: 1}
'
rich_yaml='---
apiVersion: v1
kind: Service
metadata: {name: db}
spec:
  clusterIP: None
  ports:
    - {name: tcp-db, port: 5432}
---
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: db}
spec: {replicas: 1}
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: db}
spec: {podSelector: {}}
'
crd_yaml='---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: widgets.example.test}
spec:
  group: example.test
  names: {kind: Widget, plural: widgets}
---
apiVersion: example.test/v1
kind: Widget
metadata: {name: w}
'

state="$tmp/state"
mkdir -p "$state"
touch "$state/mmc-w0.kubeconfig"
printf 'world=W0\ncluster=mmc-w0\nkubernetes=v1.36.4\nistio=none\nregistry_mirror=docker.io:https://mirror.gcr.io,https://registry-1.docker.io\ncreated=2026-01-01T00:00:00Z\n' >"$state/mmc-w0.world"

charts="$tmp/charts.json"
echo '[{"id":"demo","repo":"https://example.test/charts","chart":"demo","version":"1.2.3","why":"t"}]' >"$charts"

# run_q NAME: runs qualify.sh on chart demo with the scenario in $tmp/sc-NAME.
# Sets rc and rec (the record path).
rc=0
rec=
run_q() {
  local sc="$tmp/sc-$1"
  mkdir -p "$sc/out"
  [[ -f "$sc/rendered.yaml" ]] || printf '%s' "$plain_yaml" >"$sc/rendered.yaml"
  [[ -f "$sc/pods.json" ]] || printf '%s' "$pods_ready" >"$sc/pods.json"
  rc=0
  STUB="$sc" PATH="$bin:$PATH" CHARTS_FILE="$charts" MMC_STATE="$state" MMC_SETTLE=0 \
    "$qualify" demo "$sc/out" >"$sc/stdout" 2>"$sc/stderr" || rc=$?
  rec="$sc/out/demo.qualify.json"
}
field() { jq -c "$1" "$rec"; }

# 1. a qualified chart
mkdir -p "$tmp/sc-good"
echo 1 >"$tmp/sc-good/sleep-2"
run_q good
eq "good: exit 0" "$rc" 0
eq "good: qualified" "$(field .qualified)" true
eq "good: no failure" "$(field .firstFailure)" null
eq "good: three installs" "$(field '.installs | length')" 3
eq "good: all ready" "$(field '[.installs[].ready] | all')" true
eq "good: durations are numbers" "$(field '.durationsSeconds | all(type == "number")')" true
eq "good: slowest is a number >= 1" "$(field '.slowestSeconds >= 1')" true
eq "good: slowest is the max" "$(field '.slowestSeconds == (.durationsSeconds | max)')" true
eq "good: bias of a plain chart" "$(field .bias)" '{"hasStatefulSet":false,"hasNetworkPolicy":false,"hasNonHttpPort":false,"hasHeadlessService":false}'
eq "good: install deadline passed to helm" "$(grep -c -- '--timeout 15m' "$tmp/sc-good/calls")" 3
eq "good: template with CRDs, defaults only" "$(grep -c 'helm template demo demo --version 1.2.3 --namespace pilot --include-crds --repo https://example.test/charts' "$tmp/sc-good/calls")" 1
eq "good: world recorded with mirror" "$(field '.environment.registry_mirror | startswith("docker.io:")')" true

# 2. bias fields of a stateful chart
mkdir -p "$tmp/sc-rich"
printf '%s' "$rich_yaml" >"$tmp/sc-rich/rendered.yaml"
run_q rich
eq "rich: bias all true" "$(field .bias)" '{"hasStatefulSet":true,"hasNetworkPolicy":true,"hasNonHttpPort":true,"hasHeadlessService":true}'

# 3. template failure
mkdir -p "$tmp/sc-tpl"
echo fail >"$tmp/sc-tpl/template"
run_q tpl
eq "template: exit 0 (a record)" "$rc" 0
eq "template: not qualified" "$(field .qualified)" false
eq "template: reason" "$(field .firstFailure.reason)" '"template"'
eq "template: step" "$(field .firstFailure.step)" '"template"'
eq "template: bias unknown" "$(field .bias.hasStatefulSet)" null
eq "template: no installs" "$(field '.installs | length')" 0
eq "template: slowest null" "$(field .slowestSeconds)" null
eq "template: nothing installed" "$(grep -c 'helm install' "$tmp/sc-tpl/calls" || true)" 0

# 4. dry-run failure keeps the bias fields
mkdir -p "$tmp/sc-dry"
printf '%s' "$rich_yaml" >"$tmp/sc-dry/rendered.yaml"
echo fail >"$tmp/sc-dry/dry"
echo 'Error from server (Invalid): error when creating "r.yaml": StatefulSet.apps "db" is invalid: spec.selector: Required value' >"$tmp/sc-dry/dry.err"
run_q dry
eq "dry-run: reason" "$(field .firstFailure.reason)" '"dry-run"'
eq "dry-run: bias still recorded" "$(field .bias.hasStatefulSet)" true
eq "dry-run: detail names the error" "$(field '.firstFailure.detail | contains("spec.selector")')" true
eq "dry-run: no install" "$(field '.installs | length')" 0

# 5. a CR whose CRD is in the render is tolerated; another error is not
mkdir -p "$tmp/sc-crd"
printf '%s' "$crd_yaml" >"$tmp/sc-crd/rendered.yaml"
echo fail >"$tmp/sc-crd/dry"
printf '%s\n%s\n' 'error: resource mapping not found for name: "w" namespace: "" from "r.yaml": no matches for kind "Widget" in version "example.test/v1"' 'ensure CRDs are installed first' >"$tmp/sc-crd/dry.err"
run_q crd
eq "crd: tolerated, qualified" "$(field .qualified)" true
eq "crd: noted" "$(field '.notes | length')" 1
mkdir -p "$tmp/sc-crd2"
printf '%s' "$crd_yaml" >"$tmp/sc-crd2/rendered.yaml"
echo fail >"$tmp/sc-crd2/dry"
printf '%s\n%s\n' 'error: resource mapping not found for name: "g" namespace: "" from "r.yaml": no matches for kind "Gadget" in version "example.test/v1"' 'ensure CRDs are installed first' >"$tmp/sc-crd2/dry.err"
run_q crd2
eq "crd: a kind without its CRD fails" "$(field .firstFailure.reason)" '"dry-run"'

# 6. install failures, each reason, and the first failing install is named
for pair in "pull-failure:$pods_pull" "capacity:$pods_cap" "not-ready:$pods_unready"; do
  r="${pair%%:*}"
  mkdir -p "$tmp/sc-$r"
  printf '%s' "${pair#*:}" >"$tmp/sc-$r/pods.json"
  run_q "$r"
  eq "$r: reason" "$(field .firstFailure.reason)" "\"$r\""
  eq "$r: install 1 is the first failing one" "$(field .firstFailure.install)" 1
  eq "$r: not qualified" "$(field .qualified)" false
  eq "$r: stops after the failure" "$(field '.installs | length')" 1
done
mkdir -p "$tmp/sc-timeout" "$tmp/sc-error"
echo timeout >"$tmp/sc-timeout/install-1"
run_q timeout
eq "timeout: reason" "$(field .firstFailure.reason)" '"timeout"'
echo error >"$tmp/sc-error/install-1"
run_q error
eq "install-error: reason" "$(field .firstFailure.reason)" '"install-error"'

# 7. the second install fails: install 2 named, two durations kept
mkdir -p "$tmp/sc-second"
echo error >"$tmp/sc-second/install-2"
run_q second
eq "second: install number" "$(field .firstFailure.install)" 2
eq "second: durations so far" "$(field '.durationsSeconds | length')" 2
eq "second: first install was ready" "$(field '.installs[0].ready')" true

# 8. a pull failure beats a failed helm (the registry explains the timeout)
mkdir -p "$tmp/sc-pulltimeout"
printf '%s' "$pods_pull" >"$tmp/sc-pulltimeout/pods.json"
echo timeout >"$tmp/sc-pulltimeout/install-1"
run_q pulltimeout
eq "pull-failure outranks timeout" "$(field .firstFailure.reason)" '"pull-failure"'

# 9. a world without the mirror is refused; an unknown chart is a usage error
nomirror="$tmp/state-nomirror"
mkdir -p "$nomirror" "$tmp/sc-nomirror/out"
touch "$nomirror/mmc-w0.kubeconfig"
grep -v registry_mirror "$state/mmc-w0.world" >"$nomirror/mmc-w0.world"
printf '%s' "$pods_ready" >"$tmp/sc-nomirror/pods.json"
rc=0
STUB="$tmp/sc-nomirror" PATH="$bin:$PATH" CHARTS_FILE="$charts" MMC_STATE="$nomirror" MMC_SETTLE=0 \
  "$qualify" demo "$tmp/sc-nomirror/out" >/dev/null 2>"$tmp/sc-nomirror/stderr" || rc=$?
eq "no mirror: exit 3" "$rc" 3
rc=0
STUB="$tmp/sc-nomirror" PATH="$bin:$PATH" CHARTS_FILE="$charts" MMC_STATE="$state" MMC_SETTLE=0 \
  "$qualify" ghost "$tmp/sc-nomirror/out" >/dev/null 2>&1 || rc=$?
eq "unknown chart: exit 2" "$rc" 2

# 10. non-HTTP port definition, case by case (the jq program lifted from qualify.sh)
bias_prog="$(python3 -I -c '
import re, sys
m = re.search(r"^bias_filter=\x27(.*?)\x27\n", open(sys.argv[1]).read(), re.S | re.M)
print(m.group(1))' "$qualify")"
port_bias() { # one YAML flow mapping for a Service port
  printf 'apiVersion: v1\nkind: Service\nmetadata: {name: s}\nspec:\n  ports:\n    - %s\n' "$1" |
    yq -o=json -I=0 '.' | jq -s "$bias_prog" | jq -c .hasNonHttpPort
}
eq "port: http 80" "$(port_bias '{name: http, port: 80}')" false
eq "port: unnamed 8080" "$(port_bias '{port: 8080}')" false
eq "port: unnamed 6379" "$(port_bias '{port: 6379}')" true
eq "port: name metrics is undeclared, 9100 is not an HTTP number" "$(port_bias '{name: metrics, port: 9100}')" true
eq "port: name http-metrics" "$(port_bias '{name: http-metrics, port: 9100}')" false
eq "port: name tcp-x" "$(port_bias '{name: tcp-x, port: 8080}')" true
eq "port: appProtocol redis beats http name" "$(port_bias '{name: http, port: 80, appProtocol: redis}')" true
eq "port: appProtocol kubernetes.io/h2c" "$(port_bias '{port: 7000, appProtocol: kubernetes.io/h2c}')" false
eq "port: grpc" "$(port_bias '{name: grpc, port: 50051}')" false
eq "port: UDP" "$(port_bias '{name: dns, port: 53, protocol: UDP}')" true

# 11. the jq programs copied from observe.sh are still the same text
if python3 -I -c '
import re, sys
obs = open(sys.argv[1]).read()
q = open(sys.argv[2]).read()
for var in ("workloads_filter", "pullfailures_filter", "capacity_filter"):
    m = re.search(r"^%s=\x27(.*?)\x27\n" % var, q, re.S | re.M)
    assert m, var
    assert m.group(1) in obs, "program %s not found verbatim in observe.sh" % var
' "$repo/hack/compat-pilot/observe.sh" "$qualify"; then ok "shared jq programs equal observe.sh's"; else fail "shared jq programs drifted from observe.sh"; fi

# --- select.sh -----------------------------------------------------------------

# mkcands N: candidates.json with N candidates, ranks 1..N.
mkcands() {
  jq -n --argjson n "$1" '{candidates: [range(1; $n + 1) as $i | {rank: $i, stars: (10000 - $i), package: "chart\($i)", repository: "repo\($i)", publisher: "p\($i)", chart: "chart\($i)", repo: "https://example.test/\($i)", version: "1.0.\($i)"}]}'
}
# mkrec DIR ID QUALIFIED SECONDS BITS [REASON]; BITS is four 0/1 characters:
# StatefulSet, NetworkPolicy, non-HTTP port, headless Service.
mkrec() {
  jq -n --arg id "$2" --argjson q "$3" --argjson s "$4" --arg bits "$5" --arg reason "${6:-not-ready}" '
    ($bits | split("") | map(. == "1")) as $b
    | {id: $id, qualified: $q,
       firstFailure: (if $q then null else {step: "install", install: 1, reason: $reason, detail: ""} end),
       installs: (if $q then [{n:1,durationSeconds:$s,ready:true,helmRc:0},{n:2,durationSeconds:($s - 1),ready:true,helmRc:0},{n:3,durationSeconds:($s - 2),ready:true,helmRc:0}] else [] end),
       bias: {hasStatefulSet: $b[0], hasNetworkPolicy: $b[1], hasNonHttpPort: $b[2], hasHeadlessService: $b[3]}}' >"$1/$2.qualify.json"
}
# scenario DIR N QUAL_EXPR: N candidates; those for which the jq expression
# over $i is true qualify.
scenario() {
  mkdir -p "$1/recs"
  mkcands "$2" >"$1/cands.json"
  local i q
  for i in $(seq 1 "$2"); do
    q="$(jq -n --argjson i "$i" "$3")"
    mkrec "$1/recs" "chart$i" "$q" $((100 + i)) 0000
  done
}
sel() { # dir; sets rc, writes dir/out.json
  rc=0
  "$select_sh" "$1/cands.json" "$1/recs" "$1/out.json" >"$1/stdout" 2>"$1/stderr" || rc=$?
}

scenario "$tmp/s1" 80 '$i % 8 != 0'
sel "$tmp/s1"
eq "select: exit 0" "$rc" 0
eq "select: 50 charts" "$(jq '.charts | length' "$tmp/s1/out.json")" 50
eq "select: 15 reserves" "$(jq '.reserves | length' "$tmp/s1/out.json")" 15
eq "select: skips failures, rank order" "$(jq -c '.charts[0:9] | map(.rank)' "$tmp/s1/out.json")" '[1,2,3,4,5,6,7,9,10]'
eq "select: charts then reserves are consecutive qualified ranks" "$(jq '[.charts[].rank, .reserves[].rank] as $r | $r == ($r | sort) and ($r | all(. % 8 != 0))' "$tmp/s1/out.json")" true
eq "select: nothing past the 65th qualified" "$(jq '[.charts[].rank, .reserves[].rank] | max' "$tmp/s1/out.json")" 74
eq "select: entry shape observe.sh reads" "$(jq -c '.charts[0] | {id, chart, version, repo, deadline}' "$tmp/s1/out.json")" '{"id":"chart1","chart":"chart1","version":"1.0.1","repo":"https://example.test/1","deadline":"600s"}'
eq "select: publisher and qualified carried for crossrun's frame" "$(jq -c '.charts[0] | {publisher, qualified}' "$tmp/s1/out.json")" '{"publisher":"p1","qualified":true}'
eq "select: every entry has them" "$(jq '[.charts[], .reserves[]] | all(.publisher != null and .qualified == true)' "$tmp/s1/out.json")" true
eq "select: bias fields present" "$(jq '.bias.fields | length' "$tmp/s1/out.json")" 4

# deadline: slowest install is 174 s here, so the 600 s floor; 400 s gives 1200 s
eq "select: floor of 10 minutes" "$(jq .installDeadlineSeconds "$tmp/s1/out.json")" 600
mkrec "$tmp/s1/recs" chart5 true 400 0000
sel "$tmp/s1"
eq "select: 3 x slowest" "$(jq .installDeadlineSeconds "$tmp/s1/out.json")" 1200
eq "select: deadline in every entry" "$(jq '[.charts[], .reserves[]] | all(.deadline == "1200s")' "$tmp/s1/out.json")" true

# a slow chart outside the 65 does not move the deadline
mkrec "$tmp/s1/recs" chart79 true 4000 0000
sel "$tmp/s1"
eq "select: slowest install outside the list is ignored" "$(jq .installDeadlineSeconds "$tmp/s1/out.json")" 1200

# hash over the canonical content, and deterministic bytes
stored="$(jq -r .sha256 "$tmp/s1/out.json")"
calc="$(jq -S -c 'del(.sha256)' "$tmp/s1/out.json" | shasum -a 256 | cut -d' ' -f1)"
eq "select: sha256 over canonical content" "$stored" "$calc"
cp "$tmp/s1/out.json" "$tmp/s1/first.json"
mkdir -p "$tmp/s1b/recs"
cp "$tmp/s1/cands.json" "$tmp/s1b/"
for f in $(ls "$tmp/s1/recs" | sort -r); do cp "$tmp/s1/recs/$f" "$tmp/s1b/recs/$f"; done
sel "$tmp/s1b"
if cmp -s "$tmp/s1/first.json" "$tmp/s1b/out.json"; then ok "select: deterministic bytes"; else fail "select: output differs between runs"; fi
mkrec "$tmp/s1b/recs" chart2 true 999 0000
sel "$tmp/s1b"
[[ "$(jq -r .sha256 "$tmp/s1b/out.json")" != "$stored" ]] && ok "select: hash follows the content" || fail "select: hash did not change"

# boundaries of the selection
scenario "$tmp/s65" 70 '$i <= 65'
sel "$tmp/s65"
eq "65 qualified: 50 + 15" "$(jq -c '[.charts, .reserves] | map(length)' "$tmp/s65/out.json")" '[50,15]'
scenario "$tmp/s64" 70 '$i <= 64'
sel "$tmp/s64"
eq "64 qualified: 50 + 14" "$(jq -c '[.charts, .reserves] | map(length)' "$tmp/s64/out.json")" '[50,14]'
scenario "$tmp/s66" 70 '$i <= 66'
sel "$tmp/s66"
eq "66 qualified: the 66th is left out" "$(jq -c '[.charts, .reserves] | map(length)' "$tmp/s66/out.json")" '[50,15]'
eq "66 qualified: last reserve is rank 65" "$(jq '.reserves[-1].rank' "$tmp/s66/out.json")" 65
scenario "$tmp/s45" 60 '$i <= 45'
sel "$tmp/s45"
eq "45 qualified: all of them, no reserves" "$(jq -c '[.charts, .reserves] | map(length)' "$tmp/s45/out.json")" '[45,0]'
scenario "$tmp/s40" 60 '$i <= 40'
sel "$tmp/s40"
eq "40 qualified is feasible" "$rc" 0
scenario "$tmp/s39" 60 '$i <= 39'
sel "$tmp/s39"
eq "39 qualified: exit 1" "$rc" 1
eq "39 qualified: infeasible message" "$(grep -c '^infeasible: only 39 of 60' "$tmp/s39/stderr")" 1
[[ ! -e "$tmp/s39/out.json" ]] && ok "infeasible writes no list" || fail "infeasible wrote a list"

# a record missing before the 65th qualified is incomplete; after it, fine
scenario "$tmp/sm" 80 '$i <= 70'
rm "$tmp/sm/recs/chart10.qualify.json"
sel "$tmp/sm"
eq "missing record before the cut: exit 4" "$rc" 4
scenario "$tmp/sm2" 80 '$i <= 70'
rm "$tmp/sm2/recs/chart75.qualify.json"
sel "$tmp/sm2"
eq "missing record after the cut: fine" "$rc" 0
scenario "$tmp/sm3" 60 '$i <= 20'
rm "$tmp/sm3/recs/chart55.qualify.json"
sel "$tmp/sm3"
eq "few qualified and a missing record: incomplete, not infeasible" "$rc" 4

# duplicate package names get repository-package ids, like frame charts
mkdir -p "$tmp/sd/recs"
jq -n '{candidates: [range(1; 61) as $i | {rank: $i, stars: 5, package: (if $i <= 2 then "dup" else "c\($i)" end), repository: "r\($i)", publisher: "p\($i)", chart: "x", repo: "https://e.test", version: "1.0.0"}]}' >"$tmp/sd/cands.json"
for i in $(seq 1 60); do
  id="c$i"
  [[ $i -le 2 ]] && id="r$i-dup"
  mkrec "$tmp/sd/recs" "$id" true 100 0000
done
sel "$tmp/sd"
eq "duplicate package ids" "$(jq -c '.charts[0:3] | map(.id)' "$tmp/sd/out.json")" '["r1-dup","r2-dup","c3"]'

# bias table: qualified 50% stateful, excluded 20% -> 2.5x
mkdir -p "$tmp/sb/recs"
mkcands 60 >"$tmp/sb/cands.json"
for i in $(seq 1 60); do
  if [[ $i -le 50 ]]; then
    bits=0000
    [[ $((i % 2)) -eq 0 ]] && bits=1000
    mkrec "$tmp/sb/recs" "chart$i" true 100 "$bits"
  else
    bits=0000
    [[ $i -le 52 ]] && bits=1000
    mkrec "$tmp/sb/recs" "chart$i" false 0 "$bits" pull-failure
  fi
done
sel "$tmp/sb"
eq "bias: qualified share" "$(jq '.bias.fields[] | select(.field == "hasStatefulSet") | .qualified.share' "$tmp/sb/out.json")" 0.5
eq "bias: excluded share" "$(jq '.bias.fields[] | select(.field == "hasStatefulSet") | .excluded.share' "$tmp/sb/out.json")" 0.2
eq "bias: ratio 2.5 exceeds 1.5" "$(jq -c '.bias.fields[] | select(.field == "hasStatefulSet") | [.ratio, .exceeds]' "$tmp/sb/out.json")" '[2.5,true]'
eq "bias: both zero is no gap" "$(jq -c '.bias.fields[] | select(.field == "hasNetworkPolicy") | [.ratio, .exceeds]' "$tmp/sb/out.json")" '[null,false]'
eq "bias: any gap flag" "$(jq .bias.anyGapAbove1_5x "$tmp/sb/out.json")" true
eq "bias: excluded counted by reason" "$(jq -c .counts.excludedByReason "$tmp/sb/out.json")" '{"pull-failure":10}'
# excluded share 0 against a qualified share above 0: unbounded, exceeds
for i in $(seq 51 60); do mkrec "$tmp/sb/recs" "chart$i" false 0 0000 timeout; done
sel "$tmp/sb"
eq "bias: zero against nonzero is unbounded and exceeds" "$(jq -c '.bias.fields[] | select(.field == "hasStatefulSet") | [.ratio, .unbounded, .exceeds]' "$tmp/sb/out.json")" '[null,true,true]'
# 1.4x: qualified 0.7, excluded 0.5
mkdir -p "$tmp/sb2/recs"
mkcands 60 >"$tmp/sb2/cands.json"
for i in $(seq 1 60); do
  if [[ $i -le 50 ]]; then
    bits=0000
    [[ $((i % 10)) -lt 7 ]] && bits=1000
    mkrec "$tmp/sb2/recs" "chart$i" true 100 "$bits"
  else
    bits=0000
    [[ $i -le 55 ]] && bits=1000
    mkrec "$tmp/sb2/recs" "chart$i" false 0 "$bits" timeout
  fi
done
sel "$tmp/sb2"
eq "bias: 1.4x is not above 1.5x" "$(jq -c '.bias.fields[] | select(.field == "hasStatefulSet") | [.ratio, .exceeds]' "$tmp/sb2/out.json")" '[1.4,false]'
# a template-failed chart (bias null) is left out of both shares and counted apart
mkdir -p "$tmp/sb3/recs"
mkcands 60 >"$tmp/sb3/cands.json"
for i in $(seq 1 60); do
  if [[ $i -le 50 ]]; then mkrec "$tmp/sb3/recs" "chart$i" true 100 0000; else mkrec "$tmp/sb3/recs" "chart$i" false 0 0000 template; fi
done
jq '.bias = {hasStatefulSet: null, hasNetworkPolicy: null, hasNonHttpPort: null, hasHeadlessService: null}' "$tmp/sb3/recs/chart60.qualify.json" >"$tmp/sb3/x" && mv "$tmp/sb3/x" "$tmp/sb3/recs/chart60.qualify.json"
sel "$tmp/sb3"
eq "bias: unknown fields counted apart" "$(jq -c '.bias.fields[0] | [.excluded.of, .unknownExcluded]' "$tmp/sb3/out.json")" '[9,1]'

if [[ $fails -ne 0 ]]; then echo "$fails failure(s)" >&2; exit 1; fi
echo "all qualify/select tests passed"
