#!/usr/bin/env bash
#
# Install one chart, unchanged, in one world, record what it does, and remove
# it again (ADR 0001, section 2). One call is one run; a finding needs the same
# difference in 3 of 3 runs, so the workflow calls this three times.
#
# What is recorded does not depend on knowing how the chart should behave.
# Every observation is derived from what the chart rendered: its workloads,
# its Jobs, its tests, its Service ports. The verdict comes later, from
# comparing a mesh world with W0 (difftable), so this script never decides
# whether a value is good, only what it was.
#
# Usage:
#   hack/compat-pilot/observe.sh WORLD CHART_ID RUN OUTDIR
#
# Exit codes: 0 recorded (whatever the chart did), 2 usage, 3 could not
# decide (the world or its cleanup failed, so the run says nothing about the
# chart).
set -uo pipefail

world="${1:?usage: observe.sh WORLD CHART_ID RUN OUTDIR}"
id="${2:?chart id}"
run="${3:?run number}"
out="${4:?output dir}"

here="$(cd "$(dirname "$0")" && pwd)"
state="${MMC_STATE:-$PWD/.mmc-state}"
cluster="mmc-$(tr '[:upper:]' '[:lower:]' <<<"$world")"
export KUBECONFIG="$state/$cluster.kubeconfig"
ns=pilot
settle="${MMC_SETTLE:-30}"

# Helm gets its own config and cache as well: a repository list left on the
# operator's machine changes how `--repo` resolves, and has been seen to make
# it fail outright on a stale cache entry for an unrelated repository.
export HELM_CONFIG_HOME="$state/helm/config" HELM_CACHE_HOME="$state/helm/cache"
export HELM_REPOSITORY_CONFIG="$HELM_CONFIG_HOME/repositories.yaml"
export HELM_REPOSITORY_CACHE="$HELM_CACHE_HOME/repository"

# Once this run has created the namespace, giving up must not leave it
# behind: every later run on the world would fail on "namespace exists".
created=
undecided() {
  echo "could not decide: $*" >&2
  if [[ -n "$created" ]]; then
    helm uninstall "$release" -n "$ns" --wait --timeout 3m >/dev/null 2>&1
    kubectl delete namespace "$ns" --wait --timeout=5m >/dev/null 2>&1 ||
      echo "namespace $ns did not delete; remove it before the next run" >&2
  fi
  exit 3
}

# The only cluster this script may touch is the one world.sh made. Checking
# the file exists is not enough: a kubeconfig can be edited, so the context
# inside it has to be the one kind wrote.
[[ -f "$KUBECONFIG" && -f "$state/$cluster.world" ]] || undecided "no world $world (run world.sh $world)"
contexts="$(kubectl config get-contexts -o name)"
[[ "$contexts" == "kind-$cluster" ]] || undecided "kubeconfig holds '$contexts', expected only kind-$cluster"

spec="$(jq -ce --arg id "$id" '.[] | select(.id == $id)' "$here/charts.json")" ||
  { echo "no chart '$id' in charts.json" >&2; exit 2; }
chart="$(jq -r .chart <<<"$spec")"
version="$(jq -r .version <<<"$spec")"
repo="$(jq -r '.repo // empty' <<<"$spec")"
deadline="$(jq -r '.deadline // "8m"' <<<"$spec")"
fixof="$(jq -r '.fixOf // empty' <<<"$spec")"
# A candidate fix installs under the release name of the chart it fixes, so
# every resource, and so every observation key, is named the same and the two
# can be compared key by key.
release="${fixof:-$id}"
# A read loop rather than mapfile, which the bash 3.2 macOS ships lacks.
values=()
while IFS= read -r v; do values+=("$v"); done < <(jq -r '.values[]?' <<<"$spec")

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$out"

# --- namespace and client ---------------------------------------------------

kubectl create namespace "$ns" >/dev/null || undecided "namespace $ns exists (previous run not cleaned)"
created=1
case "$world" in
  W1 | W2) kubectl label namespace "$ns" istio-injection=enabled >/dev/null ;;
  W3 | W4) kubectl label namespace "$ns" istio.io/dataplane-mode=ambient >/dev/null ;;
esac
# W4 is W3 with a waypoint for the namespace (see world.sh).
if [[ "$world" == W4 ]]; then
  istioctl waypoint apply -n "$ns" --enroll-namespace --wait >/dev/null || undecided "waypoint not ready"
fi

# The client sits in the same namespace, so it is in the same mesh as the
# chart: in W1/W2 it gets a sidecar, in W3 its traffic goes through ztunnel.
# Its own proxy must be up before it makes a request, or the first probes
# measure the client's startup rather than the chart.
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

# --- install ----------------------------------------------------------------

args=(install "$release" "$chart" --namespace "$ns" --version "$version"
  --wait --wait-for-jobs --timeout "$deadline")
[[ -n "$repo" ]] && args+=(--repo "$repo")
start="$(date +%s)"
helm "${args[@]}" ${values[@]+"${values[@]}"} >"$tmp/helm.log" 2>&1
helm_rc=$?
duration=$(($(date +%s) - start))

# A chart that installed can still fall over a few seconds later (a liveness
# probe that the proxy breaks, a restart once the sidecar is torn down), so
# give it a moment before looking.
sleep "$settle"

# --- workloads and jobs -----------------------------------------------------

# Pods are grouped by the workload that owns them, because pod names carry
# random suffixes and would never match across runs. A Deployment's pods are
# owned by a ReplicaSet whose name ends in a template hash; that hash is
# dropped. Job pods are left to the Job's own condition: a Job that retried
# and then succeeded is a success, but its failed pod would say otherwise.
kubectl -n "$ns" get pods -o json >"$tmp/pods.json" || undecided "could not list pods"
jq '[.items[]
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
    }]' "$tmp/pods.json" >"$tmp/workloads.json" || undecided "could not read pods"

# Every pod, Job pods included, whose image could not be pulled. Such a run
# measured the registry, and difftable leaves it out; Job pods have to count
# here because they are not in the workloads above.
jq '[.items[]
  | (.metadata.ownerReferences[0] // {}) as $o
  | (if $o.kind then $o.kind + "/" + $o.name else "Pod/" + .metadata.name end) as $owner
  | ((.status.initContainerStatuses // []) + (.status.containerStatuses // []))[]
  | select(.state.waiting.reason == "ErrImagePull" or .state.waiting.reason == "ImagePullBackOff")
  | {owner: $owner, image}] | unique' \
  "$tmp/pods.json" >"$tmp/pullfailures.json" || undecided "could not read pods"

# Warning events are evidence, not observations: they explain a difference
# after the fact, and their wording changes between versions.
kubectl -n "$ns" get events --field-selector type=Warning -o json | jq '[.items[]
  | {object: (.involvedObject.kind + "/" + .involvedObject.name), reason, message: (.message | .[0:300]), count}]
  | unique_by(.object, .reason) | .[0:40]' >"$tmp/events.json" || echo '[]' >"$tmp/events.json"

kubectl -n "$ns" get jobs -o json | jq '[.items[] | {
    job: .metadata.name,
    complete: ((.status.conditions // []) | any(.type == "Complete" and .status == "True")),
    failed: ((.status.conditions // []) | any(.type == "Failed" and .status == "True")),
    active: (.status.active // 0)
  }]' >"$tmp/jobs.json" || undecided "could not list jobs"

# --- helm test --------------------------------------------------------------

test_result=none
: >"$tmp/test.log"
# The hook key is often quoted ("helm.sh/hook": test) and can hold a list.
if [[ $helm_rc -eq 0 ]] && helm get hooks "$release" -n "$ns" 2>/dev/null |
  grep -Eq 'helm\.sh/hook"?:[[:space:]]*"?([a-z-]+,)*test(-success)?([,"[:space:]]|$)'; then
  if helm test "$release" -n "$ns" --timeout 5m >"$tmp/test.log" 2>&1; then
    test_result=pass
  else
    test_result=fail
    # Not `helm test --logs`: it fails the whole test when a passing pod has
    # already been deleted by its hook-succeeded policy, which turned every
    # world into "fail" in run 5. A failed test pod stays, so read those.
    kubectl -n "$ns" get pods -o json | jq -r '.items[]
      | select((.metadata.annotations["helm.sh/hook"] // "") | test("(^|,)test(-success)?(,|$)"))
      | select(.status.phase != "Succeeded") | .metadata.name' |
      while read -r pod; do
        echo "--- $pod"
        kubectl -n "$ns" logs "$pod" --all-containers --tail=20 2>&1
      done >>"$tmp/test.log"
  fi
fi

# --- service ports ----------------------------------------------------------

# Two probes per TCP port, both from inside the mesh:
#
#   passive  connect, send nothing, and count what the server says first.
#            Server-first protocols (MySQL, NATS, SMTP) greet; under a
#            sidecar that is sniffing the port for a protocol, the greeting
#            never arrives because the proxy is waiting for the client.
#   http     a plain GET. Only meaningful where W0 answered with an HTTP
#            status; difftable ignores it elsewhere, because sending HTTP to
#            a non-HTTP port under a sniffing sidecar produces a 502 that
#            says something about the probe, not the chart.
#
# Neither probe can tell "the port works" from "the port does what W0 did",
# and they do not try: W0 is the oracle.
probe() {
  kubectl -n "$ns" exec mmc-client -c client -- timeout 15 sh -c "$1" 2>/dev/null
}
kubectl -n "$ns" get svc -o json | jq -r '.items[]
  | select(.spec.type != "ExternalName")
  | .metadata.name as $svc
  | (.spec.clusterIP == "None") as $headless
  | .spec.ports[]?
  | select((.protocol // "TCP") == "TCP")
  | [$svc, (.port | tostring), (.name // ""), (.appProtocol // ""), ($headless | tostring)]
  | join("|")' >"$tmp/ports.txt"

: >"$tmp/ports.jsonl"
# "|" rather than a tab: read collapses consecutive tabs, so an unnamed port
# would shift every field after it by one.
while IFS='|' read -r svc port pname approto headless; do
  bytes="$(probe "sleep 4 | nc -w 3 $svc.$ns $port | head -c 64 | wc -c" | tr -d ' ')"
  code="$(probe "wget -S -O /dev/null -T 5 http://$svc.$ns:$port/ 2>&1 | sed -n 's|^ *HTTP/[0-9.]* \([0-9][0-9]*\).*|\1|p' | tail -1")"
  jq -nc --arg svc "$svc" --arg port "$port" --arg name "$pname" --arg ap "$approto" \
    --arg headless "$headless" --arg bytes "${bytes:-0}" --arg code "$code" \
    '{svc: $svc, port: ($port | tonumber), name: $name, appProtocol: $ap,
      headless: ($headless == "true"), passive: (if ($bytes | tonumber) > 0 then "bytes" else "silent" end),
      http: (if $code == "" then "none" else $code end)}' >>"$tmp/ports.jsonl"
done <"$tmp/ports.txt"
jq -s . "$tmp/ports.jsonl" >"$tmp/ports.json"

# --- istioctl analyze -------------------------------------------------------

# Evidence, never the verdict: analyze runs on the live namespace so a later
# reader can see whether Istio's own linter would have warned.
echo '[]' >"$tmp/analyze.json"
if [[ "$world" != W0 ]]; then
  istioctl analyze -n "$ns" -o json 2>/dev/null |
    jq '[.[]? | {code: .code, level: .level, origin: .origin, message: .message}]' >"$tmp/analyze.json" ||
    echo '[]' >"$tmp/analyze.json"
fi

# --- record -----------------------------------------------------------------

# obs is the flat map difftable compares. Keys name a thing the chart
# rendered; values are what was seen. A key that exists only in some worlds
# (a hook Job deleted on success in W0, still hanging in W3) compares as
# "absent" on the side that lacks it.
jq -n \
  --arg world "$world" --arg chart "$id" --argjson run "$run" \
  --arg version "$version" --rawfile worldinfo "$state/$cluster.world" \
  --argjson helm_rc "$helm_rc" --argjson duration "$duration" \
  --rawfile helm_log "$tmp/helm.log" --arg test "$test_result" --rawfile test_log "$tmp/test.log" \
  --slurpfile workloads "$tmp/workloads.json" --slurpfile jobs "$tmp/jobs.json" \
  --slurpfile ports "$tmp/ports.json" --slurpfile analyze "$tmp/analyze.json" \
  --slurpfile events "$tmp/events.json" --slurpfile pulls "$tmp/pullfailures.json" \
  --arg fixof "$fixof" '
  ($workloads[0] | group_by(.workload)) as $groups
  | {
    world: $world, chart: $chart, chartVersion: $version, run: $run, fixOf: $fixof,
    environment: ($worldinfo | split("\n") | map(select(length > 0) | split("=") | {(.[0]): (.[1:] | join("="))}) | add),
    helm: {rc: $helm_rc, durationSeconds: $duration, logTail: ($helm_log | split("\n") | .[-15:] | join("\n")),
           testLogTail: ($test_log | split("\n") | .[-40:] | join("\n"))},
    workloads: $workloads[0], jobs: $jobs[0], ports: $ports[0], analyze: $analyze[0], events: $events[0],
    pullFailures: $pulls[0],
    obs: (
      {"helm:install": (if $helm_rc == 0 then "ok" else "failed" end), "helm:test": $test}
      + ([$groups[] | {("workload:" + .[0].workload + ":ready"):
            (if all(.[]; .ready or .phase == "Succeeded") then "true" else "false" end)}] | add // {})
      + ([$groups[] | {("workload:" + .[0].workload + ":restarts"):
            (if ([.[].restarts] | add) == 0 then "0" else ">0" end)}] | add // {})
      + ([$jobs[0][] | {("job:" + .job + ":complete"): (.complete | tostring)}] | add // {})
      + ([$ports[0][] | {("port:" + .svc + ":" + (.port | tostring) + ":passive"): .passive,
                         ("port:" + .svc + ":" + (.port | tostring) + ":http"): .http}] | add // {})
    )
  }' >"$out/$id.$world.run$run.json" || undecided "could not write the record"

echo "$id $world run$run: helm rc=$helm_rc in ${duration}s, $(jq '.obs | length' "$out/$id.$world.run$run.json") observations"

# --- clean up ---------------------------------------------------------------

# The next run must start from nothing, so a namespace that will not go away
# makes this run's successors meaningless. Say so instead of carrying on.
helm uninstall "$release" -n "$ns" --wait --timeout 3m >/dev/null 2>&1 || true
kubectl delete namespace "$ns" --wait --timeout=5m >/dev/null || undecided "namespace $ns did not delete"
exit 0
