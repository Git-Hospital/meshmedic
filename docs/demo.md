# See it without a mesh: `meshmedic demo`

`meshmedic watch` needs a Prometheus that scrapes Istio metrics, and that needs
a mesh. `meshmedic demo` needs neither. It runs the real detector against a
small replayed Prometheus built into the binary, and prints what `watch` would
print: the cycle states, the incident dossier with its patch and rollback note,
and the resolution report when the incident clears.

```console
$ meshmedic demo
```

It takes about a second, opens no connection beyond a loopback socket in its own
process, reads no token, opens no pull request and writes no file unless you
pass `--out DIR`.

## What is real and what is replayed

Real, the same code `watch` runs:

- the catalog entries, loaded from the binary with the lock check (an entry
  whose hash does not match `catalog.lock` shows up as `UNLOCKED` and does not run);
- `pkg/detect`: the coverage probe, the signal query, the hold duration, the
  four cycle states (`firing`, `clear`, `blind`, `unlocked`), the evidence
  queries and the firing-to-clear resolution;
- `pkg/prom`, the client, talking HTTP to the replayed server;
- `pkg/remediate` for the patch and `pkg/report` for the dossier and the
  resolution report.

Replayed, and nothing else:

- **The Prometheus.** A fixture in `pkg/demo/fixtures/` says what Prometheus
  answers on each cycle. A loopback HTTP server inside the process serves it.
  The server recognises a query by rendering the catalog's own query templates
  with the fixture's target, so editing a catalog query changes the query the
  detector sends and the demo follows. A query the fixture does not explain is
  answered with a 400 and the run fails; the replay never guesses.
- **The configuration reads.** `watch` reads Deployments through `kubectl`.
  The demo answers the same reads from the fixture. Field extraction and
  redaction are the real ones.
- **The clock.** Each cycle runs at a fixed time from the fixture, so the output
  is byte-identical on every run.

### The numbers are constructed, not recorded

The proof reports in `demo/proof-reports/` keep the incident report each run
produced. They do not keep the raw Prometheus responses, so there is no
recorded query result to replay. Each fixture is therefore built from the
values a proof report does record, and every screen says so and lists, per
scenario, what was taken from the report and what was made up:

| scenario | built to match | taken from the report | made up |
| --- | --- | --- | --- |
| `canary-latency-rollback` | `demo/proof-reports/canary-latency-rollback.md` | signal 2485 at firing, breach start 12:48:49Z, both evidence values, the canary Deployment env and resources | response bodies, coverage count, the later cycles, the recovery value, the 45s cycle spacing |
| `error-surge-outlier-ejection` | `demo/proof-reports/error-surge-outlier-ejection.md` | signal 0.1675 at firing, breach start 14:33:51Z, the four evidence rows, the Deployment env | response bodies, coverage count, the later cycles, the recovery value, the 60s cycle spacing |
| `blind-no-telemetry` | README design rule 1 | nothing | everything; it mirrors the unobservable control target of `demo/calibration/live-run.txt` |

The recovery time in the first two is constructed too: the proof reports record
how long a run took to resolve after its reset, not a breach-to-clear interval
that matches this cycle spacing.

## The scenarios

```console
This is a REPLAY of data held inside this binary. Nothing is being watched and nothing is changed.

* canary-latency-rollback          A canary gets slow: incident fires, then clears
  blind-no-telemetry               Prometheus has no mesh telemetry: blind, never clear
  error-surge-outlier-ejection     One bad workload serves 5xx: incident fires, then clears

Run one with: meshmedic demo --scenario NAME   (* is the default)

Next, on a real setup (a Prometheus that scrapes Istio metrics):
  meshmedic check --prometheus http://localhost:9090 --target service=payments,namespace=demo   # exits 1 if it cannot see the target
  meshmedic watch --prometheus http://localhost:9090 --target service=payments,namespace=demo
```

- `canary-latency-rollback` (default): cycles 1 and 2 are in breach but inside
  the 90s hold, so nothing is reported. Cycle 3 meets the hold and the full
  dossier prints. Cycle 4 is still in breach and reports nothing new. Cycle 5
  is back under the threshold and the resolution report prints.
- `error-surge-outlier-ejection`: the same story with a 120s hold, a
  DestinationRule patch and per-workload evidence that names `payments-v2`.
- `blind-no-telemetry`: Prometheus is up but holds no mesh series. The first
  cycle is `blind`, not `clear`, and the screen notes that `meshmedic check`
  would exit 1. The second cycle, with telemetry back and a healthy value, is a
  real `clear`. The difference between those two cycles is the point.

## Output

Default scenario, as printed by the built binary:

```console
$ meshmedic demo
meshmedic demo: canary-latency-rollback
This is a REPLAY of data held inside this binary. Nothing is being watched and nothing is changed.
The real detector ran 5 cycles against a Prometheus served from this process on loopback.

cycle 1  12:48:49Z  canary-latency-rollback: in breach, waiting out the 90s hold (signal 2485 > 1000)
cycle 2  12:49:34Z  canary-latency-rollback: in breach, waiting out the 90s hold (signal 2485 > 1000)
cycle 3  12:50:19Z  canary-latency-rollback: FIRING, held for 90s: incident reported (signal 2485 > 1000)

## Incident: Canary subset latency regression

Scenario `canary-latency-rollback` (severity critical) fired for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2`.

The signal has held at **2485** (threshold > 1000 for 90s) since 2026-08-19T12:48:49Z.

### Diagnosis

A newly shifted traffic subset (canary) shows p99 latency far above the stable subset. The fastest safe move during an incident is to send traffic back to the stable subset and investigate the canary offline. This entry assumes the DestinationRule defines the failing subset and a stable subset.

### Evidence

| query | value |
| --- | --- |
| p99-stable-for-comparison | 287.5 |
| canary-request-share | 0.1925 |

### Configuration evidence

- canary-deployment-env (`Deployment demo/payments-v2`) `spec.template.spec.containers[*].env`: `NAME=payments-v2 MESSAGE=ok TIMING_50_PERCENTILE=1200ms UPSTREAM_URIS=http://ledger:9090 ERROR_RATE=0 TIMING_90_PERCENTILE=1500ms`
- canary-deployment-env (`Deployment demo/payments-v2`) `spec.template.spec.containers[*].resources`: `{}`

### Proposed patch (VirtualService)

```yaml
apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: payments
  namespace: demo
spec:
  hosts:
    - payments
  http:
    - route:
        - destination:
            host: payments
            subset: v1
          weight: 100
        - destination:
            host: payments
            subset: v2
          weight: 0
```

### Rollback

Restore the previous VirtualService weights once the canary's p99 is back under the threshold in a staging replay, or roll forward with a fixed canary image. The PR that shifted traffic contains the prior weights in its diff.

This scenario requires human approval; nothing was applied to the cluster.
cycle 4  12:51:04Z  canary-latency-rollback: still in breach, already reported, no second report (signal 2485 > 1000)
cycle 5  12:51:49Z  canary-latency-rollback: clear, a real answer: the signal is 312, back under 1000

## Resolved: Canary subset latency regression

Scenario `canary-latency-rollback` for `namespace=demo` `service=payments` `stable_subset=v1` `subset=v2` `workload=payments-v2` has recovered.

The signal is back to **312** (threshold > 1000). Open from 2026-08-19T12:48:49Z to 2026-08-19T12:51:49Z, a duration of 3m0s.

Data: CONSTRUCTED to match demo/proof-reports/canary-latency-rollback.md, not recorded.
  (The proof reports keep the incident report, not the raw Prometheus responses.)
  Taken from that file:
    - the signal value at firing (2485) and the time the breach began (12:48:49Z)
    - both evidence values (stable p99 287.5, canary share 0.1925)
    - the canary Deployment's env and resources
  Made up for this replay:
    - every Prometheus response body and the coverage count; every cycle after the first and the recovery value
    - the cycle timing (45s apart, so the 90s hold is easy to see)
  The detection, thresholds, patch and report are the real code. No pull request was opened,
  no token was read, nothing beyond loopback was contacted.
  No file was written (add --out DIR to write the dossier).

Next, on a real setup (a Prometheus that scrapes Istio metrics):
  meshmedic check --prometheus http://localhost:9090 --target service=payments,namespace=demo   # exits 1 if it cannot see the target
  meshmedic watch --prometheus http://localhost:9090 --target service=payments,namespace=demo
```

The blind example:

```console
$ meshmedic demo --scenario blind-no-telemetry
meshmedic demo: blind-no-telemetry
This is a REPLAY of data held inside this binary. Nothing is being watched and nothing is changed.
The real detector ran 2 cycles against a Prometheus served from this process on loopback.

cycle 1  13:00:00Z  canary-latency-rollback: BLIND, not clear: target unobserved: coverage probe returned no series: no mesh telemetry for this target
         `meshmedic check` would exit 1 here: 1 of 1 targets could not be observed.
cycle 2  13:00:45Z  canary-latency-rollback: clear, a real answer: the signal is 312, not > 1000

Data: CONSTRUCTED to match README.md design rule 1 (no proof run exists for this case), not recorded.
  (The proof reports keep the incident report, not the raw Prometheus responses.)
  Nothing in it is taken from a recorded run.
  Made up for this replay:
    - everything: the empty responses in cycle 1, the healthy value in cycle 2 and all timestamps
    - the case itself mirrors the unobservable control target in demo/calibration/live-run.txt
  The detection, thresholds, patch and report are the real code. No pull request was opened,
  no token was read, nothing beyond loopback was contacted.
  No file was written (add --out DIR to write the dossier).

Next, on a real setup (a Prometheus that scrapes Istio metrics):
  meshmedic check --prometheus http://localhost:9090 --target service=payments,namespace=demo   # exits 1 if it cannot see the target
  meshmedic watch --prometheus http://localhost:9090 --target service=payments,namespace=demo
```

## Flags

| flag | meaning |
| --- | --- |
| `--scenario NAME` | which replay to run; default `canary-latency-rollback` |
| `--list` | list the replays and exit |
| `--verbose` | also print the cycle summary line `watch` logs, and every PromQL query with the response the replay served |
| `--out DIR` | write `<id>-report.md`, `<id>-patch.yaml` and `<id>-resolved.md` into DIR. `watch` writes no local files; in GitOps mode the first two are the pull request body and the committed file. |
| `--catalog DIR` | use a catalog directory instead of the one in the binary, as the other commands do |

## Exit codes

Consistent with the other commands:

| code | meaning |
| --- | --- |
| 0 | the replay ran, including `blind-no-telemetry`: the demo worked, and `check` is what exits 1 on a blind target |
| 1 | the replay could not run: unknown scenario name (as in `render`), catalog invalid or missing the entry the replay needs, or `--out` not writable |
| 2 | a flag or argument the command does not understand |

## Tests

`cmd/meshmedic/demo_test.go` holds one golden file per scenario in
`cmd/meshmedic/testdata/`, plus the verbose and `--list` screens. The golden
files contain the real catalog's text, so a change to a catalog entry's
description, query or patch changes them on purpose. After reading the diff,
refresh them with:

```console
$ go test ./cmd/meshmedic -run Demo -update
```

`pkg/demo/demo_test.go` proves the replay goes through the shared code: it
compares the printed dossier with `report.Markdown` of the `detect.Incident`
the detector produced and the patch from `remediate.Render`, checks the hold
and resolution on the `detect.Cycle` and `detect.Resolution` structures, and
edits the catalog entry (threshold, description, patch template, signal query)
to show the demo output changes with it.

## Adding a scenario

Add `pkg/demo/fixtures/NAME.json` naming the catalog entries to watch, the
target parameters, the evidence answers, any configuration objects and the
cycles. Say in `source`, `recorded` and `constructed` where each number comes
from; `TestFixtures` fails if a fixture cannot be answered by the catalog's
queries or its source file does not exist. Never present constructed data as
recorded.
