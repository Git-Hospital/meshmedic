# Observation record reference

What `hack/compat-pilot/observe.sh` writes, one file per run:
`OUTDIR/<chart id>.<world>.run<N>.json`. Fields marked (new) were added for the
compat experiment (docs/compat-experiment.md, section 5); everything else is
the pilot's and unchanged. New fields and keys are additions only: with the
pilot's `charts.json` the pilot's fields and keys are identical to
`origin/main` (checked by the last test in `test/observe_test.sh`).

## Invocation

    [CHARTS_FILE=path] hack/compat-pilot/observe.sh WORLD CHART_ID RUN OUTDIR

Exit 0 recorded, 2 usage or refused input (unknown id, patch path outside
`hack/compat-exp/patches/`, patch over 40 changed lines), 3 could not decide
(world or cleanup failed, patch does not apply or fails `helm lint`, the API
server could not be contacted).

| env | default | meaning |
|---|---|---|
| `CHARTS_FILE` (new) | `hack/compat-pilot/charts.json` | chart list to read |
| `MMC_STATE` | `$PWD/.mmc-state` | where world.sh keeps kubeconfigs |
| `MMC_SETTLE` | 30 | seconds between install and the first look |
| `MMC_LATE` (new) | 120 | seconds after install of the late snapshot (E2) |

A chart entry: `id`, `chart`, `version`, optional `repo`, `values` (helm args),
`deadline`, `fixOf`, and (new) `patch`: a path under `hack/compat-exp/patches/`
to a unified diff of at most 40 changed lines (`+` and `-` lines inside hunks).
The patch is applied with `patch -p1 -F0` to `helm pull --untar` of the pinned
version, then `helm lint` runs, then the chart installs from that directory
(no `--version`, no `--repo`). `values` and `patch` may be combined.

## Top-level fields

| field | type | content |
|---|---|---|
| `world`, `chart`, `chartVersion`, `run` | string, string, string, number | the run's identity; `chart` is the entry id |
| `fixOf` | string | id of the chart this entry fixes, else "" (the release is named `fixOf` when set) |
| `environment` | object | the world file: Istio and Kubernetes versions etc. |
| `helm` | object | `rc`, `durationSeconds`, `logTail`, `testLogTail` |
| `workloads` | array | one entry per non-Job pod, not the client: `workload`, `pod`, `phase`, `ready`, `restarts`, `reasons`, `containers` (unready or restarted ones) |
| `jobs` | array | `job`, `complete`, `failed`, `active` |
| `ports` | array | `svc`, `port`, `name`, `appProtocol`, `headless`, `passive` (bytes or silent), `http` (status or none) |
| `analyze` | array | `istioctl analyze` messages (mesh worlds only), evidence |
| `events` | array | Warning events, evidence |
| `pullFailures` | array | `{owner, image}` for every pod, Job pods included, with ErrImagePull or ImagePullBackOff. difftable leaves such runs out |
| `capacityPending` (new) | array | `{pod, message}` for every pod that is Pending with PodScheduled False, reason Unschedulable and a message naming "Insufficient cpu" or "Insufficient memory". Same level and type as `pullFailures`; empty means false. Taken from the first snapshot |
| `patch` (new) | object or null | null unless the entry has a patch: `path`, `sha256` of the file, `lines` (wc -l of the file), `changedLines` |
| `podMesh` (new) | array | one entry per pod including the client (`client: true`), see below |
| `lateWorkloads` (new) | array | the `workloads` program run again at install + `MMC_LATE` (E2) |
| `apiserver` (new) | object | `services`: `{service, port, reachable}`; `apiServices`: `{name, available}` (E1) |
| `obs` | object | flat map of string to string, see below. This is what difftable compares |

`podMesh` entry: `pod`, `client` (bool), `owner` (`Kind/name`), `meshMetadata`
(list of `{kind: "label"|"annotation", key, value}`, raw, for every key matching
`sidecar.istio.io/*`, `ambient.istio.io/*` or exactly `istio.io/dataplane-mode`),
`istioProxy` (`container`, `initContainer`, `native`: a container or init
container named `istio-proxy`; native is an init container with
restartPolicy Always), `ambientRedirection` (value of the
`ambient.istio.io/redirection` annotation, "" when not reported).

## Observation keys (`obs`)

The part before the first colon is the observation class. Values are strings.

| key | class | value |
|---|---|---|
| `helm:install` | helm | ok, failed |
| `helm:test` | helm | none, pass, fail |
| `workload:<Kind>/<name>:ready` | workload | true, false (Job pods are not workloads) |
| `workload:<Kind>/<name>:restarts` | workload | 0, >0 |
| `job:<name>:complete` | job | true, false |
| `port:<svc>:<port>:passive` | port | bytes, silent |
| `port:<svc>:<port>:http` | port | status code or none |
| `apiserver:svc:<svc>:<port>:reachable` (new, E1) | apiserver | true, false |
| `apiserver:apiservice:<name>:available` (new, E1) | apiserver | true, false, unknown |
| `late:workload:<Kind>/<name>:ready` (new, E2) | late | as `workload:...:ready`, at the second snapshot |
| `late:workload:<Kind>/<name>:restarts` (new, E2) | late | as `workload:...:restarts`, at the second snapshot |

### E1 details

Targets are the Services in the release namespace (`pilot`) named by any
ValidatingWebhookConfiguration (`webhooks[].clientConfig.service`),
MutatingWebhookConfiguration or APIService (`spec.service`); port defaults to
443. There is no release label that every chart sets on these cluster-scoped
objects, so "of the release" is "points to a Service in the release namespace".
Each distinct Service port gets one `kubectl get --raw
/api/v1/namespaces/pilot/services/https:<svc>:<port>/proxy/`.
Reachable means the API server got any HTTP response from the backend,
whatever the status. Not reachable means the API server's own failure: "error
trying to reach service", "no endpoints available for service", a missing
Service, or its own timeout. A release with no such object has no `apiserver:`
key and an empty `apiserver` field. All service ports are probed over https;
the proxy does not verify the backend certificate.

### E2 details

`late:` keys exist for every workload present at the second snapshot, taken at
install time + `MMC_LATE` seconds or after the probes if they end later. The
first snapshot and its `workload:` keys are never overwritten.

## Tests

    hack/compat-exp/test/observe_test.sh

Plain bash with stub `helm`, `kubectl` and `istioctl`; needs `jq`, `patch`,
`git` and a shasum. No cluster, no sleeping (except one 2 s wait test). The
last test compares against the script at `COMPAT_BASE` (default `origin/main`);
once the instrument is merged there, set `COMPAT_BASE` to the commit before
these changes (`26edb29`). The Makefile has no script test target, so it is not
wired into `make`.
