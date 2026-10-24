# Compat pilot

The pilot [ADR 0001](decisions/0001-compat-engine.md) asks for before any
engine code is written: install ten widely used charts, unchanged, in four
throwaway worlds, and record every observation that differs from the world
without a mesh. The ADR is accepted, narrowed, or dropped on what this table
says, by the rule in [Deciding the ADR](#deciding-the-adr), which was written
down before the first run so the result could not move it.

Status: run six times (2026-10-24). Two charts have a finding, both in
ambient, both from one cause, and both are cleared by a certified one-line fix.
A third, podinfo, fails its own `helm test` in the sidecar worlds and is
counted apart, for reasons given with the alternative reading. By the rule
below, the ADR is narrowed to the atlas: see [Decision](#decision).

## What runs

| piece | what it does |
|---|---|
| [`hack/compat-pilot/world.sh`](../hack/compat-pilot/world.sh) | creates one kind cluster in one Istio configuration, with its own kubeconfig |
| [`hack/compat-pilot/observe.sh`](../hack/compat-pilot/observe.sh) | installs one chart, records what it does, removes it; one call is one run |
| [`hack/compat-pilot/difftable`](../hack/compat-pilot/difftable) | compares every mesh world with W0 and writes the table |
| [`hack/compat-pilot/charts.json`](../hack/compat-pilot/charts.json) | the ten charts and two candidate fixes, pinned, with the reason each is in |
| [`.github/workflows/compat-pilot.yml`](../.github/workflows/compat-pilot.yml) | every entry x 4 worlds, one job each, three runs per job, then the table; `workflow_dispatch` takes a list of entry ids to rerun some alone |

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
| argo-cd | 10.11.1 | `redis.image.repository=mirror.gcr.io/library/redis` (from the third run on; same image, see [Results](#results)) | `redis-secret-init` hook Job reported hanging under a sidecar, [argo-helm#2887](https://github.com/argoproj/argo-helm/issues/2887), open, labelled `keep` |
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
  chart's deadline, and `helm test` when the chart ships tests. In runs 1 to 3
  the check for test hooks missed the quoted form (`"helm.sh/hook": test`), so
  only nats was tested, and the tests of grafana, kube-prometheus-stack and
  podinfo never ran; it is fixed from run 4 on. When a test fails, the logs of its failed test pods are recorded.
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

These rules keep the probes from inventing findings, and each has a test in
[`difftable`](../hack/compat-pilot/difftable/diff_test.go):

- A key that exists in one world and not the other compares as `absent`. A
  hook Job that W0 deletes on success and W1 leaves hanging is a difference.
- The HTTP probe is ignored where W0 did not speak HTTP. A sniffing sidecar
  turns a GET on a MySQL port into a 502, and that says something about the
  probe, not the chart.
- The passive probe is ignored in the sidecar worlds on ports a sidecar
  treats as HTTP (a name prefix or `appProtocol` of http, http2, grpc,
  grpc-web or h2c). In W3 it is still compared, because ztunnel carries bytes
  and does not parse HTTP. Added after the first run, where podinfo's gRPC port "greeted" in W0 and went silent behind
  a sidecar: an HTTP/2 server sends its SETTINGS frame as soon as a connection
  opens, and a sidecar opens nothing upstream until the client sends a
  request. Every real HTTP/2 client speaks first, so the silence breaks
  nothing.
- A run that could not pull an image is left out (see below).

### What the probes cannot see

- A client-first TCP protocol (Redis, PostgreSQL, AMQP) that is broken by the
  mesh shows up only through its consequences: replicas that never become
  Ready, restarts, a failed install. A port that accepts connections but
  silently drops the protocol is not detected. The engine proper would need
  protocol-aware probes; the pilot measures whether the cheap ones are enough.
- Traffic from outside the mesh. The client is always in the mesh, so
  "STRICT rejects a plaintext caller" is not tested here. That is a property
  of STRICT, not of the chart. (A chart's own test pods can still be outside
  the mesh; see [podinfo](#counted-apart-podinfos-tests-under-strict).)
- kind's NetworkPolicy implementation is kube-network-policies, not Calico or
  Cilium. Whether it treats the ambient probe source (169.254.7.127) the way
  they do is unknown. A NetworkPolicy finding in W3 is checked by reading the
  policy, not just trusted.
- Registries refuse pulls from shared runner addresses now and then. The
  first draft of this page expected that to show up as flaky. It does not: a
  world's three runs share one cluster on one runner, so a refused pull
  repeats 3 of 3 and looks like a finding. Runs that end with a container in
  ErrImagePull or ImagePullBackOff, in any pod including a Job's, are
  therefore left out of the comparison, with the image named, and a world
  left with fewer than three runs is undecided.
- The three runs of a world are not independent for the same reason: anything
  wrong with one runner or one cluster repeats in all of them. A finding
  counts here only when it held in two separate workflow runs.

## Results

### Runs

| run | commit | entries | jobs | what changed before it |
|---|---|---|---|---|
| [40094496780](https://github.com/Git-Hospital/meshmedic/actions/runs/40094496780) | 00f5a0a | 10 charts | 42, all passed | |
| [40098941189](https://github.com/Git-Hospital/meshmedic/actions/runs/40098941189) | f253ca7 | 10 charts, 2 fixes | 50, all passed | passive probe ignored on HTTP-family ports; containers recorded |
| [40103861930](https://github.com/Git-Hospital/meshmedic/actions/runs/40103861930) | 259fe76 | 10 charts, 2 fixes | 50, all passed | runs that could not pull an image left out; argo-cd's redis from a mirror |
| [40109270484](https://github.com/Git-Hospital/meshmedic/actions/runs/40109270484) | 7335fd1 | 10 charts, 2 fixes | 50, all passed | quoted test hooks found, so `helm test` runs for every chart that ships tests |
| [40115141791](https://github.com/Git-Hospital/meshmedic/actions/runs/40115141791) | c74bad4 | 10 charts, 2 fixes | 49 passed, 1 failed | `helm test --logs`; valkey's W2 job died downloading its tools (`curl: (35) Connection reset by peer`), so valkey W2 is undecided; podinfo is not counted (see [mistake 3](#mistakes-the-pilot-made-and-the-rules-they-left)) |
| [40119399052](https://github.com/Git-Hospital/meshmedic/actions/runs/40119399052) | 1624010 | 10 charts, 2 fixes | 50, all passed | failed test pods' logs read directly; every download retried |

Each workflow run is three runs per world, so the redis and rabbitmq verdicts
rest on eighteen installs per world and the fixes' on fifteen. Istio 1.31.3,
Kubernetes 1.36.4, native sidecars at their default (on) in every mesh world,
read back from istiod in every record. Runs 1 and 2 were re-tabled with the
tooling of run 3; the verdicts below are the same in every run unless a row
says otherwise.

| chart | W1 sidecar+init | W2 sidecar+cni | W3 ambient |
|---|---|---|---|
| podinfo | `helm test` fails (runs 4 and 6; tests not run before) | `helm test` fails (runs 4 and 6) | same |
| argo-cd | same (runs 3 to 6; undecided in runs 1 and 2) | same (runs 3 to 6) | same (runs 3 to 6) |
| prometheus-pushgateway | same | same | same |
| kube-prometheus-stack | same | same | same |
| redis | same | same | **differs** |
| rabbitmq | same | same | **differs** |
| cloudnative-pg | same | same | same |
| nats | same | same | same |
| grafana | same | same | same |
| valkey | same | same (undecided in run 5: tool download failed) | same |

argo-cd has one flaky key, in W1 in run 3 and in W3 in run 4: dex-server
restarted once in one of three runs (exit code 20 in run 3). Otherwise it is
the same; flaky is reported and not acted on. grafana's, kube-prometheus-stack's
and nats's tests pass in every world from run 4 on.

### Findings

**bitnami/redis 28.3.9, W3.** `redis-replicas` never becomes Ready and
restarts (CrashLoopBackOff, the container exiting 0), so `helm install --wait`
times out. W0, W1 and W2 install in 75 to 97 seconds.

**bitnami/rabbitmq 16.0.14, three replicas, W3.** The StatefulSet never
becomes Ready, containers are killed by their probes (exit 137), and the
management port 15672 does not answer HTTP.

Both charts ship a NetworkPolicy by default that admits their own ports from
any source and nothing else. In ambient, ztunnel carries pod-to-pod traffic to
port 15008 (HBONE) and the policy is enforced on the node, outside the pod, so
replication and clustering traffic is dropped. Istio documents this
requirement in [ambient and NetworkPolicy](https://istio.io/latest/docs/ambient/usage/networkpolicy/).
Sidecar worlds are unaffected because there the traffic still arrives on the
application's own port.

The cause above is an explanation, and the certificate is what tests it. The
two fix entries, `redis-np-15008` and `rabbitmq-np-15008`, add one ingress rule
and nothing else:

```console
--set-json 'networkPolicy.extraIngress=[{"ports":[{"port":15008,"protocol":"TCP"}]}]'
```

Both are **certified** in runs 2 to 6: W3 behaves like W0 in every run, W1
and W2 stay the same, nothing is flaky, and the fix's W0 behaves like the
unchanged chart's W0. Kubelet probes, which ambient sends from 169.254.7.127,
were not a factor: the charts' policies already admit any source on the probed
ports.

`istioctl analyze` ran on every install and said nothing about either: IST0118
(port naming) on rabbitmq, nothing on redis. On master (2026-10-24) Istio has
no analyzer or message about NetworkPolicy, HBONE or 15008.

Upstream state: [bitnami/charts#31394](https://github.com/bitnami/charts/issues/31394)
reports the redis case and was closed the day it was opened, with the user's
own `extraIngress` workaround and no chart change; a later comment says the
workaround did not help that commenter. No issue or PR in bitnami/charts
mentions 15008 otherwise.

Neither side is wrong here, which is why the pilot sends no chart fix. Bitnami
ships a deny-by-default policy on purpose and gives `networkPolicy.extraIngress`
as the extension point, which is exactly where the certified fix goes. Istio
documents that ambient needs 15008. The two behave as designed, and the user
who combines them finds out from a CrashLoopBackOff, because nothing warns:
not the chart, not `istioctl analyze`. The contribution that fits is the
warning.

### Counted apart: podinfo's tests under STRICT

**podinfo 6.15.0, W1 and W2.** `helm test` fails in every run of runs 4 and
6 and passes in W0 and W3. The app itself behaves the same in every world. The
failing test pod logs:

```text
timeout: failed to connect service "podinfo.pilot:9999" within 1s
```

The chart's test pods carry `sidecar.istio.io/inject: "false"`. The likely
cause: in the sidecar worlds they call podinfo in plaintext from outside the
mesh, and STRICT mTLS refuses them. In W3 the same annotation does nothing
(ambient's opt-out is `istio.io/dataplane-mode: none`), the test pods are
enrolled with the namespace, and the tests pass. This explanation fits the log
and the annotation but was not tested: no fix was applied and re-run, so unlike
redis and rabbitmq it is uncertified. `istioctl analyze` reports IST0135: the
annotation is deprecated in favour of the label of the same name.

The first commit of this page already said, under what the probes cannot see,
that "STRICT rejects a plaintext caller" is a property of STRICT and is not
tested here. It did not say such a finding would be left out of the count, and
the rule as written does not leave it out. It is counted apart anyway, and
that is a judgement made after seeing it, so here is the other reading: taken as written, the rule ("a documented Istio
requirement explains it, no merged upstream fix") is met by podinfo too, three
charts would have a finding, and the ADR would be accepted. The reasons for
not counting it:

- the caller is the chart's test pod, which leaves the mesh on purpose; every
  meshed client reaches podinfo in every world
- it needs STRICT, which these worlds chose; Istio's default is PERMISSIVE,
  where a plaintext caller is accepted (not measured here)
- there is no change for podinfo to make that is right everywhere. Guess: the
  opt-out dates from before native sidecars, when an injected test pod never
  completed, and removing it would bring that back on clusters without them.

### Not reproduced

- **argo-cd, [argo-helm#2887](https://github.com/argoproj/argo-helm/issues/2887)**
  (open, 27 comments, last 2026-09-01). The `redis-secret-init` hook Job
  completed in every run where the redis image could be pulled: 18 of 18 in
  W1, 15 of 15 in W2, 12 of 12 in W3 (runs 1 to 6; in runs 3 to 6, with the
  mirror, every world pulled it). With native sidecars the
  proxy ends with the Job. The issue's recent comments report the
  hang without Istio, from Argo CD syncing itself and a TTL race, which a
  `helm install` does not exercise, so the pilot has nothing new to add there
  and sends nothing.
- **kube-prometheus-stack**, the admission webhook patch Jobs of
  [#1183](https://github.com/prometheus-community/helm-charts/issues/1183):
  installs in W1 and W2 like in W0. The same native-sidecar effect.
- **cloudnative-pg, [cloudnative-pg/charts#327](https://github.com/cloudnative-pg/charts/issues/327)**:
  the operator starts in every world without `holdApplicationUntilProxyStarts`.
- **prometheus-pushgateway, [helm-charts#7404](https://github.com/prometheus-community/helm-charts/issues/7404)**:
  the port named `metrics` behaves the same in every world. Protocol sniffing
  recognises HTTP. An explicit `appProtocol` would still be correct, but the
  pilot measured no breakage it would fix, so it has nothing to certify.
- **nats** with clustering: the server-first INFO greeting arrives in every
  world. The merged appProtocol fix holds.

The ADR expected the two oldest classes in the trackers, Jobs that never end
and init containers without network, to be the bulk of the findings. With
native sidecars on by default since Istio 1.28 they did not appear at all in
this sample. Clusters that run with native sidecars off still have them; the
pilot did not test that (a W1L world).

### Mistakes the pilot made, and the rules they left

1. **podinfo's gRPC port (run 1)** differed on the passive probe in W1 and W2.
   It was the probe: an HTTP/2 server greets with a SETTINGS frame, a sidecar
   does not. The passive probe is now ignored on HTTP-family ports, with a
   test.
2. **argo-cd (runs 1 and 2)** "differed" in W2 and W3 the first time, and in
   W1 and W2 the other way round the second time (W0 itself failed). Its redis
   image failed to pull from `ecr-public.aws.com` in whichever worlds the
   registry refused, three of three, because a world's runs share one runner.
   Runs ending in ErrImagePull or ImagePullBackOff are now left out, with a
   test, and argo-cd pulls the same image (`library/redis:8.6.4-alpine`) from
   `mirror.gcr.io`.

3. **podinfo (run 5)** "failed" `helm test` in every world, W0 included.
   `helm test --logs` reads the logs of every test pod after the suite, and
   podinfo deletes its passing test pods (`hook-succeeded`), so Helm failed on
   "unable to get pod logs ... not found". The verdict was "same", for the
   wrong reason. The tool now runs `helm test` without `--logs` and reads the
   failed test pods' logs itself; run 6 is the second run of the finding.
4. **The test hook check (runs 1 to 3)** missed the quoted form of the
   annotation, so the tests of grafana, kube-prometheus-stack and podinfo
   never ran. Nothing was counted
   wrongly, but three runs said "same" about tests that had not run.

The first three would have been counted wrongly by a single run read without
care, and each was caught because a later run repeated the measurement. That is the reason for the
two-workflow-run rule in [What the probes cannot see](#what-the-probes-cannot-see).

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

### Decision

Two charts, redis and rabbitmq, have a finding that a documented Istio
requirement explains and that has no merged upstream fix. No chart has an
unexplained finding. podinfo's test failure is counted apart, as explained in
[its section](#counted-apart-podinfos-tests-under-strict); counted as written
it would make three and the ADR accepted, and that is said here so the
reader can weigh it. By the rule above, written before the first run, **ADR
0001 is narrowed to the atlas.**

The two rules added after run 2 (leave out runs that could not pull, count a
finding only when two workflow runs agree) do not change the count. Under the
original rule, argo-cd's differences in run 1 were unexplained and would have
been read by hand before counting, and reading them by hand finds the
registry.

The count is also weaker than two suggests: both findings have one cause and
one publisher. What the pilot does suggest, as a guess to test rather than a
result: the class that is left after native sidecars is ambient against a
default NetworkPolicy. Bitnami ships a NetworkPolicy enabled by default in
both charts tested here, and as far as we know in many others, so a targeted
atlas run over Bitnami charts whose pods talk to each other is the obvious
next measurement, and the one most likely to move this decision.

What narrowing means for the ADR's sections:

- **Kept:** the atlas (section 6), with the pilot's tooling as its first
  version, and the queue's rules (section 7) for the few packets there are.
- **Not built now:** the engine as a product mode (`meshmedic compat` and its
  certification flow in the main binary), and the daily contribution cadence.
  A cadence needs a supply of findings, and ten popular charts gave one cause.
- **Highest-leverage contribution:** a proposal to Istio for an `istioctl
  analyze` check, because the class is invisible to analyze and hits every
  chart that ships a default NetworkPolicy, not one chart at a time.

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
