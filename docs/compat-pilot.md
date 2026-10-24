# Compat pilot

The pilot [ADR 0001](decisions/0001-compat-engine.md) asks for before any
engine code is written: install ten widely used charts, unchanged, in four
throwaway worlds, and record every observation that differs from the world
without a mesh. The ADR is accepted, narrowed, or dropped on what this table
says, by the rule in [Deciding the ADR](#deciding-the-adr), which is written
down before the first run so the result cannot move it.

Status: tooling written, not yet run. Nothing below is a finding.

## What runs

| piece | what it does |
|---|---|
| [`hack/compat-pilot/world.sh`](../hack/compat-pilot/world.sh) | creates one kind cluster in one Istio configuration, with its own kubeconfig |
| [`hack/compat-pilot/observe.sh`](../hack/compat-pilot/observe.sh) | installs one chart, records what it does, removes it; one call is one run |
| [`hack/compat-pilot/difftable`](../hack/compat-pilot/difftable) | compares every mesh world with W0 and writes the table |
| [`hack/compat-pilot/charts.json`](../hack/compat-pilot/charts.json) | the ten charts, pinned, with the reason each is in |
| [`.github/workflows/compat-pilot.yml`](../.github/workflows/compat-pilot.yml) | 10 charts x 4 worlds, one job each, three runs per job, then the table |

The matrix runs on GitHub-hosted runners, which this public repository gets at
no cost (4 vCPU, 16 GB, 14 GB of disk; disk is the tight one, so the job frees
some first). It does not run on pull requests.

### Pinned environment

| | version | why |
|---|---|---|
| Istio | 1.31.3 (2026-10-22) | latest stable; 1.30.7 is the previous minor for a later comparison |
| Kubernetes | 1.36.4 (`kindest/node@sha256:099e0493…`) | inside the support window of both 1.31 and 1.30 |
| kind | v0.33.0 | its kindnet enforces NetworkPolicy (since v0.24, via kube-network-policies), which the ambient class depends on |
| Helm | v4.0.4 | the version Helmbulance pins |
| Gateway API | v1.6.0 | CRDs istiod expects in ambient |

Every record carries the versions its world ran with, read back from the
cluster rather than copied from this table.

### Worlds

| world | data plane | redirection | mTLS |
|---|---|---|---|
| W0 | none | none | none |
| W1 | sidecar | istio-init | STRICT |
| W2 | sidecar | istio-cni | STRICT |
| W3 | ambient | ztunnel | STRICT |

**One assumption of the ADR is already out of date.** Native sidecars have been
on by default since Istio 1.28.0 (`ENABLE_NATIVE_SIDECARS`, istio/istio#56428;
in 1.31 the value is `auto`, on when every kubelet is 1.33 or newer). The proxy
then runs as a Kubernetes sidecar container, which ends with the Job and starts
before the app's init containers. The two oldest classes in the issue trackers,
"the Job never completes because istio-proxy keeps running" and "an init
container has no network because the proxy is not up yet", should mostly be
gone in W1 and W2. The pilot runs the default deliberately, because the
default is what a new cluster gets. If those classes still show up, that is a
finding in itself. If they do not, the issues that report them are candidates
for a "fixed by native sidecars" comment (see [the queue](#from-a-difference-to-an-upstream-contribution)),
and a W1L world with native sidecars off can be added later for the clusters
that still run without them.

## The ten charts

Picked from a survey of the public issue trackers (2026-10-24): each one either
has a reported Istio failure we can try to reproduce, covers a failure class
the others do not, or is a control. Values are defaults unless the table says
otherwise. Every override is listed, because an override is a change to the
thing under test.

| chart | version | override | why it is in |
|---|---|---|---|
| podinfo | 6.15.0 | | control: a small HTTP app that should behave the same everywhere |
| argo-cd | 10.11.1 | | `redis-secret-init` hook Job reported hanging under a sidecar, [argo-helm#2887](https://github.com/argoproj/argo-helm/issues/2887), open, labelled `keep` |
| prometheus-pushgateway | 3.9.1 | | Service port named `metrics` with no protocol prefix, [prometheus-community/helm-charts#7404](https://github.com/prometheus-community/helm-charts/issues/7404), opened 2026-10-22, no replies |
| kube-prometheus-stack | 92.12.0 | | admission webhook patch Jobs ([#1183](https://github.com/prometheus-community/helm-charts/issues/1183), closed stale with a workaround), API server to webhook traffic, many scrape ports |
| redis (Bitnami) | 28.3.9 | | ships a NetworkPolicy by default and replicates between pods; an ambient report says 15008 is blocked, [bitnami/charts#31394](https://github.com/bitnami/charts/issues/31394), closed without a chart change |
| rabbitmq (Bitnami) | 16.0.14 | `image.repository=bitnamilegacy/rabbitmq`, `global.security.allowInsecureImages=true`, `replicaCount=3` | Erlang clustering between StatefulSet peers plus a default NetworkPolicy. The chart's default image no longer exists on `docker.io/bitnami`, so it points at the legacy copy |
| cloudnative-pg | 0.29.1 | | operator reported crashing under mTLS until `holdApplicationUntilProxyStarts` is set, [cloudnative-pg/charts#327](https://github.com/cloudnative-pg/charts/issues/327); API server to webhook traffic |
| nats | 2.15.1 | `config.cluster.enabled=true` | server-first protocol (INFO greeting) on client and route ports. The appProtocol fix is merged ([nats-io/k8s#761](https://github.com/nats-io/k8s/issues/761)), so this is a regression check |
| grafana | 14.0.1 | | HTTP app with init containers; the chart moved to grafana-community in 2026 |
| valkey | 0.12.0 | `replica.enabled=true` | official Valkey chart, replication between pods, no Istio issue on record: any result is new data |

Left out, and why: keycloakx (clustering needs `start` with an external
database, too much to build for one chart), Temporal and Airflow (need an
external database; Temporal's Istio fixes are merged), Harbor and Strimzi
(heavy, workaround-only evidence), cert-manager, external-secrets and
oauth2-proxy (no Istio failure on record), Gitea (its chart lives on
gitea.com, outside the search this pilot used), and every Bitnami database
chart whose pinned image tag is gone from `docker.io/bitnami`.

The Bitnami image situation is itself a finding about the ecosystem, not about
Istio: since the 2025 catalog change, a default install of several Bitnami
charts fails with ImagePullBackOff in every world, W0 included. The differ
reports that as "same", which is correct: it is not a mesh problem.

## What is observed

For each run, derived from what the chart rendered, never written per chart:

- **helm** — whether `helm install --wait --wait-for-jobs` succeeded within the
  chart's deadline, and `helm test` when the chart ships tests
- **workloads** — per Deployment, StatefulSet or DaemonSet: every pod Ready,
  and whether any container restarted
- **jobs** — every Job, hooks included, complete or not
- **ports** — every TCP Service port, probed from a client pod in the same
  namespace, so inside the same mesh:
  - *passive*: connect, send nothing for four seconds, record whether the
    server said anything. This is what catches server-first protocols behind
    a sniffing sidecar: the server greets, the proxy waits for the client to
    speak first, and nothing arrives.
  - *http*: a plain GET, compared only where W0 answered with an HTTP status.
- **istioctl analyze** — recorded as evidence beside each mesh world's result,
  never used for the verdict.

### How a difference becomes a finding

W0 is the oracle. An observation counts only if all three W0 runs agree on its
value; a chart that is flaky without a mesh cannot be blamed on the mesh for
being flaky with one. A mesh world's observation is then:

- a **finding** when it differs from W0 in 3 of 3 runs
- **flaky** when it differs in 1 or 2 runs: reported, never acted on
- **undecided** when either side has fewer than three runs, never "same"

Two rules keep the probes from inventing findings, and both have tests in
[`difftable`](../hack/compat-pilot/difftable/diff_test.go):

- A key that exists in one world and not the other compares as `absent`. A
  hook Job that W0 deletes on success and W1 leaves hanging is a difference.
- The HTTP probe is ignored where W0 did not speak HTTP. A sniffing sidecar
  turns a GET on a MySQL port into a 502, and that says something about the
  probe, not the chart.

### What the probes cannot see

- A client-first TCP protocol (Redis, PostgreSQL, AMQP) that is broken by the
  mesh shows up only through its consequences: replicas that never become
  Ready, restarts, a failed install. A port that accepts connections but
  silently drops the protocol is not detected. The engine proper would need
  protocol-aware probes; the pilot measures whether the cheap ones are enough.
- Traffic from outside the mesh. The client is always in the mesh, so
  "STRICT rejects a plaintext caller" is not tested here. That is a property
  of STRICT, not of the chart.
- kind's NetworkPolicy implementation is kube-network-policies, not Calico or
  Cilium. Whether it treats the ambient probe source (169.254.7.127) the way
  they do is unknown. A NetworkPolicy finding in W3 is checked by reading the
  policy, not just trusted.
- Docker Hub's anonymous pull limit applies to the runners. A world that fails
  on ImagePullBackOff in one run and not the others shows up as flaky, which
  is why flaky is never acted on.

## Deciding the ADR

Counted over the ten charts, after the run:

- **Accept** ADR 0001 as written if at least three charts show a finding in
  some mesh world that a documented Istio requirement explains and that has no
  merged upstream fix.
- **Narrow it to the atlas** (a published compatibility table, no daily
  contribution cadence) if one or two do.
- **Drop the engine** if none do, and publish that result: "ten popular charts
  work unchanged under Istio 1.31 in all three data-plane modes" is worth
  knowing, and it is also what the ADR's last consequence warned might happen.

Findings that no documented requirement explains are counted apart. They are
the most interesting rows, and the ones most likely to be a mistake in the
pilot, so each is read by hand before it counts for anything.

## From a difference to an upstream contribution

The ADR's queue (section 7) applies from the first finding, by hand, before
any of it is code:

1. **Search first.** An issue or PR that already covers the finding gets the
   run attached as a comment only if the run adds something: a newer chart
   version, a mode nobody tested, or proof that it is fixed.
2. **Certify before proposing a fix.** The smallest change (a port name, an
   `appProtocol`, a NetworkPolicy rule for 15008) is applied with `--set`, and
   the chart is run again in every world: the finding must clear 3 of 3, and
   nothing may break that passed before, W0 included. A fix without that does
   not go out.
3. **Issue before PR**, with the reproduction as one command.
4. **The repository's own rules**, read before writing anything: see
   [Target repositories](#target-repositories).
5. **Limits:** one contribution sent per day across all destinations; one open
   contribution per repository, counting Helmbulance's (both tools write to
   bitnami/charts, prometheus-community/helm-charts and grafana charts), and at
   least seven days between two to the same repository.
6. **Security** findings (mTLS not enforced, a policy not applied) never go to
   a public tracker. They go to the project's private disclosure channel.
7. **A person sends everything.** The tool writes packets; Kadirhan reads every
   line and posts it.

The measure is how many are merged, not how many are sent.

## Target repositories

Read from each repository on 2026-10-24. Response times are the median of a
sample of at most eight recent external PRs, so they are a rough guide, not a
measurement. Policies change, so a packet re-reads the repository's files
before it is built rather than trusting this table.

| repository | sign-off | AI assistance | conventions | first response / merge |
|---|---|---|---|---|
| argoproj/argo-helm | DCO | **disclose, draft PR**: [AGENTS.md](https://github.com/argoproj/argo-helm/blob/main/AGENTS.md) asks for the checkbox "I wrote this PR with the help of a LLM but I fully vetted the work" and a draft PR the human marks ready | `fix(argo-cd): …`, version bump, one `artifacthub.io/changes` entry, helm-docs | ~20 h / ~3 days |
| prometheus-community/helm-charts | DCO | identify: the PR template says "If you are an AI, please identify yourself" | `[chart] …`, one chart per PR, version bump, helm-unittest | ~14 h / days to weeks |
| grafana-community/helm-charts | DCO | identify (same template wording) | `[grafana] …`, one chart per PR, version bump, helm-unittest case | ~1 h / ~14 h |
| bitnami/charts | DCO | silent | `[bitnami/redis] …`, version bump, readme-generator | ~1 week, up to 4 |
| cloudnative-pg/charts | DCO | **strictest**: the org [AI policy](https://github.com/cloudnative-pg/governance/blob/main/AI_POLICY.md) refuses PRs "that result from running an AI tool over the codebase", refuses AI-written PR descriptions, can block the account, and requires an `Assisted-by:` trailer | issue first, conventional titles | ~17 days / ~7 weeks |
| nats-io/k8s | not stated (the server repo requires sign-off) | silent here; the nats-server policy probably applies | ct lint, terratest | ~30 h / ~4 days (one sample) |
| valkey-io/valkey-helm | DCO | silent | version bump, values.schema.json, helm-unittest | ~1 h / 2 to 60 days |
| istio/istio, istio/istio.io | EasyCLA | none yet; proposed in [istio/community#1691](https://github.com/istio/community/issues/1691) | release note for istio/istio | fast, mostly maintainers |

podinfo is the control and has no external merges in 90 days; it is not a
target.

What this means for the queue:

- **Every contribution is written by a person and says how it was made.** The
  pilot's evidence comes from a tool and the text is drafted with help, so
  every packet discloses that, in the form each repository asks for, even
  where the policy is silent. "Identify yourself" is answered plainly.
- **cloudnative-pg gets issues, not PRs.** Its policy describes the pilot
  almost word for word ("running a tool over the codebase to find
  improvements"). A reproduced failure is still worth an issue there; a
  patch is not worth an account block. The response times would make it a
  slow target anyway.
- **Bitnami last.** Its content looks frozen since the 2025 image change and a
  first response takes a week or more. A Bitnami finding goes to the atlas
  first and to the tracker only if it also holds for a maintained chart.
- **argo-helm, prometheus-community and grafana-community first**: they answer
  quickly, they accept agent-assisted work with disclosure, and they hold the
  two open issues the pilot tries to reproduce ([argo-helm#2887](https://github.com/argoproj/argo-helm/issues/2887),
  [helm-charts#7404](https://github.com/prometheus-community/helm-charts/issues/7404)).
- **Istio needs the CLA signed** before the first docs PR, and an issue there
  waits for a sidecar and ambient disagreement that the docs do not explain.

## Running it locally

One world at a time; an 8 GB laptop will not hold more. A world needs roughly
6 GB of free disk for the node image, Istio and a chart's images.

```console
$ export ISTIO_VERSION=1.31.3 KIND_NODE_IMAGE=kindest/node:v1.36.4@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed
$ hack/compat-pilot/world.sh W3
$ for run in 1 2 3; do hack/compat-pilot/observe.sh W3 nats $run results; done
$ hack/compat-pilot/world.sh --delete W3
$ go run ./hack/compat-pilot/difftable results
```

`world.sh` never touches `~/.kube/config`. Each world has its own kubeconfig
under `.mmc-state/`, and `observe.sh` refuses to run unless that kubeconfig
holds only the cluster `world.sh` created. The difftable needs W0 and at least
one mesh world, three runs each, to say anything.
