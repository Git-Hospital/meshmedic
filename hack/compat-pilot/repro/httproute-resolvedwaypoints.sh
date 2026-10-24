#!/usr/bin/env bash
#
# The reproduction quoted in the upstream report about HTTPRoute's
# ResolvedWaypoints condition, run as written in a W3 world (ambient, no
# waypoint) made by world.sh. It prints what the report claims and exits 1 if
# any claim does not hold.
#
# Usage: hack/compat-pilot/repro/httproute-resolvedwaypoints.sh
set -uo pipefail

state="${MMC_STATE:-$PWD/.mmc-state}"
export KUBECONFIG="$state/mmc-w3.kubeconfig"
[[ "$(kubectl config get-contexts -o name)" == kind-mmc-w3 ]] || { echo "no W3 world (run world.sh W3)" >&2; exit 2; }

fail=0
# check LABEL COMMAND...: one claim of the report, tested by running COMMAND.
check() {
  local label="$1"
  shift
  if "$@"; then echo "ok:   $label"; else echo "FAIL: $label"; fail=1; fi
}
# shellcheck disable=SC2329 # called through check
contains() { grep -qi -- "$2" <<<"$1"; }
# shellcheck disable=SC2329 # called through check
lacks() { ! grep -qi -- "$2" <<<"$1"; }

# --- the report's commands, unchanged ---
kubectl create namespace demo
kubectl label namespace demo istio.io/dataplane-mode=ambient
kubectl -n demo create deployment podinfo --image=ghcr.io/stefanprodan/podinfo:6.15.0 --port=9898
kubectl -n demo expose deployment podinfo --port=9898
kubectl -n demo apply -f - <<'YAML'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: podinfo
spec:
  parentRefs:
    - {group: "", kind: Service, name: podinfo, port: 9898}
  rules:
    - backendRefs:
        - {name: podinfo, port: 9898}
YAML
# --- end ---

# istiod writes the status asynchronously.
cond=""
for _ in $(seq 30); do
  cond="$(kubectl -n demo get httproute podinfo -o json | jq -c '.status.parents[0].conditions[]? | select(.type == "ResolvedWaypoints")')"
  [[ -n "$cond" ]] && break
  sleep 2
done
echo "condition: $cond"
analyze="$(istioctl analyze -n demo 2>&1)"
echo "istioctl analyze: $analyze"

check "status is True" test "$(jq -r .status <<<"$cond")" = True
check "reason is ResolvedWaypoints" test "$(jq -r .reason <<<"$cond")" = ResolvedWaypoints
check "message says the route will not be respected" contains "$(jq -r .message <<<"$cond")" "route will not be respected"
check "analyze says nothing about waypoints" lacks "$analyze" "waypoint"

# The contrast the report gives: with a waypoint the message is the success one.
istioctl waypoint apply -n demo --enroll-namespace --wait
msg=""
for _ in $(seq 30); do
  msg="$(kubectl -n demo get httproute podinfo -o json | jq -r '.status.parents[0].conditions[]? | select(.type == "ResolvedWaypoints") | .message')"
  [[ "$msg" == "All waypoints resolved" ]] && break
  sleep 2
done
check "with a waypoint: All waypoints resolved" test "$msg" = "All waypoints resolved"

kubectl delete namespace demo --wait=false >/dev/null
exit "$fail"
