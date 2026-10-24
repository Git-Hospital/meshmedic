# Compat pilot 2: tools that program Istio

The [first pilot](compat-pilot.md) installed ten popular charts unchanged and
found one class of failure: a default NetworkPolicy in ambient. Those charts
do not use Istio; they only run under it. This pilot looks at software that
does use it, starting with the tools that write Istio routing for you.

Status: run twice (2026-10-24). Every cell came out as the page predicted
before the first run: in ambient without a waypoint both tools report a
canary held at 10% and no request reaches it. By the rule below, the tool
group stops here. Everything under
[Expected](#expected-written-before-the-first-run) and
[Deciding what comes next](#deciding-what-comes-next) was written before the
first run (commit 46066be), so the result cannot move it; see
[Results](#results).

## The question

Argo Rollouts and Flagger run a canary by writing route weights: "send 10% of
the requests to the new version". With Istio they write a VirtualService.
Istio's own documentation says two things about that in ambient mode:

- "Usage of VirtualService with the ambient data plane mode is considered
  Alpha" ([L7 features](https://istio.io/latest/docs/ambient/usage/l7-features/)),
  and the migration guide recommends an HTTPRoute bound to the Service instead.
- L7 routing runs only in a waypoint. Without one, "the source ztunnel
  performs L4 load balancing directly across these instances"
  ([ztunnel troubleshooting](https://istio.io/latest/docs/ambient/usage/troubleshoot-ztunnel/)).

Neither tool's documentation mentions ambient (searched 2026-10-24: no match
for "ambient", "waypoint" or "ztunnel" in argoproj/argo-rollouts,
fluxcd/flagger or the Argo Rollouts Gateway API plugin). `istioctl analyze`
has no message for a VirtualService that no waypoint will apply. So the
question is what a user who switches a namespace to ambient actually gets
when the tool reports a canary at 10%.

## What runs

| piece | what it does |
|---|---|
| [`hack/compat-pilot/world.sh`](../hack/compat-pilot/world.sh) | the first pilot's worlds, plus W4 |
| [`hack/compat-pilot/canary/tool.sh`](../hack/compat-pilot/canary/tool.sh) | installs the scenario's controller once per world, outside the mesh |
| [`hack/compat-pilot/canary/canary.sh`](../hack/compat-pilot/canary/canary.sh) | one run: deploy, start the canary, wait for the hold, send the requests, record |
| [`hack/compat-pilot/canary/scenarios.json`](../hack/compat-pilot/canary/scenarios.json) | the four scenarios, pinned |
| [`hack/compat-pilot/difftable`](../hack/compat-pilot/difftable) | the first pilot's differ, run with `-oracle W1 -worlds W3,W4` |
| [`.github/workflows/compat-pilot-2.yml`](../.github/workflows/compat-pilot-2.yml) | every scenario x 3 worlds, three runs per job, then the table |

Istio 1.31.3, Kubernetes 1.36.4, kind v0.33.0, Helm v4.0.4 and Gateway API
v1.6.0, as in the first pilot. Gateway API CRDs are installed in every world.

### Worlds

| world | data plane | L7 routing happens in | mTLS |
|---|---|---|---|
| W1 | sidecar, istio-init | the client's sidecar | STRICT |
| W3 | ambient | nowhere (no waypoint) | STRICT |
| W4 | ambient, a waypoint for the namespace (`istioctl waypoint apply --enroll-namespace`) | the waypoint | STRICT |

**W1 is the oracle**, not W0. Without a mesh there is no weighted routing to
compare with: a canary weight is an Istio feature, and sidecar mode is where
both tools document it.

### Scenarios

| id | tool | routes with |
|---|---|---|
| `rollouts-istio` | Argo Rollouts v1.10.1 (chart 2.43.8) | VirtualService, host `podinfo-stable` |
| `rollouts-gatewayapi` | Argo Rollouts v1.10.1 with the Gateway API plugin v0.17.0 | HTTPRoute, parent the Service `podinfo-stable` |
| `flagger-istio` | Flagger 1.45.0, `meshProvider=istio` | the VirtualService Flagger generates, host `podinfo` |
| `flagger-gatewayapi` | Flagger 1.45.0, `meshProvider=gatewayapi:v1` | HTTPRoute, `gatewayRefs` the Service `podinfo` |

The app is podinfo, stable 6.14.1, canary 6.15.0; `GET /api/info` says which
version answered. Each tool holds the canary at 10% without metrics: Argo
Rollouts with `setWeight: 10` then `pause: {}`, Flagger with `maxWeight: 10`
and a confirm-promotion hook on the load tester's `/gate/halt`, which always
refuses. The HTTPRoute scenarios bind the route to the Service (the GAMMA
form), which neither tool's documentation shows: their examples use an
ingress Gateway. That is part of what is tested.

## What is observed

Per run, from a client pod in the same namespace and so in the same mesh:

| key | value |
|---|---|
| `tool:initial` | the tool's phase before the canary: `Healthy` (Argo), `Initialized` (Flagger), or what it was stuck in after 5 minutes |
| `tool:held` | the phase after the canary started: `Paused` (Argo), `WaitingPromotion` (Flagger), or what it was stuck in |
| `route:canary-weight` | the canary weight in the route object itself, which is what the data plane is given |
| `route:accepted` | for an HTTPRoute, its `Accepted` condition; `n/a` for a VirtualService |
| `traffic:canary` | of 400 requests, one connection each: `zero`, `at-weight` (within three standard deviations of 10%, 22 to 58) or `off-weight` |
| `traffic:errors` | `0` or `>0` requests that got no version back |

The counts themselves, the tool's status, the route and `istioctl analyze`
are kept in the record as evidence. A finding is what the first pilot calls
one: the same difference from W1 in 3 of 3 runs, in two separate workflow
runs, with runs that could not pull an image left out.

## Expected, written before the first run

These are guesses, and the point of writing them down is to be wrong in
public if they are wrong.

| scenario | W3 | W4 |
|---|---|---|
| `rollouts-istio` | tool held at 10%, route says 10, traffic **zero** | same as W1 (VirtualService at a waypoint, Alpha) |
| `rollouts-gatewayapi` | traffic **zero**; the route may not be accepted | same as W1 |
| `flagger-istio` | tool held at 10%, route says 10, traffic **zero** | same as W1 |
| `flagger-gatewayapi` | traffic **zero**; Flagger may not reach its hold if it waits for the route to be accepted | same as W1 |

The reasoning for "zero": both tools leave the stable Service selecting only
stable pods (Argo Rollouts adds the stable pod-template hash to its selector;
Flagger's `podinfo` Service selects the primary). Without a waypoint nothing
applies the weights, so every request goes to the stable pods, while the
tool reports a canary at 10%.

## Deciding what comes next

Counted after the run, per difference from W1:

1. **A W3 difference while the tool reports its hold** is Istio's documented
   behaviour that the tool does not warn about. It is the first pilot's class
   again ("both sides as designed, nothing warns"), and what it is worth is a
   documentation contribution to the tool: issue first, then a short ambient
   section (a waypoint is required; the Gateway API route works there, if W4
   says so).
2. **A W4 difference** contradicts what Istio documents. It is read by hand,
   then reproduced without the tool (the same VirtualService or HTTPRoute
   applied by hand). If it reproduces, it is an Istio issue; if not, it is
   the tool's.
3. **A difference that weakens a security property** (traffic reaching a
   version or a workload a policy was meant to keep it from) is not
   published anywhere public. It goes to the project's private disclosure
   channel, and this page says only that a report was made.

And for the plan:

- If at least one scenario shows a difference under rule 2, or under rule 1
  that the expectations above did not predict, the tool group continues:
  Knative's net-istio (ambient support requested in knative-extensions/net-istio#1360,
  open) and an ext_authz policy through oauth2-proxy.
- If every cell matches the expectations, the tool group stops after its one
  documentation contribution, and the next pilot moves to operators exercised
  through their own resources (a CloudNativePG Cluster, Vitess, the Zalando
  Postgres operator), where the open issue reports are about sidecars.

## Results

| run | commit | jobs |
|---|---|---|
| [40126396676](https://github.com/Git-Hospital/meshmedic/actions/runs/40126396676) | 46066be | 12 observe jobs, all passed |
| [40127366085](https://github.com/Git-Hospital/meshmedic/actions/runs/40127366085) | 46066be | 12 observe jobs, all passed |

Three runs per world in each, so every cell below rests on six runs of 400
requests.

| scenario | W3 ambient | W4 ambient+waypoint |
|---|---|---|
| `rollouts-istio` | **differs**: `traffic:canary` zero | same |
| `rollouts-gatewayapi` | **differs**: `traffic:canary` zero | same |
| `flagger-istio` | **differs**: `traffic:canary` zero | same |
| `flagger-gatewayapi` | **differs**: `traffic:canary` zero | same |

In every run of every world the tool reached its hold (`Paused` for Argo
Rollouts, `WaitingPromotion` for Flagger), the route held a canary weight of
10, and no request failed. What reached the canary out of 400:

| | W1 sidecar | W3 ambient | W4 ambient+waypoint |
|---|---|---|---|
| all four scenarios | 27 to 51 | 0, in all 24 runs | 25 to 48 |

`istioctl analyze` reported nothing in any world.

**The tools.** Neither one is wrong by its own documentation: each wrote the
weight it was asked to write, and Istio documents that ambient applies L7
routing only in a waypoint. A user who moves a namespace to ambient keeps a
canary that reports 10% and serves none of it, and nothing says so, the
same shape as the first pilot's finding. Under rule 1 that is worth one
documentation contribution to each tool: an ambient note saying that
weighted routing needs a waypoint, and that both the VirtualService and the
Service-bound HTTPRoute work once there is one (W4, six runs each). Neither
tool's docs show the Service-bound HTTPRoute; both accepted it unchanged.

**Istio, one thing the run turned up that the page did not ask about.** In
W3 the HTTPRoute is `Accepted` and its `ResolvedWaypoints` condition is
`True`, with the message "istio.io/use-waypoint label missing from parent
and parent namespace; in ambient mode, route will not be respected". The
status is `True` on purpose: the review of the PR that added it
(istio/istio#55381) asked not to alarm sidecar users, who need no waypoint.
But the reason is lost as well. The conversion computes
`MissingUseWaypointLabel`, and the status writer keeps only the message,
with the reason `ResolvedWaypoints` it uses on success
(`pilot/pkg/config/kube/gateway/conditions.go`, 1.31.3 and master). So
neither `istioctl analyze` nor a tool can tell the warning from success
without matching the text. That is a small, concrete change to propose: keep
the reason, and report it from the condition analyzer only for parents in an
ambient namespace. A VirtualService gets no status at all, so for the
`*-istio` scenarios there is no signal to keep.

**Decision.** No W4 difference, and no W3 difference the expectations did
not predict. By the rule, the tool group stops after its documentation
contributions, and the next pilot moves to operators exercised through
their own resources. Knative and ext_authz are not run.

## Boundaries

- **Differential conformance of the data plane** (does ztunnel enforce what
  the sidecar enforces, policy by policy) belongs to MeshDesync and its
  private disclosure track, not to this pilot.
- **Migrating a live namespace from sidecar to ambient** is a day-2
  scenario; each run here starts from a fresh namespace in one mode.
  Helmbulance owns day-2 behaviour.
- **One version of each tool.** The result is about Argo Rollouts v1.10.1 and
  Flagger 1.45.0 with Istio 1.31.3.

## Running it locally

```console
MMC_GATEWAY_API=1 ISTIO_VERSION=1.31.3 hack/compat-pilot/world.sh W4
hack/compat-pilot/canary/tool.sh W4 rollouts-istio
hack/compat-pilot/canary/canary.sh W4 rollouts-istio 1 results
go run ./hack/compat-pilot/difftable -oracle W1 -worlds W3,W4 results
hack/compat-pilot/world.sh --delete W4
```

The local run uses whatever node architecture kind gives you. The Gateway API
plugin pinned in `scenarios.json` is the linux-amd64 binary the CI runners
need; on an arm64 machine, change it to the arm64 build of the same release.
