#!/usr/bin/env bash
#
# The reproduction quoted in the upstream report about NetworkPolicy and port
# 15008 in ambient, run as written in a W3 world (ambient, kindnet enforcing
# NetworkPolicy) made by world.sh. Exits 1 if either claim does not hold.
#
# Usage: hack/compat-pilot/repro/networkpolicy-15008.sh
set -uo pipefail

state="${MMC_STATE:-$PWD/.mmc-state}"
export KUBECONFIG="$state/mmc-w3.kubeconfig"
export HELM_CONFIG_HOME="$state/helm/config" HELM_CACHE_HOME="$state/helm/cache"
[[ "$(kubectl config get-contexts -o name)" == kind-mmc-w3 ]] || { echo "no W3 world (run world.sh W3)" >&2; exit 2; }

fail=0

# --- the report's commands, unchanged ---
kubectl create namespace np-demo
kubectl label namespace np-demo istio.io/dataplane-mode=ambient
helm install redis oci://registry-1.docker.io/bitnamicharts/redis --version 28.3.9 -n np-demo --wait --timeout 5m
first=$?
# times out: redis-replicas in CrashLoopBackOff
helm uninstall redis -n np-demo
helm install redis oci://registry-1.docker.io/bitnamicharts/redis --version 28.3.9 -n np-demo --wait --timeout 5m \
  --set-json 'networkPolicy.extraIngress=[{"ports":[{"port":15008,"protocol":"TCP"}]}]'
second=$?
# installs
# --- end ---

kubectl -n np-demo get pods -o wide
if [[ $first -ne 0 ]]; then echo "ok:   default install fails (rc=$first)"; else echo "FAIL: default install succeeded"; fail=1; fi
if [[ $second -eq 0 ]]; then echo "ok:   install with the 15008 rule succeeds"; else echo "FAIL: install with the 15008 rule failed (rc=$second)"; fail=1; fi

kubectl delete namespace np-demo --wait=false >/dev/null
exit "$fail"
