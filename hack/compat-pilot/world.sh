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
#   W4  ambient, installed like W3; the second pilot gives each test namespace
#       a waypoint (canary.sh), so L7 features have somewhere to run
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
# Registry mirror (compat experiment, docs/compat-experiment.md section 5):
# MMC_REGISTRY_MIRROR=1 gives the cluster's containerd a docker.io mirror,
# mirror.gcr.io first and registry-1.docker.io as the fallback, identically in
# every world, W0 included. Unset or 0 leaves the cluster exactly as the pilot
# made it: no kind config file, no extra line in the world file. When on, the
# world file gets a registry_mirror line naming the endpoints.
# MMC_DRY_RUN=1 prints the kind command and config that would be used and
# exits before touching kind, kubectl or the state directory.
#
# Usage:
#   ISTIO_VERSION=1.x.y KIND_NODE_IMAGE=kindest/node:v1.x.y hack/compat-pilot/world.sh W3
#   hack/compat-pilot/world.sh --delete W3
set -euo pipefail

if [[ "${1:-}" == "--delete" ]]; then
  world="${2:?usage: world.sh --delete W0|W1|W2|W3|W4}"
  cluster="mmc-$(tr '[:upper:]' '[:lower:]' <<<"$world")"
  kind delete cluster --name "$cluster"
  exit 0
fi

world="${1:?usage: world.sh W0|W1|W2|W3|W4}"
case "$world" in W0 | W1 | W2 | W3 | W4) ;; *)
  echo "unknown world: $world" >&2
  exit 2
  ;;
esac

state="${MMC_STATE:-$PWD/.mmc-state}"
cluster="mmc-$(tr '[:upper:]' '[:lower:]' <<<"$world")"
export KUBECONFIG="$state/$cluster.kubeconfig"
# The docker.io mirror. containerd reads /etc/containerd/certs.d/docker.io/
# hosts.toml: hosts are tried in order, then `server` as the fallback. The
# directory is mounted from the host into every node; config_path is set
# explicitly so the lookup also works on node images that do not default it.
mirror_endpoints="https://mirror.gcr.io,https://registry-1.docker.io"
case "${MMC_REGISTRY_MIRROR:-0}" in
  0 | "") mirror=0 ;;
  1) mirror=1 ;;
  *)
    echo "MMC_REGISTRY_MIRROR must be 0 or 1" >&2
    exit 2
    ;;
esac
case "$state" in /*) state_abs="$state" ;; *) state_abs="$PWD/$state" ;; esac

mirror_hosts_toml() {
  cat <<'EOF'
server = "https://registry-1.docker.io"

[host."https://mirror.gcr.io"]
  capabilities = ["pull", "resolve"]
EOF
}

# $1 is the absolute host directory that holds docker.io/hosts.toml.
kind_config() {
  cat <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
nodes:
- role: control-plane
  extraMounts:
  - hostPath: $1/docker.io
    containerPath: /etc/containerd/certs.d/docker.io
    readOnly: true
EOF
}

kind_args=(create cluster --name "$cluster" --kubeconfig "$KUBECONFIG")
if [[ "$mirror" == 1 ]]; then
  kind_args+=(--config "$state_abs/$cluster.kind.yaml")
fi
kind_args+=(${KIND_NODE_IMAGE:+--image "$KIND_NODE_IMAGE"} --wait 180s)

if [[ "${MMC_DRY_RUN:-}" == 1 ]]; then
  echo "kind ${kind_args[*]}"
  if [[ "$mirror" == 1 ]]; then
    echo "--- kind config"
    kind_config "$state_abs/registry-mirror"
    echo "--- docker.io/hosts.toml"
    mirror_hosts_toml
  fi
  exit 0
fi

mkdir -p "$state"

# A world is created, never adopted. If a cluster by this name already exists
# it is either a previous run's leftover or something we did not make, and in
# both cases its state would leak into the observations.
if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
  echo "cluster $cluster already exists; delete it first (world.sh --delete $world)" >&2
  exit 2
fi

if [[ "$mirror" == 1 ]]; then
  mkdir -p "$state/registry-mirror/docker.io"
  mirror_hosts_toml >"$state/registry-mirror/docker.io/hosts.toml"
  kind_config "$state_abs/registry-mirror" >"$state/$cluster.kind.yaml"
fi

kind "${kind_args[@]}"

if [[ "$world" != W0 ]]; then
  : "${ISTIO_VERSION:?pin ISTIO_VERSION for a mesh world}"
  installed="$(istioctl version --remote=false -o json | jq -r .clientVersion.version)"
  if [[ "$installed" != "$ISTIO_VERSION" ]]; then
    echo "istioctl is $installed, the run pins $ISTIO_VERSION" >&2
    exit 2
  fi

  # Ambient needs the Gateway API CRDs for waypoints; kind does not ship
  # them. W3 deploys no waypoint, but istiod expects the CRDs to exist. The
  # second pilot routes with HTTPRoute in the sidecar world too
  # (MMC_GATEWAY_API=1). They go in before istiod, as they always have in W3.
  if [[ "$world" == W3 || "$world" == W4 || "${MMC_GATEWAY_API:-}" == 1 ]]; then
    kubectl apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION:-v1.6.0}/standard-install.yaml"
  fi
  case "$world" in
    W1) istioctl install -y --set profile=default ;;
    W2) istioctl install -y --set profile=default --set components.cni.enabled=true ;;
    W3 | W4) istioctl install -y --set profile=ambient ;;
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
  if [[ "$mirror" == 1 ]]; then
    echo "registry_mirror=docker.io:$mirror_endpoints"
  fi
  echo "created=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >"$state/$cluster.world"

cat "$state/$cluster.world"
