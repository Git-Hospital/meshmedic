#!/usr/bin/env bash
#
# Install the progressive-delivery controller one canary scenario needs, once
# per world, before its runs (docs/compat-pilot-2.md). The controller lives
# outside the test namespace and outside the mesh: it only talks to the API
# server and, for Flagger, to the load tester's gate.
#
# Usage:
#   hack/compat-pilot/canary/tool.sh WORLD SCENARIO_ID
set -euo pipefail

world="${1:?usage: tool.sh WORLD SCENARIO_ID}"
id="${2:?scenario id}"

here="$(cd "$(dirname "$0")" && pwd)"
state="${MMC_STATE:-$PWD/.mmc-state}"
cluster="mmc-$(tr '[:upper:]' '[:lower:]' <<<"$world")"
export KUBECONFIG="$state/$cluster.kubeconfig"
export HELM_CONFIG_HOME="$state/helm/config" HELM_CACHE_HOME="$state/helm/cache"
export HELM_REPOSITORY_CONFIG="$HELM_CONFIG_HOME/repositories.yaml"
export HELM_REPOSITORY_CACHE="$HELM_CACHE_HOME/repository"

[[ -f "$KUBECONFIG" && -f "$state/$cluster.world" ]] || { echo "no world $world (run world.sh $world)" >&2; exit 2; }
contexts="$(kubectl config get-contexts -o name)"
[[ "$contexts" == "kind-$cluster" ]] || { echo "kubeconfig holds '$contexts', expected only kind-$cluster" >&2; exit 2; }

spec="$(jq -ce --arg id "$id" '.[] | select(.id == $id)' "$here/scenarios.json")" ||
  { echo "no scenario '$id' in scenarios.json" >&2; exit 2; }
tool="$(jq -r .tool <<<"$spec")"
chart="$(jq -r .chart <<<"$spec")"
version="$(jq -r .chartVersion <<<"$spec")"
repo="$(jq -r .repo <<<"$spec")"

case "$tool" in
  argo-rollouts)
    args=()
    if jq -e .plugin <<<"$spec" >/dev/null; then
      args+=(--set-json "controller.trafficRouterPlugins=$(jq -c '[.plugin | {name, location, sha256}]' <<<"$spec")")
    fi
    helm install argo-rollouts "$chart" --repo "$repo" --version "$version" \
      --namespace argo-rollouts --create-namespace --wait --timeout 5m ${args[@]+"${args[@]}"}
    if jq -e .plugin <<<"$spec" >/dev/null; then
      # The chart's ClusterRole grants update on HTTPRoutes; the plugin's
      # installation guide asks for patch as well.
      kubectl apply -f - <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: argo-rollouts-gateway-api-plugin
rules:
  - apiGroups: [""]
    resources: [services]
    verbs: [get]
  - apiGroups: [gateway.networking.k8s.io]
    resources: [httproutes]
    verbs: [get, list, update, patch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: argo-rollouts-gateway-api-plugin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: argo-rollouts-gateway-api-plugin
subjects:
  - kind: ServiceAccount
    name: argo-rollouts
    namespace: argo-rollouts
EOF
    fi
    ;;
  flagger)
    helm install flagger "$chart" --repo "$repo" --version "$version" \
      --namespace flagger-system --create-namespace --wait --timeout 5m \
      --set meshProvider="$(jq -r .meshProvider <<<"$spec")"
    # The load tester's /gate/halt answers 403 to every call, which is how a
    # confirm-promotion hook holds the canary at its weight without metrics.
    helm install flagger-loadtester loadtester --repo "$repo" --version "${LOADTESTER_VERSION:-0.39.0}" \
      --namespace flagger-system --wait --timeout 5m
    ;;
  *)
    echo "unknown tool $tool" >&2
    exit 2
    ;;
esac

echo "tool=$tool" >>"$state/$cluster.world"
echo "tool_chart=$chart $version" >>"$state/$cluster.world"
