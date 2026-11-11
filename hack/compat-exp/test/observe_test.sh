#!/usr/bin/env bash
#
# Tests for hack/compat-pilot/observe.sh with stub helm, kubectl and istioctl.
# No cluster, no network, no sleeping. Run from anywhere:
#
#   hack/compat-exp/test/observe_test.sh
#
# Each test builds a small copy of the repository layout in a temp dir (the
# script finds hack/compat-exp/patches/ relative to itself), puts the stubs on
# PATH, runs observe.sh and asserts on the JSON record with jq.
# The A && B || C chains below are deliberate: ok and bad never fail.
# shellcheck disable=SC2015
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); echo "ok   $1"; }
bad() { fail=$((fail + 1)); echo "FAIL $1"; }
# check NAME CMD...: passes when the command succeeds.
check() { local name="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name"; fi; }
# jqok NAME FILE FILTER: passes when the filter prints true.
jqok() { [[ "$(jq -r "$3" "$2" 2>/dev/null)" == "true" ]] && ok "$1" || { bad "$1"; jq -c "$3" "$2" 2>&1 | head -3; }; }

# --- the fake repository and the stubs ----------------------------------------

mkdir -p "$T/repo/hack/compat-pilot" "$T/repo/hack/compat-exp/patches" "$T/bin" "$T/state" "$T/stub"
cp "$repo/hack/compat-pilot/observe.sh" "$T/repo/hack/compat-pilot/observe.sh"
cp "$repo/hack/compat-pilot/render-args.sh" "$T/repo/hack/compat-pilot/render-args.sh"
cp "$repo/hack/compat-pilot/charts.json" "$T/repo/hack/compat-pilot/charts.json"
export STUB_DIR="$T/stub"

cat >"$T/bin/helm" <<'STUB'
#!/usr/bin/env bash
echo "helm $*" >>"$STUB_DIR/helm.log"
case "$1" in
  pull)
    # helm pull CHART ... --untardir DIR: lay down the fixture chart.
    while [[ $# -gt 0 ]]; do [[ "$1" == --untardir ]] && dir="$2"; shift; done
    mkdir -p "$dir" && cp -R "$STUB_DIR/fixture-chart" "$dir/fixture-chart" ;;
  lint) exit "${STUB_LINT_RC:-0}" ;;
  install) exit "${STUB_INSTALL_RC:-0}" ;;
  test) exit "${STUB_TEST_RC:-0}" ;;
  get) cat "$STUB_DIR/hooks.txt" 2>/dev/null ;;
esac
exit 0
STUB

cat >"$T/bin/istioctl" <<'STUB'
#!/usr/bin/env bash
echo '[]'
STUB

cat >"$T/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
d="$STUB_DIR"
echo "kubectl $*" >>"$d/kubectl.log"
a=" $* "
serve() { if [[ -f "$d/$1" ]]; then cat "$d/$1"; else echo '{"items":[]}'; fi; }
case "$a" in
  *" config get-contexts "*) echo "${STUB_CONTEXT:-kind-mmc-w0}" ;;
  *" get pods -o json "*)
    n=$(($(cat "$d/pods.n" 2>/dev/null || echo 0) + 1)); echo "$n" >"$d/pods.n"
    [[ "$n" != "${STUB_PODS_FAIL_AT:-}" ]] || { echo "the server is gone" >&2; exit 1; }
    if [[ -f "$d/pods.$n.json" ]]; then cat "$d/pods.$n.json"; else serve pods.json; fi ;;
  *" get events "*) echo '{"items":[]}' ;;
  *" get jobs "*) serve jobs.json ;;
  *" get svc "*) serve svc.json ;;
  *" get validatingwebhookconfigurations "*) serve validating.json ;;
  *" get mutatingwebhookconfigurations "*) serve mutating.json ;;
  *" get apiservices "*) serve apiservices.json ;;
  *" get --raw "*)
    while [[ $# -gt 0 ]]; do [[ "$1" == --raw ]] && p="$2"; shift; done
    f="$d/raw/$(sed -n 's|.*/services/\([^/]*\)/proxy/.*|\1|p' <<<"$p")"
    if [[ ! -f "$f" ]]; then echo "Error from server (NotFound): no stub for $p" >&2; exit 1; fi
    rc="$(head -1 "$f")"
    if [[ "$rc" == 0 ]]; then tail -n +2 "$f"; else tail -n +2 "$f" >&2; fi
    exit "$rc" ;;
  *" apply "*) cat >/dev/null ;;
esac
exit 0
STUB
chmod +x "$T/bin/"*

# --- helpers --------------------------------------------------------------------

# reset: clear the stub data between tests.
reset() {
  rm -rf "$T/stub" "$T/out" "$T/patches-made"
  mkdir -p "$T/stub/raw" "$T/out"
  rm -f "$T/repo/hack/compat-exp/patches/"*
  unset STUB_INSTALL_RC STUB_LINT_RC STUB_TEST_RC STUB_PODS_FAIL_AT
}

# world_files WORLD: what world.sh would have left behind.
world_files() {
  local c; c="mmc-$(tr '[:upper:]' '[:lower:]' <<<"$1")"
  : >"$T/state/$c.kubeconfig"
  printf 'istio=1.31.3\nkubernetes=1.35\n' >"$T/state/$c.world"
}

# run_observe CHARTS_FILE WORLD ID RUN: rc in $rc, record in $rec if written.
run_observe() {
  local cf="$1" world="$2" id="$3" run="$4"
  world_files "$world"
  rec="$T/out/$id.$world.run$run.json"
  env PATH="$T/bin:$PATH" STUB_CONTEXT="kind-mmc-$(tr '[:upper:]' '[:lower:]' <<<"$world")" \
    MMC_STATE="$T/state" MMC_SETTLE=0 MMC_LATE="${TEST_LATE:-0}" ${cf:+CHARTS_FILE="$cf"} \
    bash "${OBSERVE:-$T/repo/hack/compat-pilot/observe.sh}" "$world" "$id" "$run" "$T/out" \
    >"$T/stdout" 2>"$T/stderr"
  rc=$?
}

# A chart "fixture-chart" with one template, and the diffs against it.
make_fixture_chart() {
  mkdir -p "$T/stub/fixture-chart/templates"
  printf 'apiVersion: v2\nname: fixture-chart\nversion: 1.0.0\n' >"$T/stub/fixture-chart/Chart.yaml"
  printf 'replicas: 1\nextra: false\n' >"$T/stub/fixture-chart/values.yaml"
  printf 'kind: ConfigMap\n' >"$T/stub/fixture-chart/templates/cm.yaml"
}
good_patch='--- a/values.yaml
+++ b/values.yaml
@@ -1,2 +1,2 @@
 replicas: 1
-extra: false
+extra: true
'
bad_patch='--- a/values.yaml
+++ b/values.yaml
@@ -1,2 +1,2 @@
 replicas: 9
-extra: nope
+extra: true
'

# pod JSON helpers
# pod NAME OWNERKIND OWNERNAME EXTRA_JQ: one running pod, adjusted by a jq filter.
pod() {
  jq -nc --arg n "$1" --arg ok "$2" --arg on "$3" '{
    metadata: {name: $n, labels: {}, annotations: {}, ownerReferences: [{kind: $ok, name: $on}]},
    spec: {containers: [{name: "app"}]},
    status: {phase: "Running", conditions: [{type: "Ready", status: "True"}],
             containerStatuses: [{name: "app", ready: true, restartCount: 0, state: {running: {}}, image: "img"}]}
  }' | jq -c "${4:-.}"
}
# pods_file FILE POD...
pods_file() { local f="$1"; shift; printf '%s\n' "$@" | jq -s '{items: .}' >"$f"; }

charts_one() { # FILE with a single chart entry given as JSON
  printf '[%s]\n' "$2" >"$1"
}

# --- item 3: CHARTS_FILE and the patch fix form -----------------------------------

echo "# item 3"

# The default is the pilot's list, so the pilot workflows keep working.
reset
run_observe "" W0 podinfo 1
[[ $rc -eq 0 ]] && jqok "default CHARTS_FILE reads the pilot list" "$rec" '.chart == "podinfo" and .chartVersion == "6.15.0"' ||
  bad "default CHARTS_FILE reads the pilot list (rc=$rc)"
check "default run installs from the pinned chart version" grep -q -- "install podinfo oci://ghcr.io/stefanprodan/charts/podinfo .*--version 6.15.0" "$T/stub/helm.log"
jqok "no patch recorded for an unpatched chart" "$rec" '.patch == null'

# An unknown id is a usage error against the file named by CHARTS_FILE.
reset
charts_one "$T/c.json" '{"id":"a","chart":"c","version":"1"}'
run_observe "$T/c.json" W0 nope 1
[[ $rc -eq 2 ]] && ok "unknown id in CHARTS_FILE exits 2" || bad "unknown id in CHARTS_FILE exits 2 (rc=$rc)"

# A values fix, with CHARTS_FILE pointing elsewhere.
reset
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"oci://example/app","version":"2.0.0","values":["--set","x=1"]}'
run_observe "$T/c.json" W0 app-fix 1
[[ $rc -eq 0 ]] && jqok "values fix: record names the fixed release" "$rec" '.chart == "app-fix" and .fixOf == "app" and .patch == null' || bad "values fix (rc=$rc)"
check "values fix: install uses the release of the chart it fixes, with the values" grep -q -- "install app oci://example/app .*--version 2.0.0.*--set x=1" "$T/stub/helm.log"
check "values fix: no helm pull" bash -c "! grep -q '^helm pull' '$T/stub/helm.log'"

# A patch fix that applies.
reset
make_fixture_chart
printf '%s' "$good_patch" >"$T/repo/hack/compat-exp/patches/ok.diff"
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"oci://example/app","version":"2.0.0","patch":"hack/compat-exp/patches/ok.diff"}'
run_observe "$T/c.json" W0 app-fix 1
want_sha="$(shasum -a 256 "$T/repo/hack/compat-exp/patches/ok.diff" | cut -d' ' -f1)"
[[ $rc -eq 0 ]] && jqok "patch fix: sha256, line count and changed lines recorded" "$rec" \
  ".patch == {path: \"hack/compat-exp/patches/ok.diff\", sha256: \"$want_sha\", lines: 6, changedLines: 2}" || bad "patch fix applies (rc=$rc: $(cat "$T/stderr"))"
check "patch fix: helm pull --untar of the pinned version" grep -q -- "^helm pull oci://example/app --version 2.0.0 --untar --untardir" "$T/stub/helm.log"
check "patch fix: helm lint called on the patched directory" grep -q "^helm lint .*fixture-chart" "$T/stub/helm.log"
check "patch fix: installed from the patched directory under the fixed release name" grep -q "^helm install app .*/fixture-chart --namespace pilot" "$T/stub/helm.log"
check "patch fix: the install has no --version for a directory" bash -c "! grep '^helm install' '$T/stub/helm.log' | grep -q -- --version"

# A patch that does not apply fails the run loudly, before any namespace exists.
reset
make_fixture_chart
printf '%s' "$bad_patch" >"$T/repo/hack/compat-exp/patches/bad.diff"
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"oci://example/app","version":"2.0.0","patch":"hack/compat-exp/patches/bad.diff"}'
run_observe "$T/c.json" W0 app-fix 1
[[ $rc -eq 3 && ! -f "$rec" ]] && grep -q "does not apply" "$T/stderr" && ok "patch that does not apply: exit 3, no record, loud message" ||
  bad "patch that does not apply (rc=$rc)"
check "patch that does not apply: no install, no namespace" bash -c "! grep -q '^helm install' '$T/stub/helm.log' && ! grep -q 'create namespace' '$STUB_DIR/kubectl.log'"

# helm lint failing on the patched chart also fails the run.
reset
make_fixture_chart
printf '%s' "$good_patch" >"$T/repo/hack/compat-exp/patches/ok.diff"
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"oci://example/app","version":"2.0.0","patch":"hack/compat-exp/patches/ok.diff"}'
STUB_LINT_RC=1 run_observe "$T/c.json" W0 app-fix 1
[[ $rc -eq 3 && ! -f "$rec" ]] && grep -q "lint" "$T/stderr" && ok "helm lint failure: exit 3, no record" || bad "helm lint failure (rc=$rc)"
export -n STUB_LINT_RC

# A patch outside hack/compat-exp/patches/ is refused, in each spelling.
for p in "hack/compat-pilot/x.diff" "/etc/passwd" "hack/compat-exp/patches/../../compat-pilot/charts.json" "hack/compat-exp/patchesx/a.diff" "hack/compat-exp/patches/"; do
  reset
  make_fixture_chart
  printf '%s' "$good_patch" >"$T/repo/hack/compat-pilot/x.diff"
  jq -nc --arg p "$p" '[{id:"app-fix",fixOf:"app",chart:"oci://example/app",version:"2.0.0",patch:$p}]' >"$T/c.json"
  run_observe "$T/c.json" W0 app-fix 1
  [[ $rc -eq 2 && ! -f "$rec" ]] && ok "patch path refused: $p" || bad "patch path refused: $p (rc=$rc)"
done
check "refused patch path never reaches helm" bash -c "! test -f '$STUB_DIR/helm.log'"

# A patch over 40 changed lines is refused; exactly 40 is accepted.
mkpatch() { # N: a diff against a values.yaml of N lines changing all of them
  local n="$1" i
  { echo '--- a/values.yaml'; echo '+++ b/values.yaml'; echo "@@ -1,$n +1,$n @@"
    for ((i = 1; i <= n; i++)); do echo "-l$i"; done
    for ((i = 1; i <= n; i++)); do echo "+m$i"; done; }
}
reset
make_fixture_chart
seq 1 21 | sed 's/^/l/' >"$T/stub/fixture-chart/values.yaml"
mkpatch 21 >"$T/repo/hack/compat-exp/patches/big.diff"
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"c","version":"1","patch":"hack/compat-exp/patches/big.diff"}'
run_observe "$T/c.json" W0 app-fix 1
[[ $rc -eq 2 && ! -f "$rec" ]] && grep -q "42 lines" "$T/stderr" && ok "patch over 40 changed lines (42) refused" || bad "patch over 40 changed lines (rc=$rc)"
reset
make_fixture_chart
seq 1 20 | sed 's/^/l/' >"$T/stub/fixture-chart/values.yaml"
mkpatch 20 >"$T/repo/hack/compat-exp/patches/edge.diff"
charts_one "$T/c.json" '{"id":"app-fix","fixOf":"app","chart":"c","version":"1","patch":"hack/compat-exp/patches/edge.diff"}'
run_observe "$T/c.json" W0 app-fix 1
[[ $rc -eq 0 ]] && jqok "patch of exactly 40 changed lines accepted" "$rec" '.patch.changedLines == 40' || bad "patch of exactly 40 lines (rc=$rc: $(cat "$T/stderr"))"

# --- item 4: per-pod mesh fields and capacityPending -------------------------------

echo "# item 4"
charts_one "$T/c4.json" '{"id":"app","chart":"oci://example/app","version":"1.0.0"}'

# A pod that opts out, a pod with a regular proxy, an init-container proxy, a
# native sidecar, ambient redirection, and a plain pod.
reset
pods_file "$T/stub/pods.json" \
  "$(pod plain-0 StatefulSet plain)" \
  "$(pod optout-1 Pod optout '.metadata.labels["sidecar.istio.io/inject"] = "false" | .metadata.annotations["istio.io/dataplane-mode"] = "none" | .metadata.labels["app"] = "x"')" \
  "$(pod proxy-2 Pod proxy '.spec.containers += [{name: "istio-proxy"}] | .spec.initContainers = [{name: "istio-init"}] | .metadata.annotations["sidecar.istio.io/status"] = "{}"')" \
  "$(pod native-3 Pod native '.spec.initContainers = [{name: "istio-proxy", restartPolicy: "Always"}]')" \
  "$(pod amb-4 Pod amb '.metadata.annotations["ambient.istio.io/redirection"] = "enabled"')" \
  "$(pod mmc-client Pod mmc-client '.metadata.labels["mmc-client"] = "true"')"
run_observe "$T/c4.json" W1 app 1
[[ $rc -eq 0 ]] || bad "item 4 run (rc=$rc: $(cat "$T/stderr"))"
jqok "every pod is listed, client flagged" "$rec" '(.podMesh | length) == 6 and ([.podMesh[] | select(.client)] | length) == 1'
jqok "pod with no mesh metadata: empty list, no proxy, no redirection" "$rec" \
  '.podMesh[] | select(.pod == "plain-0") | .meshMetadata == [] and .istioProxy == {container: false, initContainer: false, native: false} and .ambientRedirection == ""'
jqok "opt-out label and annotation recorded as raw key and value, other labels left out" "$rec" \
  '.podMesh[] | select(.pod == "optout-1") | .meshMetadata == [{kind: "label", key: "sidecar.istio.io/inject", value: "false"}, {kind: "annotation", key: "istio.io/dataplane-mode", value: "none"}]'
jqok "pod with an istio-proxy container" "$rec" \
  '.podMesh[] | select(.pod == "proxy-2") | .istioProxy == {container: true, initContainer: false, native: false}'
jqok "pod with an istio-proxy init container (native sidecar)" "$rec" \
  '.podMesh[] | select(.pod == "native-3") | .istioProxy == {container: false, initContainer: true, native: true}'
jqok "ambient redirection annotation reported" "$rec" \
  '.podMesh[] | select(.pod == "amb-4") | .ambientRedirection == "enabled" and .meshMetadata == [{kind: "annotation", key: "ambient.istio.io/redirection", value: "enabled"}]'
jqok "no capacity problem: capacityPending is an empty list" "$rec" '.capacityPending == []'

# capacityPending: true cases and look-alikes that must not count.
unsched() { # MESSAGE [PHASE] [REASON] [STATUS]
  pod "pend-$RANDOM" Pod pend '.status.phase = "'"${2:-Pending}"'" | .status.conditions = [{type: "PodScheduled", status: "'"${4:-False}"'", reason: "'"${3:-Unschedulable}"'", message: ""}]' |
    jq -c --arg m "$1" '.status.conditions[0].message = $m'
}
cap_case() { # NAME EXPECTED-COUNT POD...
  local name="$1" want="$2"; shift 2
  reset
  pods_file "$T/stub/pods.json" "$@"
  run_observe "$T/c4.json" W0 app 1
  [[ $rc -eq 0 ]] && jqok "$name" "$rec" ".capacityPending | length == $want" || bad "$name (rc=$rc)"
}
cap_case "capacityPending: Insufficient cpu" 1 "$(pod ok-0 Pod ok)" "$(unsched '0/1 nodes are available: 1 Insufficient cpu.')"
cap_case "capacityPending: Insufficient memory" 1 "$(unsched '0/3 nodes are available: 3 Insufficient memory. preemption: 0/3')"
cap_case "capacityPending: not when unschedulable for another reason (taint)" 0 "$(unsched '0/1 nodes are available: 1 node(s) had untolerated taint.')"
cap_case "capacityPending: not when the reason is not Unschedulable" 0 "$(unsched 'Insufficient cpu' Pending SchedulerError)"
cap_case "capacityPending: not when PodScheduled is True" 0 "$(unsched 'Insufficient cpu' Pending Unschedulable True)"
cap_case "capacityPending: not when the pod is not Pending" 0 "$(unsched 'Insufficient cpu' Running)"
reset
pods_file "$T/stub/pods.json" "$(unsched '0/1 nodes are available: 1 Insufficient cpu.')"
run_observe "$T/c4.json" W0 app 1
jqok "capacityPending entry names the pod and the scheduler message" "$rec" \
  '.capacityPending[0] | (.pod | startswith("pend-")) and (.message | test("Insufficient cpu"))'
jqok "capacityPending is at the top level like pullFailures, both arrays" "$rec" \
  '(.capacityPending | type) == "array" and (.pullFailures | type) == "array"'

# --- item 5: E1 (the API server as a caller) and E2 (the late snapshot) ------------

echo "# item 5"
charts_one "$T/c5.json" '{"id":"app","chart":"oci://example/app","version":"1.0.0"}'

# webhook_fixture KIND SERVICE [PORT]: a webhook configuration pointing to a
# Service in the release namespace (no port field when PORT is empty).
webhook_fixture() {
  local file="$1" name="$2" ns="${4:-pilot}" port="${3:-}"
  jq -nc --arg n "$name" --arg ns "$ns" --arg p "$port" \
    '{items: [{metadata: {name: $n}, webhooks: [{clientConfig: {service: ({namespace: $ns, name: $n} + (if $p == "" then {} else {port: ($p | tonumber)} end))}}]}]}' \
    >"$T/stub/$file"
}
# raw_reply SERVICE PORT RC TEXT: what the service proxy answers.
raw_reply() { printf '%s\n%s\n' "$3" "$4" >"$T/stub/raw/https:$1:$2"; }
e1_value() { jq -r --arg k "$1" '.obs[$k] // "ABSENT"' "$rec"; }
only_default_pods() { pods_file "$T/stub/pods.json" "$(pod app-0 StatefulSet app)"; }

# reachable: the backend answered, whatever the status.
e1_case() { # NAME EXPECTED RC TEXT
  reset; only_default_pods
  webhook_fixture validating.json hook 9443
  raw_reply hook 9443 "$3" "$4"
  run_observe "$T/c5.json" W3 app 1
  [[ $rc -eq 0 && "$(e1_value apiserver:svc:hook:9443:reachable)" == "$2" ]] && ok "$1" ||
    bad "$1 (rc=$rc, value=$(e1_value apiserver:svc:hook:9443:reachable))"
}
e1_case "E1 reachable: 2xx" true 0 "ok"
e1_case "E1 reachable: backend 404" true 1 'Error from server (NotFound): the server could not find the requested resource (get services https:hook:9443)'
e1_case "E1 reachable: backend 403" true 1 'Error from server (Forbidden): services "https:hook:9443" is forbidden: User cannot get'
e1_case "E1 reachable: backend 503 of its own" true 1 'Error from server (ServiceUnavailable): the server is currently unable to handle the request (get services https:hook:9443)'
e1_case "E1 not reachable: error trying to reach service" false 1 'Error from server (ServiceUnavailable): error trying to reach service: read tcp 10.0.0.1:44444->10.0.0.2:9443: read: connection reset by peer'
e1_case "E1 not reachable: no endpoints" false 1 'Error from server (ServiceUnavailable): no endpoints available for service "https:hook:9443"'
e1_case "E1 not reachable: API server timeout" false 1 'Error from server (Timeout): Timeout: request did not complete within requested timeout - context deadline exceeded'
e1_case "E1 not reachable: Service missing" false 1 'Error from server (NotFound): services "hook" not found'

# a mutating webhook, the default port, and two webhooks sharing one Service port
reset; only_default_pods
webhook_fixture mutating.json mut ""
raw_reply mut 443 0 ok
run_observe "$T/c5.json" W3 app 1
[[ "$(e1_value apiserver:svc:mut:443:reachable)" == true ]] && ok "E1 mutating webhook, port defaults to 443" || bad "E1 mutating webhook default port"
reset; only_default_pods
webhook_fixture validating.json hook 9443; webhook_fixture mutating.json hook 9443
raw_reply hook 9443 0 ok
run_observe "$T/c5.json" W0 app 1
jqok "E1 one key per Service port, however many webhooks name it" "$rec" '[.obs | keys[] | select(startswith("apiserver:"))] == ["apiserver:svc:hook:9443:reachable"]'
check "E1 the proxy URL is the documented service proxy path" grep -q -- "get --raw /api/v1/namespaces/pilot/services/https:hook:9443/proxy/" "$T/stub/kubectl.log"

# a webhook pointing at a Service in another namespace is not the release's
reset; only_default_pods
webhook_fixture validating.json istio-sidecar-injector 443 istio-system
run_observe "$T/c5.json" W1 app 1
jqok "E1 webhook of another namespace is ignored: no apiserver: keys" "$rec" '[.obs | keys[] | select(startswith("apiserver:"))] == []'

# APIService: reachability of its Service and its Available condition
reset; only_default_pods
cat >"$T/stub/apiservices.json" <<'JSON'
{"items":[
 {"metadata":{"name":"v1beta1.up.example.io"},"spec":{"service":{"namespace":"pilot","name":"up","port":8443}},
  "status":{"conditions":[{"type":"Available","status":"True"}]}},
 {"metadata":{"name":"v1beta1.down.example.io"},"spec":{"service":{"namespace":"pilot","name":"down"}},
  "status":{"conditions":[{"type":"Available","status":"False","reason":"FailedDiscoveryCheck"}]}},
 {"metadata":{"name":"v1beta1.nocond.example.io"},"spec":{"service":{"namespace":"pilot","name":"nocond"}}},
 {"metadata":{"name":"v1.core"},"spec":{"service":null}},
 {"metadata":{"name":"v1.elsewhere.example.io"},"spec":{"service":{"namespace":"other","name":"x"}}}]}
JSON
raw_reply up 8443 0 ok
raw_reply down 443 1 'Error from server (ServiceUnavailable): no endpoints available for service "https:down:443"'
raw_reply nocond 443 0 ok
run_observe "$T/c5.json" W3 app 1
[[ $rc -eq 0 ]] || bad "APIService run (rc=$rc: $(cat "$T/stderr"))"
jqok "E1 APIService Available True" "$rec" '.obs["apiserver:apiservice:v1beta1.up.example.io:available"] == "true"'
jqok "E1 APIService unavailable (False)" "$rec" '.obs["apiserver:apiservice:v1beta1.down.example.io:available"] == "false"'
jqok "E1 APIService without an Available condition is unknown" "$rec" '.obs["apiserver:apiservice:v1beta1.nocond.example.io:available"] == "unknown"'
jqok "E1 APIService Service reachability and its absence" "$rec" \
  '.obs["apiserver:svc:up:8443:reachable"] == "true" and .obs["apiserver:svc:down:443:reachable"] == "false"'
jqok "E1 APIServices of other namespaces and local ones are ignored" "$rec" \
  '[.obs | keys[] | select(startswith("apiserver:"))] | length == 6'

# a release with no webhooks and no APIServices has no apiserver: key at all
reset; only_default_pods
run_observe "$T/c5.json" W3 app 1
jqok "E1 no webhooks: no apiserver: keys" "$rec" '[.obs | keys[] | select(startswith("apiserver:"))] == [] and .apiserver == {services: [], apiServices: []}'
check "E1 no webhooks: no service proxy request was made" bash -c "! grep -q -- '--raw' '$STUB_DIR/kubectl.log'"

# the API server itself being unreachable says nothing about the chart
reset; only_default_pods
webhook_fixture validating.json hook 9443
raw_reply hook 9443 1 'Unable to connect to the server: dial tcp 127.0.0.1:6443: connect: connection refused'
run_observe "$T/c5.json" W0 app 1
[[ $rc -eq 3 && ! -f "$rec" ]] && ok "E1 API server not contactable: exit 3, no record" || bad "E1 API server not contactable (rc=$rc)"

# E2: the second snapshot is a new set of keys and the first is untouched
reset
pods_file "$T/stub/pods.1.json" "$(pod web-7d9f-abc ReplicaSet web-7d9f)" "$(pod db-0 StatefulSet db)"
pods_file "$T/stub/pods.2.json" "$(pod web-7d9f-abc ReplicaSet web-7d9f '.status.containerStatuses[0].restartCount = 2')" \
  "$(pod db-0 StatefulSet db '.status.conditions = [{type: "Ready", status: "False"}]')"
run_observe "$T/c5.json" W0 app 1
[[ $rc -eq 0 ]] || bad "E2 run (rc=$rc: $(cat "$T/stderr"))"
jqok "E2 first snapshot keeps its values" "$rec" \
  '.obs["workload:Deployment/web:ready"] == "true" and .obs["workload:Deployment/web:restarts"] == "0" and .obs["workload:StatefulSet/db:ready"] == "true"'
jqok "E2 late keys carry the second snapshot" "$rec" \
  '.obs["late:workload:Deployment/web:restarts"] == ">0" and .obs["late:workload:StatefulSet/db:ready"] == "false" and .obs["late:workload:Deployment/web:ready"] == "true"'
jqok "E2 late keys all start with late:workload: and mirror the first snapshot's keys" "$rec" \
  '([.obs | keys[] | select(startswith("workload:"))] | map("late:" + .) | sort) == ([.obs | keys[] | select(startswith("late:"))] | sort)'
jqok "E2 lateWorkloads holds the full second snapshot, workloads the first" "$rec" \
  '.lateWorkloads[0].restarts == 2 and (.workloads | map(.restarts) | add) == 0'
check "E2 pods listed twice" test "$(grep -c 'get pods -o json' "$T/stub/kubectl.log")" -eq 2

# the wait: install + MMC_LATE, not shorter
reset; only_default_pods
t0=$(date +%s)
TEST_LATE=2 run_observe "$T/c5.json" W0 app 1
t1=$(date +%s)
[[ $rc -eq 0 && $((t1 - t0)) -ge 2 ]] && ok "E2 waits until install + MMC_LATE" || bad "E2 wait (rc=$rc, took $((t1 - t0))s)"

# --- helm test pods: testPodMesh --------------------------------------------------

echo "# helm test pods"
charts_one "$T/c6.json" '{"id":"app","chart":"oci://example/app","version":"1.0.0"}'
web_pods() { # the pods before helm test: a workload and the client
  pods_file "$T/stub/pods.1.json" "$(pod web-7d9f-abc ReplicaSet web-7d9f)" "$(pod mmc-client Pod mmc-client '.metadata.labels["mmc-client"] = "true"')"
}
test_pod='pod app-test Pod app-test ".metadata.annotations[\"helm.sh/hook\"] = \"test\" | .metadata.annotations[\"helm.sh/hook-delete-policy\"] = \"before-hook-creation\" | .metadata.labels[\"sidecar.istio.io/inject\"] = \"false\" | .status.phase = \"Failed\""'
after_test() { # the pods after helm test: the same two and the test pod
  pods_file "$1" "$(pod web-7d9f-abc ReplicaSet web-7d9f)" "$(pod mmc-client Pod mmc-client '.metadata.labels["mmc-client"] = "true"')" "$(eval "$test_pod")"
}
reset; web_pods; after_test "$T/stub/pods.2.json"
echo 'helm.sh/hook: test' >"$T/stub/hooks.txt"
run_observe "$T/c6.json" W1 app 1
[[ $rc -eq 0 ]] || bad "helm test pods run (rc=$rc: $(cat "$T/stderr"))"
jqok "testPodMesh: the pod helm test created, same shape as podMesh, with its opt-out" "$rec" \
  '(.testPodMesh | length) == 1 and .testPodMesh[0].pod == "app-test" and .testPodMesh[0].client == false and .testPodMesh[0].owner == "Pod/app-test"
   and .testPodMesh[0].meshMetadata == [{kind: "label", key: "sidecar.istio.io/inject", value: "false"}]
   and .testPodMesh[0].istioProxy == {container: false, initContainer: false, native: false} and .testPodMesh[0].ambientRedirection == ""'
jqok "testPodMesh: the Helm hook annotations identify it as a test pod" "$rec" \
  '.testPodMesh[0].testHook == true and .testPodMesh[0].helmHooks == {"helm.sh/hook": "test", "helm.sh/hook-delete-policy": "before-hook-creation"} and .testPodMeshStatus == "listed"'
jqok "podMesh is the snapshot before helm test: it does not hold the test pod" "$rec" '([.podMesh[].pod] | sort) == ["mmc-client", "web-7d9f-abc"]'
jqok "helm test ran and its result is the same key as before" "$rec" '.obs["helm:test"] == "pass" and (.obs | has("workload:Pod/app-test:ready") | not)'

# helm test failed: the failure path lists pods once more for the logs, the capture is the next listing
reset; web_pods; after_test "$T/stub/pods.2.json"; after_test "$T/stub/pods.3.json"
echo 'helm.sh/hook: test' >"$T/stub/hooks.txt"
STUB_TEST_RC=1 run_observe "$T/c6.json" W1 app 1
jqok "a failed helm test: the test pod is captured too" "$rec" '.obs["helm:test"] == "fail" and (.testPodMesh | map(.pod)) == ["app-test"] and .testPodMeshStatus == "listed"'

# the test pod is already gone (hook delete policy): recorded as not listable, nothing guessed
reset; web_pods; pods_file "$T/stub/pods.2.json" "$(pod web-7d9f-abc ReplicaSet web-7d9f)" "$(pod mmc-client Pod mmc-client '.metadata.labels["mmc-client"] = "true"')"
echo 'helm.sh/hook: test' >"$T/stub/hooks.txt"
run_observe "$T/c6.json" W1 app 1
jqok "test pod already deleted: helm test ran, no pod listed, status says so" "$rec" '.obs["helm:test"] == "pass" and .testPodMesh == [] and .testPodMeshStatus == "no-new-pods"'

# the listing itself fails
reset; web_pods
echo 'helm.sh/hook: test' >"$T/stub/hooks.txt"
STUB_PODS_FAIL_AT=2 run_observe "$T/c6.json" W1 app 1
jqok "pods could not be listed after helm test: status list-failed, empty list" "$rec" '.testPodMesh == [] and .testPodMeshStatus == "list-failed" and .obs["helm:test"] == "pass"'

# no test hook: nothing ran, nothing to list, and no extra listing
reset; web_pods
run_observe "$T/c6.json" W1 app 1
jqok "no test hook: testPodMesh empty, status not-run" "$rec" '.testPodMesh == [] and .testPodMeshStatus == "not-run" and .obs["helm:test"] == "none"'
check "no test hook: pods are listed twice as before (snapshot and late)" test "$(grep -c 'get pods -o json' "$T/stub/kubectl.log")" -eq 2

# --- the pilot's observe.sh and this one agree on everything that existed ----------

echo "# compatibility with the pilot instrument"

# The reference is the script before these changes: COMPAT_BASE (default
# origin/main) names the commit to take it from. Same stubs, same inputs; every
# field and key that existed before must be identical, and the new ones are
# additions only.
base="${COMPAT_BASE:-origin/main}"
if git -C "$repo" show "$base:hack/compat-pilot/observe.sh" >"$T/observe.old.sh" 2>/dev/null &&
  ! grep -q CHARTS_FILE "$T/observe.old.sh"; then
  norm='del(.helm.durationSeconds, .patch, .podMesh, .testPodMesh, .testPodMeshStatus, .capacityPending, .lateWorkloads, .apiserver)
        | .obs |= with_entries(select(.key | test("^(apiserver|late):") | not))'
  for world in W0 W1 W3; do
    reset
    pods_file "$T/stub/pods.json" \
      "$(pod web-7d9f-abc ReplicaSet web-7d9f)" \
      "$(pod db-0 StatefulSet db '.status.containerStatuses[0] |= (.ready = false | .restartCount = 3 | .state = {waiting: {reason: "CrashLoopBackOff", message: "back-off"}})')" \
      "$(pod init-xyz Job init '.status.initContainerStatuses = [{name: "i", ready: false, restartCount: 0, image: "reg/x:1", state: {waiting: {reason: "ImagePullBackOff"}}}]')" \
      "$(pod mmc-client Pod mmc-client '.metadata.labels["mmc-client"] = "true"')"
    cat >"$T/stub/svc.json" <<'JSON'
{"items":[{"metadata":{"name":"web"},"spec":{"type":"ClusterIP","clusterIP":"10.0.0.1","ports":[{"port":80,"name":"http","protocol":"TCP"}]}}]}
JSON
    cat >"$T/stub/jobs.json" <<'JSON'
{"items":[{"metadata":{"name":"init"},"status":{"conditions":[{"type":"Complete","status":"True"}]}}]}
JSON
    echo "web-pre-hook" >"$T/stub/hooks.txt"
    webhook_fixture validating.json hook 9443
    raw_reply hook 9443 1 'Error from server (ServiceUnavailable): error trying to reach service: connection refused'
    charts_one "$T/c.json" '{"id":"app","chart":"oci://example/app","version":"1.0.0","values":["--set","a=b"]}'
    cp "$T/c.json" "$T/charts.json" # the old script reads the list next to itself
    OBSERVE="$T/observe.old.sh" run_observe "" "$world" app 1
    old_rc=$rc; cp "$rec" "$T/old.json" 2>/dev/null
    rm -f "$rec"; rm -f "$T/stub/pods.n"
    OBSERVE="$T/repo/hack/compat-pilot/observe.sh" run_observe "$T/c.json" "$world" app 1
    new_rc=$rc
    [[ $old_rc -eq 0 && $new_rc -eq 0 ]] || { bad "old and new both record in $world (old=$old_rc new=$new_rc)"; continue; }
    if [[ "$(jq -S "$norm" "$T/old.json")" == "$(jq -S "$norm" "$rec")" ]]; then
      ok "$world: every pre-existing field and key identical to the pilot's observe.sh"
    else
      bad "$world: records differ"; diff <(jq -S "$norm" "$T/old.json") <(jq -S "$norm" "$rec") | head -10
    fi
  done
else
  bad "no pre-change observe.sh at $base to compare against (set COMPAT_BASE to the commit before these changes)"
fi

echo
echo "$pass passed, $fail failed"
[[ $fail -eq 0 ]]
