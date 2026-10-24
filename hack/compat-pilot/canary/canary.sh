#!/usr/bin/env bash
#
# One run of one canary scenario in one world (docs/compat-pilot-2.md): deploy
# podinfo 6.14.1 under the scenario's tool, start a canary to 6.15.0, let the
# tool hold it at 10%, then send 400 requests from a client in the same mesh
# and count which version answered.
#
# What is recorded is what the tool says (its phase, the weight it wrote into
# the route) and what the traffic did. The verdict comes later, from holding
# the ambient worlds against W1, where Istio documents these routes to work.
#
# Usage:
#   hack/compat-pilot/canary/canary.sh WORLD SCENARIO_ID RUN OUTDIR
#
# Exit codes: 0 recorded (whatever the tool did), 2 usage, 3 could not decide.
set -uo pipefail

world="${1:?usage: canary.sh WORLD SCENARIO_ID RUN OUTDIR}"
id="${2:?scenario id}"
run="${3:?run number}"
out="${4:?output dir}"

here="$(cd "$(dirname "$0")" && pwd)"
state="${MMC_STATE:-$PWD/.mmc-state}"
cluster="mmc-$(tr '[:upper:]' '[:lower:]' <<<"$world")"
export KUBECONFIG="$state/$cluster.kubeconfig"
ns=pilot
requests="${MMC_REQUESTS:-400}"
weight=10
stable=ghcr.io/stefanprodan/podinfo:6.14.1
canary=ghcr.io/stefanprodan/podinfo:6.15.0

created=
undecided() {
  echo "could not decide: $*" >&2
  if [[ -n "$created" ]]; then
    kubectl delete namespace "$ns" --wait --timeout=5m >/dev/null 2>&1 ||
      echo "namespace $ns did not delete; remove it before the next run" >&2
  fi
  exit 3
}

[[ -f "$KUBECONFIG" && -f "$state/$cluster.world" ]] || undecided "no world $world (run world.sh $world)"
contexts="$(kubectl config get-contexts -o name)"
[[ "$contexts" == "kind-$cluster" ]] || undecided "kubeconfig holds '$contexts', expected only kind-$cluster"

spec="$(jq -ce --arg id "$id" '.[] | select(.id == $id)' "$here/scenarios.json")" ||
  { echo "no scenario '$id' in scenarios.json" >&2; exit 2; }
tool="$(jq -r .tool <<<"$spec")"
router="$(jq -r .router <<<"$spec")"
toolversion="$(jq -r .toolVersion <<<"$spec")"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$out"

# --- namespace, waypoint and client -------------------------------------------

kubectl create namespace "$ns" >/dev/null || undecided "namespace $ns exists (previous run not cleaned)"
created=1
case "$world" in
  W1 | W2) kubectl label namespace "$ns" istio-injection=enabled >/dev/null ;;
  W3 | W4) kubectl label namespace "$ns" istio.io/dataplane-mode=ambient >/dev/null ;;
esac
# W4 is W3 plus a waypoint for every Service in the namespace, the place
# Istio runs L7 routing in ambient.
if [[ "$world" == W4 ]]; then
  istioctl waypoint apply -n "$ns" --enroll-namespace --wait >/dev/null || undecided "waypoint not ready"
fi

kubectl -n "$ns" apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: mmc-client
  labels:
    mmc-client: "true"
  annotations:
    proxy.istio.io/config: '{"holdApplicationUntilProxyStarts": true}'
spec:
  terminationGracePeriodSeconds: 0
  containers:
    - name: client
      image: busybox:1.36.1
      command: ["sleep", "86400"]
EOF
kubectl -n "$ns" wait pod/mmc-client --for=condition=Ready --timeout=180s >/dev/null ||
  undecided "client pod not Ready in $world"

# --- the app under the tool -------------------------------------------------

container() {
  cat <<EOF
      containers:
        - name: podinfo
          image: $stable
          ports:
            - name: http
              containerPort: 9898
          readinessProbe:
            httpGet: {path: /readyz, port: http}
          resources:
            requests: {cpu: 10m, memory: 32Mi}
EOF
}

# An object that does not exist (yet) has the status {}, so its phase reads
# "none" rather than an empty string.
get_status() {
  local j
  j="$(kubectl -n "$ns" get "$1" podinfo -o json 2>/dev/null)" || j='{}'
  jq -c '.status // {}' <<<"$j"
}

case "$tool" in
  argo-rollouts)
    host=podinfo-stable
    {
      cat <<'EOF'
apiVersion: v1
kind: Service
metadata:
  name: podinfo-stable
spec:
  selector: {app: podinfo}
  ports: [{name: http, port: 9898, targetPort: http, protocol: TCP}]
---
apiVersion: v1
kind: Service
metadata:
  name: podinfo-canary
spec:
  selector: {app: podinfo}
  ports: [{name: http, port: 9898, targetPort: http, protocol: TCP}]
---
EOF
      if [[ "$router" == virtualservice ]]; then
        cat <<'EOF'
apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: podinfo
spec:
  hosts: [podinfo-stable]
  http:
    - name: primary
      route:
        - destination: {host: podinfo-stable}
          weight: 100
        - destination: {host: podinfo-canary}
          weight: 0
---
EOF
        traffic="istio: {virtualService: {name: podinfo, routes: [primary]}}"
      else
        cat <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: podinfo
spec:
  parentRefs:
    - {group: "", kind: Service, name: podinfo-stable, port: 9898}
  rules:
    - backendRefs:
        - {name: podinfo-stable, port: 9898, weight: 100}
        - {name: podinfo-canary, port: 9898, weight: 0}
---
EOF
        traffic="plugins: {argoproj-labs/gatewayAPI: {httpRoute: podinfo, namespace: $ns}}"
      fi
      cat <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata:
  name: podinfo
spec:
  replicas: 4
  revisionHistoryLimit: 1
  selector: {matchLabels: {app: podinfo}}
  strategy:
    canary:
      stableService: podinfo-stable
      canaryService: podinfo-canary
      trafficRouting:
        $traffic
      steps:
        - setWeight: $weight
        - pause: {}
  template:
    metadata: {labels: {app: podinfo}}
    spec:
$(container)
EOF
    } >"$tmp/app.yaml"
    ready_phase=Healthy
    hold_phase=Paused
    start_canary() {
      kubectl -n "$ns" patch rollout podinfo --type=json \
        -p "[{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/image\",\"value\":\"$canary\"}]"
    }
    status() { get_status rollout; }
    ;;
  flagger)
    host=podinfo
    refs=""
    [[ "$router" == httproute ]] && refs="
    gatewayRefs:
      - {group: \"\", kind: Service, name: podinfo, port: 9898}"
    cat >"$tmp/app.yaml" <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: podinfo
spec:
  replicas: 2
  selector: {matchLabels: {app: podinfo}}
  template:
    metadata: {labels: {app: podinfo}}
    spec:
$(container)
---
apiVersion: flagger.app/v1beta1
kind: Canary
metadata:
  name: podinfo
spec:
  targetRef: {apiVersion: apps/v1, kind: Deployment, name: podinfo}
  progressDeadlineSeconds: 600
  service:
    port: 9898
    targetPort: 9898$refs
  analysis:
    interval: 15s
    threshold: 5
    maxWeight: $weight
    stepWeight: $weight
    webhooks:
      - name: hold-at-max-weight
        type: confirm-promotion
        url: http://flagger-loadtester.flagger-system/gate/halt
EOF
    ready_phase=Initialized
    # Flagger resets status.canaryWeight to 0 when it starts waiting for
    # promotion; the weight it set stays in the route, which is read below.
    hold_phase=WaitingPromotion
    start_canary() {
      kubectl -n "$ns" set image deployment/podinfo "podinfo=$canary"
    }
    status() { get_status canary; }
    ;;
esac

kubectl -n "$ns" apply -f "$tmp/app.yaml" >"$tmp/apply.log" 2>&1 || {
  cat "$tmp/apply.log" >&2
  undecided "manifests rejected"
}

# Each wait records what it ended on rather than giving up: a tool that never
# reaches its hold in one world is the observation.
wait_phase() {
  local want="$1" timeout="$2" got=""
  local deadline=$(($(date +%s) + timeout))
  while (($(date +%s) < deadline)); do
    got="$(status | jq -r '.phase // "none"')"
    [[ "$got" == "$want" ]] && break
    sleep 5
  done
  echo "$got"
}

initial="$(wait_phase "$ready_phase" 300)"
held=none
if [[ "$initial" == "$ready_phase" ]]; then
  start_canary >/dev/null || undecided "could not start the canary"
  # Give the tool a moment to notice the new revision before the first look,
  # or a phase left over from the first revision ends the wait at once.
  sleep 10
  held="$(wait_phase "$hold_phase" 300)"
fi
status >"$tmp/status.json"

# The weight the tool wrote, read from the route itself: that is what the
# data plane is given, whatever the tool's status says.
if [[ "$router" == virtualservice ]]; then
  kubectl -n "$ns" get virtualservice podinfo -o json >"$tmp/route.json" 2>/dev/null || echo '{}' >"$tmp/route.json"
  route_weight="$(jq -r '[.spec.http[0].route[]? | select(.destination.host | test("canary")) | .weight // 0][0] // "none"' "$tmp/route.json")"
  route_status=n/a
else
  kubectl -n "$ns" get httproute podinfo -o json >"$tmp/route.json" 2>/dev/null || echo '{}' >"$tmp/route.json"
  route_weight="$(jq -r '[.spec.rules[0].backendRefs[]? | select(.name | test("canary")) | .weight // 0][0] // "none"' "$tmp/route.json")"
  route_status="$(jq -r '[.status.parents[]?.conditions[]? | select(.type == "Accepted") | .status][0] // "none"' "$tmp/route.json")"
fi

# --- traffic ----------------------------------------------------------------

# One connection per request, so ztunnel, which balances connections, gets
# the same number of choices as a proxy that balances requests.
kubectl -n "$ns" exec mmc-client -c client -- sh -c "
  i=0
  while [ \$i -lt $requests ]; do
    v=\$(wget -q -T 3 -O - http://$host.$ns:9898/api/info 2>/dev/null | sed -n 's/.*\"version\": *\"\\([^\"]*\\)\".*/\\1/p' | head -1)
    echo \"\${v:-error}\"
    i=\$((i + 1))
  done" >"$tmp/answers.txt" 2>"$tmp/exec.log" || undecided "client exec failed: $(tail -1 "$tmp/exec.log")"

count() { grep -Fcx "$1" "$tmp/answers.txt"; }
n_canary="$(count "${canary##*:}")"
n_stable="$(count "${stable##*:}")"
n_total="$(wc -l <"$tmp/answers.txt" | tr -d ' ')"
n_error=$((n_total - n_canary - n_stable))

# The canary's share in three buckets, so that runs compare as values:
# at-weight is within three standard deviations of 10% of the requests
# (binomial, 40 +- 18 at 400 requests).
share="$(awk -v c="$n_canary" -v n="$n_total" -v w="$weight" 'BEGIN {
  e = n * w / 100; sd = sqrt(n * (w / 100) * (1 - w / 100))
  if (c == 0) print "zero"; else if (c >= e - 3 * sd && c <= e + 3 * sd) print "at-weight"; else print "off-weight"
}')"

# --- evidence ---------------------------------------------------------------

echo '[]' >"$tmp/analyze.json"
istioctl analyze -n "$ns" -o json 2>/dev/null |
  jq '[.[]? | {code: .code, level: .level, origin: .origin, message: .message}]' >"$tmp/analyze.json" ||
  echo '[]' >"$tmp/analyze.json"

kubectl -n "$ns" get pods -o json >"$tmp/pods.json" 2>/dev/null || echo '{"items":[]}' >"$tmp/pods.json"
jq '[.items[] | (.status.containerStatuses // []) + (.status.initContainerStatuses // [])
  | .[] | select(.state.waiting.reason == "ErrImagePull" or .state.waiting.reason == "ImagePullBackOff")
  | {owner: "", image: .image}]' "$tmp/pods.json" >"$tmp/pulls.json"

# --- record -----------------------------------------------------------------

jq -n \
  --arg world "$world" --arg chart "$id" --argjson run "$run" --arg version "$toolversion" \
  --rawfile worldinfo "$state/$cluster.world" \
  --arg initial "$initial" --arg held "$held" --arg route_weight "$route_weight" --arg route_status "$route_status" \
  --arg share "$share" --argjson n_canary "$n_canary" --argjson n_stable "$n_stable" \
  --argjson n_error "$n_error" --argjson n_total "$n_total" \
  --slurpfile status "$tmp/status.json" --slurpfile route "$tmp/route.json" \
  --slurpfile analyze "$tmp/analyze.json" --slurpfile pulls "$tmp/pulls.json" '
  {
    world: $world, chart: $chart, chartVersion: $version, run: $run, fixOf: "",
    environment: ($worldinfo | split("\n") | map(select(length > 0) | split("=") | {(.[0]): (.[1:] | join("="))}) | add),
    traffic: {canary: $n_canary, stable: $n_stable, error: $n_error, total: $n_total},
    toolStatus: $status[0], route: ($route[0] | {spec, status}),
    analyze: $analyze[0], pullFailures: $pulls[0],
    obs: {
      "tool:initial": $initial,
      "tool:held": $held,
      "route:canary-weight": $route_weight,
      "route:accepted": $route_status,
      "traffic:canary": $share,
      "traffic:errors": (if $n_error == 0 then "0" else ">0" end)
    }
  }' >"$out/$id.$world.run$run.json" || undecided "could not write the record"

echo "$id $world run$run: held=$held weight=$route_weight canary=$n_canary/$n_total errors=$n_error ($share)"

# --- clean up ---------------------------------------------------------------

kubectl delete namespace "$ns" --wait --timeout=5m >/dev/null || undecided "namespace $ns did not delete"
exit 0
