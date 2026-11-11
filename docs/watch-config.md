# watch.yaml reference

Every field `meshmedic watch`, `check` and `calibrate` read from the config
file (`--config`, default `watch.yaml`). The file is decoded strictly: a key
that is not on this page fails the load, so a typo such as `statefile` is an
error and not a silently disabled feature.

A test (`TestWatchConfigPageListsEveryField` in `pkg/detect`) walks the config
structs and fails when a field is missing from this page.

## Top level

| field | type | required | default | what it does |
| --- | --- | --- | --- | --- |
| `prometheus` | string | yes | | Base URL of the Prometheus that every signal is queried against. |
| `prometheusAuth` | object | no | none | Credentials and CA for a Prometheus behind authentication; see [below](#prometheusauth). |
| `interval` | duration | no | `30s` | How often every target is evaluated. A Go duration such as `30s` or `2m`; anything else fails the load. |
| `targets` | list | yes | | What to watch; at least one. See [below](#targets). |
| `gitops` | object | no | none | Where remediation pull requests go. Without it `watch` only prints incident reports. See [below](#gitops). |
| `stateFile` | path | no | none | Where open incidents are kept between ticks. Without it a restart re-opens every incident that is still breaching, which with `gitops` means a second pull request. |
| `coverageProbe` | PromQL template | no | `count(istio_requests_total{destination_service_namespace="{{.namespace}}"})` | The control query that proves a target is visible at all, so silence is not read as health. A target can override it. |
| `baselineState` | path | no | none | Where the learned per-signal baseline is kept. Without it entries with a `baselineMultiplier` use their static threshold only. |
| `anomalyWatch` | list | no | none | Generic signals baselined per target for the unmatched-incident recorder; see [below](#anomalywatch). |
| `unmatchedLog` | path | no | none | The JSON-lines file the recorder appends a fingerprint to when an `anomalyWatch` signal deviates while no catalog scenario is active. |

The recorder runs only when `baselineState`, `unmatchedLog` and at least one
`anomalyWatch` signal are all set. With `unmatchedLog` and no `baselineState`,
`watch` logs that the recorder is skipped and carries on.

## targets

Each target is one set of template parameters, evaluated against the catalog.

| field | type | required | default | what it does |
| --- | --- | --- | --- | --- |
| `params` | map of string to string | yes | | The template parameters the catalog's signals and patches use, such as `service`, `namespace`, `workload`, `subset` and `stable_subset`. A target with no params fails the load. |
| `scenarios` | list of ids | no | the whole catalog | The catalog entries to evaluate for this target. An id that is not in the catalog fails startup. |
| `coverageProbe` | PromQL template | no | the top-level `coverageProbe` | Overrides the control query for this target, for when a namespace is not the right unit of visibility. |

## gitops

| field | type | required | default | what it does |
| --- | --- | --- | --- | --- |
| `repo` | string | yes | | The configuration repository, as `owner/repo`. Anything without a `/` fails the load. |
| `base` | string | no | the repository's default branch | The branch pull requests are opened against. |
| `path` | path template | no | `meshmedic/{{.namespace}}/{{.scenario}}.yaml` | Where the patch file goes in the repository. It can use the target's params and `scenario`, the entry's id. |

The token is read from `MESHMEDIC_GITHUB_TOKEN`, then `GITHUB_TOKEN`, never
from the file. With `gitops` set and neither variable set, `watch` exits.

## prometheusAuth

The section names environment variables, never the credentials themselves, so
the file can be committed. An empty section is an error.

| field | type | required | default | what it does |
| --- | --- | --- | --- | --- |
| `bearerTokenEnv` | variable name | no | none | The environment variable that holds a bearer token. An alternative to basic auth, not an addition. |
| `usernameEnv` | variable name | with `passwordEnv` | none | The environment variable that holds the basic auth user. |
| `passwordEnv` | variable name | with `usernameEnv` | none | The environment variable that holds the basic auth password. |
| `caFile` | path | no | none | A PEM file of extra CA certificates to trust, in addition to the system roots. |

A named variable that is unset or empty stops the run before any request is
sent.

## anomalyWatch

| field | type | required | default | what it does |
| --- | --- | --- | --- | --- |
| `name` | string | yes | | The name the signal has in the baseline and in the fingerprint. |
| `promql` | PromQL template | yes | | The query to baseline. It can use the target's params. |

## What the flag form cannot set

`meshmedic watch --prometheus URL --target key=value,...` builds a config
without a file. It sets `prometheus`, one `targets[].params` per `--target`,
`interval` (`--interval`, `watch` only), and from `prometheusAuth` only
`bearerTokenEnv` (`--prometheus-token-env`) and `caFile`
(`--prometheus-ca-file`).

Everything else needs a file: `gitops`, `stateFile`, `coverageProbe`,
`baselineState`, `anomalyWatch`, `unmatchedLog`, a target's `scenarios` and
`coverageProbe`, and basic auth (`usernameEnv`, `passwordEnv`). For `gitops`
that is on purpose: write access to a repository should not be reachable from
a flag pasted out of a README.

## A file with everything set

```yaml
prometheus: https://prometheus.example.com
prometheusAuth:
  bearerTokenEnv: MESHMEDIC_PROMETHEUS_TOKEN
  caFile: /etc/meshmedic/prometheus-ca.pem
interval: 30s
stateFile: /var/lib/meshmedic/state.json
coverageProbe: 'count(istio_requests_total{destination_service_namespace="{{.namespace}}"})'
baselineState: /var/lib/meshmedic/baseline.json
unmatchedLog: /var/lib/meshmedic/unmatched.jsonl
anomalyWatch:
  - name: payments-service-p99-latency
    promql: >
      histogram_quantile(0.99, sum by (le) (rate(istio_request_duration_milliseconds_bucket{destination_service_name="{{.service}}", destination_service_namespace="{{.namespace}}"}[2m])))
gitops:
  repo: you/your-config-repo
  base: main
  path: istio/{{.namespace}}/{{.scenario}}.yaml
targets:
  - params:
      service: payments
      namespace: demo
      workload: payments-v2
      subset: v2
      stable_subset: v1
    scenarios: [canary-latency-rollback]
    coverageProbe: 'count(istio_requests_total{destination_service_name="{{.service}}"})'
```
