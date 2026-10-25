# 0001: A differential compatibility engine, and fixes that carry a proof

Status: narrowed to the atlas (2026-10-24), by the pilot's rule. Proposed
2026-10-23. The pilot found two charts that break under Istio, both in
ambient, both from one cause, both cleared by a certified fix: see
[the pilot's decision](../compat-pilot.md#decision). Sections 6 and 7 stand;
the engine as a product mode and the daily cadence are not built now. The
rest of this record is the proposal as written.

## Context

MeshMedic today answers one question: a mesh that was working is now failing,
what broke and what is the fix. It answers it from Prometheus, for one cluster,
for whoever runs it. An internal review on 2026-10-22 made two of its limits
concrete:

- Nothing has ever applied a rendered patch. `remediate.Render` has three
  callers and none of them applies (`cmd/meshmedic/main.go:231`, `:306-330`,
  `pkg/proof/run.go:466-471`). The proofs show that an entry fires and names
  the culprit, not that its fix heals anything, and at least the
  DestinationRule and VirtualService patches are suspected no-ops or
  overwrites.
- Its evidence comes from faults its own author injected. Every proof is a home
  game.

There is a second question nobody answers today: an application that works
without a mesh, does it still work inside Istio, in which data-plane mode, and
if not, what is the smallest change that fixes it. Istio documents part of the
answer ([application requirements](https://istio.io/latest/docs/ops/deployment/application-requirements/),
[ambient and NetworkPolicy](https://istio.io/latest/docs/ambient/usage/networkpolicy/)),
`istioctl analyze` and Kiali lint part of it statically, and none of them runs
the application. We found no tool that installs a chart under Istio, observes
it break, and proves a fix. Absence of search hits is not proof, but the issue
trackers show the cost of the gap: charts break under Istio for years in
public, and the fixes that do merge are small chart changes, for example
appProtocol on Services ([bitnami/charts#14169](https://github.com/bitnami/charts/pull/14169),
[rabbitmq/cluster-operator#888](https://github.com/rabbitmq/cluster-operator/pull/888)),
port fixes ([temporalio/helm-charts#639](https://github.com/temporalio/helm-charts/pull/639)),
or an optional init check ([bitnami/charts#31277](https://github.com/bitnami/charts/pull/31277)).
Others stay open or go stale ([prometheus-community/helm-charts#7404](https://github.com/prometheus-community/helm-charts/issues/7404),
[argoproj/argo-helm#2887](https://github.com/argoproj/argo-helm/issues/2887),
[grafana/helm-charts#237](https://github.com/grafana/helm-charts/issues/237)).
Ambient NetworkPolicy fixes (port 15008, the kubelet probe source) are
documented by Istio and almost never merged in charts.

## Decision

Add a second engine to MeshMedic beside `watch`, built on one idea: **the same
application in a world without a mesh is the oracle for the same application
in a world with one.**

### 1. Worlds

A world is a throwaway kind cluster in one Istio configuration:

| world | data plane | redirection | mTLS |
|---|---|---|---|
| W0 | none | none | none |
| W1 | sidecar | istio-init | STRICT |
| W2 | sidecar | istio-cni | STRICT |
| W3 | ambient | ztunnel | STRICT |
| W4 (later) | ambient + waypoint | ztunnel | STRICT |

Istio and Kubernetes versions are pinned per run and recorded in every result.
The engine refuses any kube context it did not create, the way
`meshmedic-prove` refuses to run without `--yes-inject-faults`.

### 2. Observations, not rules, find the problem

The engine installs the chart unchanged in every world and records the same
observations in each, derived from the rendered manifests rather than written
per chart:

- every pod Ready within a deadline, restart counts, container exit reasons
- every Job and hook completes
- `helm test`, when the chart ships tests
- every Service port reachable from an in-mesh client, protocol-aware: an HTTP
  request for HTTP ports, a TCP handshake plus first-bytes check for the rest
- `istioctl analyze` messages, kept as evidence, never as the verdict

A finding is a difference: an observation that holds in W0 and fails in some
Wn, reproduced in 3 of 3 runs. Detection needs no knowledge of the failure.
That is what lets it find things nobody has documented yet.

### 3. Diagnosis is a catalog, held to the catalog's bar

Explaining a difference does need knowledge. A compat entry has the same
discipline as an incident entry: an id, the observation pattern it explains,
the static evidence it reads from the manifests (port names, appProtocol,
securityContext, NetworkPolicy rules, UID, ports in Istio's reserved range),
a fix template, and a lock hash. It does not merge until a real chart has
broken in a world and its fix has been certified (below). Seed entries come
from the documented requirements: protocol for server-first and unnamed ports,
NetworkPolicy missing 15008 or the probe source in ambient, istio-init under a
restricted Pod Security level, Jobs held open by the proxy, UID 1337, reserved
ports. A difference no entry explains goes to the recorder, the same way
`pkg/recorder` handles unmatched incidents: a human decides whether it is a
new entry, an Istio bug, or a documentation gap.

### 4. Every fix carries a certificate

A fix is accepted only when, with the patch applied:

- the failing observation passes in every world where it failed, 3 of 3 runs
- nothing that passed before fails, in any world, W0 included
- the chart's own checks still pass (`helm lint`, its unit tests if present)

The certificate records the chart version, the worlds, the Istio and
Kubernetes versions, the runs, and the diff. The same harness then closes the
review's first finding for the incident catalog: inject the fault, apply the
entry's patch to the throwaway cluster, and require the signal to clear. An
incident entry that cannot earn a certificate drops to `report-only`.

### 5. Three ways to use it

- `meshmedic compat ./chart` for chart authors and platform teams, locally or
  as a GitHub Action on every pull request. JSON output with a
  `schemaVersion`, exit codes 0 (compatible), 1 (finding), 2 (usage), 3
  (could not decide: a world failed to come up), the same contract as
  Helmbulance and GitHealthCheck.
- `meshmedic lint ./chart`, static and cluster-free, made only of entries that
  hold a certificate. A rule that has never been proven on a real chart does
  not ship in the linter.
- `meshmedic watch`, unchanged: live incidents from Prometheus.

### 6. An atlas, rebuilt every night

A scheduled GitHub Actions workflow in the Git-Hospital org runs the most-used
charts from Artifact Hub through W0-W3 on the latest Istio release and, when
one exists, the current release candidate. Public repositories get these
runners at no cost, which is what makes this possible for a team without a
budget. The result is a published table: chart, version, world, verdict, the
entry that explains it, the certified fix, and the state of the fix upstream.
It answers the question Istio's own tracker keeps asking
([istio/istio.io#16494](https://github.com/istio/istio.io/issues/16494),
"What works in Ambient?") with runs instead of prose, and it turns every Istio
release candidate into a regression run over real applications.

### 7. The upstream queue

Each certified finding becomes a contribution packet: the issue text, the
reproduction (one command), the certificate, and the patch prepared to the
target repository's own rules. Packets go to whichever destination owns the
fix:

| what was found | where it goes |
|---|---|
| a chart violates a documented requirement | an issue with the reproduction, then a small PR to the chart |
| an open or stale issue the atlas reproduces, or shows fixed | a comment with the run, so maintainers can act or close |
| a requirement that holds in practice but is not documented | a docs PR to istio.io |
| sidecar and ambient disagree where the docs say they should not | an issue to istio/istio |
| a check `istioctl analyze` could make statically and does not | a proposal to Istio |
| anything that weakens security (mTLS not enforced, a policy not applied) | never public: the project's private disclosure channel |

The tool never posts. It writes packets to a local queue; a person reads every
line, edits it, and sends it. The queue enforces what keeps this from being
spam, and these limits are code, not intentions:

- at most one packet sent per day across all destinations
- at most one open contribution per repository, and at least seven days
  between two contributions to the same repository
- no packet when an existing issue or PR already covers the finding (searched
  first); add evidence to it only when the evidence is new
- each repository's contribution profile is checked before a packet is built:
  DCO sign-off, CLA, PR title and version-bump conventions, and its policy on
  AI assistance. Where a project asks for disclosure, the packet discloses
  ([argo-helm AGENTS.md](https://github.com/argoproj/argo-helm/blob/main/AGENTS.md)
  requires a draft PR and an LLM checkbox the human ticks); where a project
  does not accept AI-assisted contributions, it gets an issue with the
  reproduction and no patch
- the measure of success is the merge rate, not the number sent

## Consequences

- MeshMedic gets a second, larger audience (anyone shipping or adopting a chart
  in Istio, and anyone migrating from sidecar to ambient) without weakening
  the first.
- The bench stops being a home game: every certified compat finding is a real
  third-party failure with a known fix, and becomes a bench case.
- The trust boundary moves. `watch` stays credential-free and never writes.
  `compat` writes, but only to clusters it created and destroys; that is
  stated in SECURITY.md before the first release that has it.
- Most of `pkg/detect`, `pkg/prom`, `pkg/promql`, `pkg/baseline` and
  `pkg/calibrate` do not apply to compat mode. Reused: the proof spec shape
  (argv commands, idempotent reset, expectations, the blind outcome),
  `remediate.Render`, the lock and approve flow, `pkg/kube`. New: worlds,
  observations, the differ, certification, the queue.
- A machine with 8 GB of memory runs only one world at a time. Local work
  is a single world; the matrix runs in CI.
- The supply of findings is unknown. A pilot measures it before the engine is
  built (below). If few popular charts break, the atlas is still useful as a
  compatibility table, but the daily contribution cadence is not sustainable
  and we say so.

## First step: the pilot

Before any engine code: 10 widely used charts, W0 and W3 locally, W1 and W2 in
one CI run. For each: does it differ, can the difference be explained by a
documented requirement, does the smallest fix heal it, does an upstream issue
already exist. The pilot's table decides whether this ADR is accepted.

The pilot's charts, worlds, observations and decision rule are in
[`docs/compat-pilot.md`](../compat-pilot.md).

## Not decided here

- Which charts form the atlas and how many (by Artifact Hub stars, downloads,
  or the issue evidence above).
- Whether W4 (waypoint) and Istio version sweeps come in the first atlas.
- Where the atlas is published (a page in this repo, or its own repository in
  the org).
- How the compat cases are shared with mesh-incidents-bench (the bench's
  `case.yaml` schema is a separate decision).
