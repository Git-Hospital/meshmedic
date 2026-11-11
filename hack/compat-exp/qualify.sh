#!/usr/bin/env bash
#
# Qualify one candidate chart for the compat experiment (docs/compat-experiment.md
# section 4, "Frame and qualification"): in a W0 world, at the pinned version,
# with defaults only,
#
#   1. `helm template` succeeds,
#   2. a server-side dry-run of the rendered manifests succeeds,
#   3. three of three installs become Ready within 15 minutes, without an image
#      pull error.
#
# The duration of every install is recorded; select.sh turns the slowest one
# into the experiment's install deadline, max(10 min, 3 x slowest).
#
# Ready is the observe.sh definition, not a second one. observe.sh records a
# workload as ready when every pod of it has the Ready condition or has
# Succeeded, and it counts an install as done when `helm install --wait
# --wait-for-jobs` returns 0. Here an install is Ready when
#   - helm install --wait --wait-for-jobs returns 0 within the deadline, and
#   - after the same settle wait observe.sh uses (MMC_SETTLE, 30 s), every
#     workload group in the pod list (observe.sh's own jq program, copied
#     verbatim and checked against observe.sh by test/qualify_test.sh) is
#     ready or Succeeded.
# Job pods are left to --wait-for-jobs, as in observe.sh.
#
# Gate-bias fields (section 4) are computed from the rendered manifests, so
# they exist for excluded charts too, whenever the chart rendered at all:
#   hasStatefulSet       a StatefulSet in the render
#   hasNetworkPolicy     a NetworkPolicy in the render
#   hasNonHttpPort       a Service port that is not HTTP, defined below
#   hasHeadlessService   a Service with clusterIP None
# A chart whose template step failed has no render: the four fields are null.
#
# Non-HTTP port, mechanically. For each port of each Service that is not
# ExternalName:
#   - protocol UDP or SCTP: non-HTTP.
#   - TCP with a declared protocol: appProtocol if set (a leading
#     "kubernetes.io/" is dropped, case ignored), else the name prefix before
#     the first "-" when it is one Istio recognises (grpc, grpc-web, http,
#     http2, https, mongo, mysql, redis, tcp, tls, udp). HTTP means one of
#     http, http2, https, grpc, grpc-web, h2c, ws, wss; anything else is
#     non-HTTP.
#   - TCP with no declared protocol: HTTP if the port number is in
#     80, 443, 3000, 8000, 8008, 8080, 8081, 8443, 8888, 9090, 9093, else
#     non-HTTP.
# That is a rule about how the chart labels its ports, not a probe.
#
# Dry-run limit: `kubectl apply --dry-run=server` cannot check a custom
# resource whose CRD is created in the same file. Errors of the form "no
# matches for kind K" are tolerated when K is defined by a CRD in the render
# (the render includes crds/ through --include-crds) and are noted in the
# record. Every other error fails the step.
#
# Usage:
#   hack/compat-exp/qualify.sh CHART_ID OUTDIR
#
# Output: OUTDIR/CHART_ID.qualify.json, one record, see below.
#
# Environment:
#   CHARTS_FILE  chart list to read (observe.sh shape); default the pilot's.
#   MMC_STATE    where world.sh keeps its kubeconfigs (default ./.mmc-state).
#   MMC_SETTLE   seconds to wait after install before looking (default 30).
#   QUAL_DEADLINE helm --timeout for an install (default 15m, the experiment's).
#   QUAL_INSTALLS installs required (default 3).
#
# The world must be W0, made by world.sh with MMC_REGISTRY_MIRROR=1 (the
# experiment mirrors docker.io in every world, W0 included). A world file
# without a registry_mirror line is refused.
#
# Record fields: id, chart, version, qualified, firstFailure (null, or {step:
# template|dry-run|install, install: N or null, reason: template|dry-run|
# pull-failure|capacity|not-ready|timeout|install-error, detail}), steps
# (template, dryRun: ok|failed|skipped), installs ([{n, durationSeconds,
# ready, helmRc}]), durationsSeconds, slowestSeconds (number or null when no
# install ran), bias, notes, environment.
#
# Exit codes: 0 recorded (qualified or not), 2 usage, 3 could not decide (the
# world or its cleanup failed, so the run says nothing about the chart).
set -uo pipefail

id="${1:?usage: qualify.sh CHART_ID OUTDIR}"
out="${2:?output dir}"

here="$(cd "$(dirname "$0")" && pwd)"
state="${MMC_STATE:-$PWD/.mmc-state}"
cluster=mmc-w0
export KUBECONFIG="$state/$cluster.kubeconfig"
ns=pilot
settle="${MMC_SETTLE:-30}"
qdeadline="${QUAL_DEADLINE:-15m}"
need="${QUAL_INSTALLS:-3}"

export HELM_CONFIG_HOME="$state/helm/config" HELM_CACHE_HOME="$state/helm/cache"
export HELM_REPOSITORY_CONFIG="$HELM_CONFIG_HOME/repositories.yaml"
export HELM_REPOSITORY_CACHE="$HELM_CACHE_HOME/repository"

created=
undecided() {
  echo "could not decide: $*" >&2
  if [[ -n "$created" ]]; then
    helm uninstall "$id" -n "$ns" --wait --timeout 3m >/dev/null 2>&1
    kubectl delete namespace "$ns" --wait --timeout=5m >/dev/null 2>&1 ||
      echo "namespace $ns did not delete; remove it before the next run" >&2
  fi
  exit 3
}

[[ -f "$KUBECONFIG" && -f "$state/$cluster.world" ]] || undecided "no world W0 (run world.sh W0)"
contexts="$(kubectl config get-contexts -o name)"
[[ "$contexts" == "kind-$cluster" ]] || undecided "kubeconfig holds '$contexts', expected only kind-$cluster"
grep -q '^registry_mirror=' "$state/$cluster.world" ||
  undecided "the W0 world file has no registry_mirror line; run world.sh with MMC_REGISTRY_MIRROR=1"
command -v yq >/dev/null 2>&1 || undecided "yq is needed to read the rendered manifests"

charts_file="${CHARTS_FILE:-$here/../compat-pilot/charts.json}"
spec="$(jq -ce --arg id "$id" '.[] | select(.id == $id)' "$charts_file")" ||
  { echo "no chart '$id' in $charts_file" >&2; exit 2; }
chart="$(jq -r .chart <<<"$spec")"
version="$(jq -r .version <<<"$spec")"
repo="$(jq -r '.repo // empty' <<<"$spec")"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$out"

# --- the three jq programs shared with observe.sh ----------------------------
# Copied verbatim from hack/compat-pilot/observe.sh; test/qualify_test.sh fails
# if they drift apart.

# shellcheck disable=SC2016 # a jq program, not shell
workloads_filter='[.items[]
  | select(.metadata.labels["mmc-client"] == null)
  | (.metadata.ownerReferences[0] // {}) as $o
  | select($o.kind != "Job")
  | {
      workload: (if $o.kind == "ReplicaSet" then "Deployment/" + ($o.name | sub("-[^-]+$"; ""))
                 elif $o.kind then $o.kind + "/" + $o.name
                 else "Pod/" + .metadata.name end),
      pod: .metadata.name,
      phase: .status.phase,
      ready: ((.status.conditions // []) | any(.type == "Ready" and .status == "True")),
      restarts: ([.status.containerStatuses[]?.restartCount, .status.initContainerStatuses[]?.restartCount] | add // 0),
      reasons: ([.status.containerStatuses[]?, .status.initContainerStatuses[]?
                 | .state.waiting.reason // .state.terminated.reason // .lastState.terminated.reason // empty]
                | unique),
      # Which container, which image, and what it said. A reason without the
      # container it belongs to was not enough to read the first pilot run:
      # "ImagePullBackOff" on a pod that also has an init container and a
      # proxy does not say which image failed.
      containers: [(.status.initContainerStatuses[]? | . + {init: true}), .status.containerStatuses[]?
                   | select(.ready != true or .restartCount > 0)
                   | {name, image, init: (.init // false), restarts: .restartCount,
                      state: (.state | keys[0]),
                      reason: (.state.waiting.reason // .state.terminated.reason // ""),
                      message: ((.state.waiting.message // .state.terminated.message // "") | .[0:300]),
                      lastExit: (.lastState.terminated | if . then {exitCode, reason} else null end)}]
    }]'

# shellcheck disable=SC2016 # a jq program, not shell
pullfailures_filter='[.items[]
  | (.metadata.ownerReferences[0] // {}) as $o
  | (if $o.kind then $o.kind + "/" + $o.name else "Pod/" + .metadata.name end) as $owner
  | ((.status.initContainerStatuses // []) + (.status.containerStatuses // []))[]
  | select(.state.waiting.reason == "ErrImagePull" or .state.waiting.reason == "ImagePullBackOff")
  | {owner: $owner, image}] | unique'

# shellcheck disable=SC2016 # a jq program, not shell
capacity_filter='[.items[]
  | select(.status.phase == "Pending")
  | . as $p
  | ((.status.conditions // [])[] | select(.type == "PodScheduled" and .status == "False" and .reason == "Unschedulable"
       and ((.message // "") | test("Insufficient (cpu|memory)"))))
  | {pod: $p.metadata.name, message: (.message | .[0:300])}]'

# --- bias fields, from the render ----------------------------------------------

# shellcheck disable=SC2016 # a jq program, not shell
bias_filter='
  def objs: .[] | select(type == "object") | if .kind == "List" then (.items[]? | select(type == "object")) else . end;
  def known_prefix: ["grpc","grpc-web","http","http2","https","mongo","mysql","redis","tcp","tls","udp"];
  def http_protos: ["http","http2","https","grpc","grpc-web","h2c","ws","wss"];
  def http_ports: [80,443,3000,8000,8008,8080,8081,8443,8888,9090,9093];
  def declared:
    ((.appProtocol // "") | ascii_downcase | sub("^kubernetes\\.io/"; "")) as $ap
    | if $ap != "" then $ap
      else (((.name // "") | ascii_downcase | split("-")[0]) as $p | if ($p | IN(known_prefix[])) then $p else "" end)
      end;
  def non_http:
    ((.protocol // "TCP") | ascii_upcase) as $proto
    | if $proto != "TCP" then true
      else declared as $d
        | if $d != "" then ($d | IN(http_protos[]) | not)
          else ((.port | tonumber? // 0) | IN(http_ports[]) | not) end
      end;
  [objs] as $all
  | [$all[] | select(.kind == "Service" and (.spec.type // "") != "ExternalName")] as $svcs
  | {
      hasStatefulSet: ($all | any(.kind == "StatefulSet")),
      hasNetworkPolicy: ($all | any(.kind == "NetworkPolicy")),
      hasNonHttpPort: ($svcs | any(.spec.ports[]? | non_http)),
      hasHeadlessService: ($svcs | any(.spec.clusterIP == "None"))
    }'

null_bias='{"hasStatefulSet":null,"hasNetworkPolicy":null,"hasNonHttpPort":null,"hasHeadlessService":null}'

# --- the record ------------------------------------------------------------------

template_status=skipped
dryrun_status=skipped
bias="$null_bias"
fail_step=
fail_install=null
fail_reason=
fail_detail=
: >"$tmp/installs.jsonl"
: >"$tmp/notes.txt"

fail() { # step reason detail [install]
  fail_step="$1"
  fail_reason="$2"
  fail_detail="$(tr '\n' ' ' <<<"$3" | cut -c1-300)"
  fail_install="${4:-null}"
}

write_record() {
  local qualified=false
  [[ -z "$fail_reason" ]] && qualified=true
  jq -n \
    --arg id "$id" --arg chart "$chart" --arg version "$version" \
    --argjson qualified "$qualified" \
    --arg step "$fail_step" --argjson inst "$fail_install" --arg reason "$fail_reason" --arg detail "$fail_detail" \
    --arg tstat "$template_status" --arg dstat "$dryrun_status" \
    --argjson bias "$bias" --rawfile notes "$tmp/notes.txt" \
    --slurpfile installs "$tmp/installs.jsonl" --rawfile worldinfo "$state/$cluster.world" '
    {
      id: $id, chart: $chart, version: $version, qualified: $qualified,
      firstFailure: (if $reason == "" then null
                     else {step: $step, install: $inst, reason: $reason, detail: $detail} end),
      steps: {template: $tstat, dryRun: $dstat},
      installs: $installs,
      durationsSeconds: [$installs[].durationSeconds],
      slowestSeconds: ([$installs[].durationSeconds] | max),
      bias: $bias,
      notes: ($notes | split("\n") | map(select(length > 0))),
      environment: ($worldinfo | split("\n") | map(select(length > 0) | split("=") | {(.[0]): (.[1:] | join("="))}) | add)
    }' >"$out/$id.qualify.json" || undecided "could not write the record"
  if [[ -z "$fail_reason" ]]; then
    echo "$id: qualified, installs $(jq -c .durationsSeconds "$out/$id.qualify.json") s"
  else
    echo "$id: not qualified, $fail_step: $fail_reason"
  fi
}

# --- 1. template -------------------------------------------------------------------

targs=(template "$id" "$chart" --version "$version" --namespace "$ns" --include-crds)
[[ -n "$repo" ]] && targs+=(--repo "$repo")
if ! helm "${targs[@]}" >"$tmp/rendered.yaml" 2>"$tmp/template.err"; then
  template_status=failed
  fail template template "$(tail -3 "$tmp/template.err")"
  write_record
  exit 0
fi
template_status=ok

yq -o=json -I=0 '.' "$tmp/rendered.yaml" 2>"$tmp/yq.err" | jq -s "$bias_filter" >"$tmp/bias.json" 2>>"$tmp/yq.err" ||
  { echo "could not read the rendered manifests: $(tail -2 "$tmp/yq.err")" >&2; exit 3; }
bias="$(jq -c . "$tmp/bias.json")"

# --- 2. server-side dry-run ----------------------------------------------------------

kubectl create namespace "$ns" >/dev/null || undecided "namespace $ns exists (previous run not cleaned)"
created=1
kubectl apply --dry-run=server -n "$ns" -f "$tmp/rendered.yaml" >"$tmp/dry.out" 2>"$tmp/dry.err"
dry_rc=$?
if [[ $dry_rc -ne 0 ]]; then
  yq -o=json -I=0 '.' "$tmp/rendered.yaml" | jq -rs '[.[] | select(type == "object")
    | select(.kind == "CustomResourceDefinition") | .spec.names.kind] | unique[]' >"$tmp/crd-kinds.txt" 2>/dev/null
  : >"$tmp/dry-remaining.txt"
  tolerated=0
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    if [[ "$line" == "ensure CRDs are installed first" ]]; then
      continue
    fi
    kind="$(sed -n 's/.*no matches for kind "\([^"]*\)" in version.*/\1/p' <<<"$line")"
    if [[ -n "$kind" ]] && grep -qxF "$kind" "$tmp/crd-kinds.txt"; then
      tolerated=$((tolerated + 1))
      echo "dry-run: kind $kind has its CRD in the render; not checked server-side" >>"$tmp/notes.txt"
      continue
    fi
    echo "$line" >>"$tmp/dry-remaining.txt"
  done <"$tmp/dry.err"
  if [[ -s "$tmp/dry-remaining.txt" || $tolerated -eq 0 ]]; then
    dryrun_status=failed
    detail="$(head -3 "$tmp/dry-remaining.txt")"
    [[ -n "$detail" ]] || detail="$(tail -3 "$tmp/dry.out") $(tail -3 "$tmp/dry.err")"
    kubectl delete namespace "$ns" --wait --timeout=5m >/dev/null || undecided "namespace $ns did not delete"
    created=
    fail dry-run dry-run "$detail"
    write_record
    exit 0
  fi
fi
dryrun_status=ok
kubectl delete namespace "$ns" --wait --timeout=5m >/dev/null || undecided "namespace $ns did not delete"
created=

# --- 3. installs ---------------------------------------------------------------------

iargs=(install "$id" "$chart" --namespace "$ns" --version "$version" --wait --wait-for-jobs --timeout "$qdeadline")
[[ -n "$repo" ]] && iargs+=(--repo "$repo")

n=1
while [[ $n -le $need ]]; do
  kubectl create namespace "$ns" >/dev/null || undecided "namespace $ns exists (previous run not cleaned)"
  created=1
  start="$(date +%s)"
  helm "${iargs[@]}" >"$tmp/helm.log" 2>&1
  helm_rc=$?
  duration=$(($(date +%s) - start))
  sleep "$settle"

  kubectl -n "$ns" get pods -o json >"$tmp/pods.json" || undecided "could not list pods"
  jq "$workloads_filter" "$tmp/pods.json" >"$tmp/workloads.json" || undecided "could not read pods"
  jq "$pullfailures_filter" "$tmp/pods.json" >"$tmp/pullfailures.json" || undecided "could not read pods"
  jq "$capacity_filter" "$tmp/pods.json" >"$tmp/capacity.json" || undecided "could not read pods"

  # Same rule as observe.sh's workload:...:ready observation.
  unready="$(jq -r '[group_by(.workload)[] | select(all(.[]; .ready or .phase == "Succeeded") | not) | .[0].workload] | join(",")' "$tmp/workloads.json")"
  ready=false
  reason=
  detail=
  if [[ "$(jq length "$tmp/pullfailures.json")" -gt 0 ]]; then
    reason=pull-failure
    detail="$(jq -r 'map(.owner + " " + .image) | join("; ")' "$tmp/pullfailures.json")"
  elif [[ "$(jq length "$tmp/capacity.json")" -gt 0 ]]; then
    reason=capacity
    detail="$(jq -r 'map(.pod + ": " + .message) | join("; ")' "$tmp/capacity.json")"
  elif [[ $helm_rc -ne 0 ]]; then
    detail="$(tail -3 "$tmp/helm.log")"
    if grep -Eqi 'timed out waiting|context deadline exceeded|timeout' "$tmp/helm.log"; then reason=timeout; else reason=install-error; fi
  elif [[ -n "$unready" ]]; then
    reason=not-ready
    detail="workloads not ready: $unready"
  else
    ready=true
  fi
  jq -nc --argjson n "$n" --argjson d "$duration" --argjson r "$ready" --argjson rc "$helm_rc" \
    '{n: $n, durationSeconds: $d, ready: $r, helmRc: $rc}' >>"$tmp/installs.jsonl"

  helm uninstall "$id" -n "$ns" --wait --timeout 3m >/dev/null 2>&1 || true
  kubectl delete namespace "$ns" --wait --timeout=5m >/dev/null || undecided "namespace $ns did not delete"
  created=

  if [[ -n "$reason" ]]; then
    fail install "$reason" "$detail" "$n"
    break
  fi
  n=$((n + 1))
done

write_record
exit 0
