#!/usr/bin/env bash
#
# Bring up one pilot world: a throwaway kind cluster in one Istio
# configuration (ADR 0001, section 1).
#
#   W0  no mesh. The oracle: whatever the chart does here is what it should do
#       everywhere else.
#   W1  sidecar, traffic redirected by the istio-init container
#   W2  sidecar, traffic redirected by the istio-cni node agent
#   W3  ambient, ztunnel
#
# Every mesh world runs mesh-wide STRICT mTLS, because PERMISSIVE hides the
# class of failure where a client outside the mesh is silently accepted.
#
# The cluster gets its own kubeconfig under $MMC_STATE (default
# ./.mmc-state). Nothing here reads or writes ~/.kube/config, and every later
# script refuses a kubeconfig whose only context is not a cluster this script
# made. That is the same line meshmedic-prove draws with --yes-inject-faults:
# a tool that installs things must not be able to do it to the wrong cluster
# by accident.
#
# Usage:
#   ISTIO_VERSION=1.x.y KIND_NODE_IMAGE=kindest/node:v1.x.y hack/compat-pilot/world.sh W3
#   hack/compat-pilot/world.sh --delete W3
set -euo pipefail

if [[ "${1:-}" == "--delete" ]]; then
  world="${2:?usage: world.sh --delete W0|W1|W2|W3}"
  cluster="mmc-$(tr '[:upper:]' '[:lower:]' <<<"$world")"
  kind delete cluster --name "$cluster"
  exit 0
fi

world="${1:?usage: world.sh W0|W1|W2|W3}"
case "$world" in W0 | W1 | W2 | W3) ;; *)
  echo "unknown world: $world" >&2
  exit 2
  ;;
esac

state="${MMC_STATE:-$PWD/.mmc-state}"
cluster="mmc-$(tr '[:upper:]' '[:lower:]' <<<"$world")"
export KUBECONFIG="$state/$cluster.kubeconfig"
mkdir -p "$state"

# A world is created, never adopted. If a cluster by this name already exists
# it is either a previous run's leftover or something we did not make, and in
# both cases its state would leak into the observations.
if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
  echo "cluster $cluster already exists; delete it first (world.sh --delete $world)" >&2
  exit 2
fi

kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" \
  ${KIND_NODE_IMAGE:+--image "$KIND_NODE_IMAGE"} --wait 180s

if [[ "$world" != W0 ]]; then
  : "${ISTIO_VERSION:?pin ISTIO_VERSION for a mesh world}"
  installed="$(istioctl version --remote=false -o json | jq -r .clientVersion.version)"
  if [[ "$installed" != "$ISTIO_VERSION" ]]; then
    echo "istioctl is $installed, the run pins $ISTIO_VERSION" >&2
    exit 2
  fi

  case "$world" in
    W1) istioctl install -y --set profile=default ;;
    W2) istioctl install -y --set profile=default --set components.cni.enabled=true ;;
    W3)
      # Ambient needs the Gateway API CRDs for waypoints; kind does not ship
      # them. W3 deploys no waypoint, but istiod expects the CRDs to exist.
      kubectl apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION:-v1.6.0}/standard-install.yaml"
      istioctl install -y --set profile=ambient
      ;;
  esac

  kubectl apply -f - <<'EOF'
apiVersion: security.istio.io/v1
kind: PeerAuthentication
metadata:
  name: default
  namespace: istio-system
spec:
  mtls:
    mode: STRICT
EOF
fi

# Recorded with every observation, so a result can never be read without the
# versions that produced it.
{
  echo "world=$world"
  echo "cluster=$cluster"
  echo "kubernetes=$(kubectl version -o json | jq -r .serverVersion.gitVersion)"
  echo "istio=${ISTIO_VERSION:-none}"
  if [[ "$world" != W0 ]]; then
    # Native sidecars decide whether a proxy can hold a Job open, so the
    # setting istiod actually runs with is part of the world's identity.
    echo "native_sidecars=$(kubectl -n istio-system get deploy istiod -o json |
      jq -r '[.spec.template.spec.containers[0].env[]? | select(.name=="ENABLE_NATIVE_SIDECARS") | .value][0] // "default"')"
  fi
  echo "created=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >"$state/$cluster.world"

cat "$state/$cluster.world"
