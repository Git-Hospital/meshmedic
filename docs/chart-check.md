# Check a Helm chart

`meshmedic chart check` reads the rendered manifests of a Helm chart and lists
what in them is likely to break under Istio, before any cluster exists. It is
static: it needs no cluster, no Prometheus, no Istio and, if you already have
rendered YAML, not even Helm.

What it finds are **candidates**. Each one is a field in your manifests that
matches a requirement Istio documents (or, for one rule, a requirement that
Istio does not document, said plainly). None of them is a failure observed in a
cluster, and a clean result is not a guarantee. The command says so in every
report, and so does [Limits](#limits).

The rules are written against **Istio 1.31**.

## The one command

```console
$ meshmedic chart check ./my-chart -f values.yaml
$ helm template my-release ./my-chart | meshmedic chart check --manifests -
```

| flag | meaning |
|---|---|
| `CHART` | a chart directory or `.tgz`; rendered with `helm template` as a child process (an argument vector, never a shell) |
| `--manifests FILE` or `--manifests -` | rendered manifests from a file or stdin; helm is not needed and not run |
| `--mesh sidecar\|ambient\|both` | which Istio data-plane mode to check; default `both`. Each check runs only for the modes it applies to |
| `--format text\|json` | `text` for a person (default), `json` for a program |
| `--verbose` | text only: add every finding's evidence, the list of checks that ran and the full limits (the default report is grouped by rule and short) |
| `-f`, `--values FILE` | values file, repeatable, passed to helm |
| `--set`, `--set-string`, `--set-json` | value overrides, repeatable, passed to helm |
| `--namespace NS` | release namespace (default `default`); also the namespace given to objects that name none |
| `--release-name NAME` | helm release name (default `release`) |
| `--kube-version V` | Kubernetes version helm renders for |
| `--native-sidecars=BOOL` | assume sidecar proxies are native sidecars (default true, which is Istio 1.31 on Kubernetes 1.33 or newer); `false` for older clusters |

A chart and `--manifests` together, or helm flags together with `--manifests`,
are an error: a flag that has no effect would look like it had one. With a chart
argument and no `helm` on the `PATH`, the command stops with an error that names
the `--manifests` way. `--include-crds` is always passed to helm, so conversion
webhooks in `crds/` are visible to MM-CHART-007.

### Exit codes

The same three the other commands use.

| code | meaning |
|---|---|
| 0 | no candidates (notes do not count) |
| 1 | at least one candidate |
| 2 | usage error, render error, unreadable or invalid input, or an input with no Kubernetes objects |

A **note** is a finding that is weaker than a candidate (see MM-CHART-001): it is
reported but never sets exit code 1. An input with no objects is exit 2, never 0: a run that read nothing must not
look like a clean one.

### Determinism

The same input gives the same bytes. There is no clock, no network and no
cluster access in the command (helm may fetch dependencies when it renders a
chart; render once and use `--manifests` to take that out). Findings are sorted
by id, mode, resource and reason. One caveat sits in the input, not in the
command: a chart that uses `randAlphaNum` or similar (podinfo names its test
pods that way) renders different YAML on each `helm template`, and its findings
carry those names.

## Sample output

Bitnami redis 28.3.9 with its default values, rendered from the chart archive
(`helm pull oci://registry-1.docker.io/bitnamicharts/redis --version 28.3.9`).
Real output.

```console
$ meshmedic chart check redis-28.3.9.tgz --namespace pilot --release-name redis
meshmedic chart check, rules written against Istio 1.31
2 candidates and 0 notes: MM-CHART-001 ambient x2
read chart redis-28.3.9.tgz: 14 objects, 2 pod templates, namespace pilot, modes ambient+sidecar;
  sidecar proxies assumed native

MM-CHART-001  ambient  A NetworkPolicy does not admit ambient HBONE traffic (TCP 15008) (candidate)
  effect:   Ambient carries traffic between meshed pods over TCP port 15008 (HBONE) and the node
            enforces this NetworkPolicy on it, so a policy that admits no traffic to 15008 drops
            pod-to-pod traffic to this workload.
  istio:    "However, NetworkPolicy is enforced on the host, outside the pod. This means that if you
            have preexisting NetworkPolicy in place that, for example, will deny list inbound
            traffic to an ambient pod on every port but 443, you will have to add an exception to
            that NetworkPolicy for port 15008."
            https://istio.io/latest/docs/ambient/usage/networkpolicy/
  fix:      Add an ingress rule to the NetworkPolicy that admits TCP 15008 (ports: [{port: 15008,
            protocol: TCP}]). If the chart has a value for extra ingress rules, put the rule there
            instead of patching the template.
  confirm:  Install the chart in a throwaway ambient cluster and watch the traffic between its own
            pods (replication, clustering): pods that never become Ready or keep restarting, and
            connection errors toward port 15008 in the ztunnel logs, confirm it. Install it without
            the mesh as the comparison.
  affected:
    all 2 are hit through NetworkPolicy/redis spec.ingress = 6379/TCP from any source
    - StatefulSet pilot/redis-master
    - StatefulSet pilot/redis-replicas

These are candidates found by reading manifests, not failures observed in a cluster. A clean result
is not a guarantee that the chart works under Istio. A note is weaker than a candidate: it depends
on something the manifests cannot show and does not set the exit code.
limits: Only rendered manifests are read. Namespace enrolment, PeerAuthentication, policies from
  outside the chart and the CNI are assumed, not read, and nothing is run. Run with --verbose for
  the full list.
```

Exit code 1 (32 lines). The two StatefulSets are hit through one NetworkPolicy, and
the report says so once instead of repeating the rule for each. The same chart with the one-rule fix, a values file with
`networkPolicy.extraIngress` (the certified fix of the
[compat pilot](compat-pilot.md#findings)):

```yaml
# fix-values.yaml
networkPolicy:
  extraIngress:
    - ports:
        - port: 15008
          protocol: TCP
```

```console
$ meshmedic chart check redis-28.3.9.tgz --namespace pilot --release-name redis -f fix-values.yaml
meshmedic chart check, rules written against Istio 1.31
No candidates in 14 objects.
read chart redis-28.3.9.tgz: 14 objects, 2 pod templates, namespace pilot, modes ambient+sidecar;
  sidecar proxies assumed native

These are candidates found by reading manifests, not failures observed in a cluster. A clean result
is not a guarantee that the chart works under Istio. A note is weaker than a candidate: it depends
on something the manifests cannot show and does not set the exit code.
limits: Only rendered manifests are read. Namespace enrolment, PeerAuthentication, policies from
  outside the chart and the CNI are assumed, not read, and nothing is run. Run with --verbose for
  the full list.
```

Exit code 0. The report still says what was looked at, so "clean" can be told
from "did not look"; `--verbose` also lists the checks that ran.

The same chart as rendered YAML on stdin, ambient only, as JSON:

```console
$ helm template redis redis-28.3.9.tgz --namespace pilot | meshmedic chart check --manifests - --namespace pilot --mesh ambient --format json
```

<details>
<summary>The JSON report (exit code 1)</summary>

```json
{
  "schemaVersion": 1,
  "tool": "meshmedic chart check",
  "istioVersion": "1.31",
  "input": "manifests (stdin)",
  "namespace": "pilot",
  "modes": [
    "ambient"
  ],
  "nativeSidecars": true,
  "examined": {
    "objects": 14,
    "podTemplates": 2
  },
  "checks": [
    {
      "id": "MM-CHART-001",
      "check": "np-hbone-probe",
      "mode": "ambient",
      "findings": 2
    },
    {
      "id": "MM-CHART-004",
      "check": "reserved-port",
      "mode": "ambient",
      "findings": 0
    },
    {
      "id": "MM-CHART-007",
      "check": "webhook-backend-in-mesh",
      "mode": "ambient",
      "findings": 0
    },
    {
      "id": "MM-CHART-008",
      "check": "l7-without-waypoint",
      "mode": "ambient",
      "findings": 0
    }
  ],
  "findings": [
    {
      "id": "MM-CHART-001",
      "check": "np-hbone-probe",
      "title": "A NetworkPolicy does not admit ambient HBONE traffic (TCP 15008)",
      "mode": "ambient",
      "level": "candidate",
      "resource": {
        "kind": "StatefulSet",
        "namespace": "pilot",
        "name": "redis-master"
      },
      "trigger": {
        "kind": "NetworkPolicy",
        "name": "redis",
        "path": "spec.ingress",
        "value": "6379/TCP from any source"
      },
      "reason": "no ingress rule admits TCP 15008 (HBONE)",
      "effect": "Ambient carries traffic between meshed pods over TCP port 15008 (HBONE) and the node enforces this NetworkPolicy on it, so a policy that admits no traffic to 15008 drops pod-to-pod traffic to this workload.",
      "docs": [
        {
          "url": "https://istio.io/latest/docs/ambient/usage/networkpolicy/",
          "heading": "Ambient traffic overlay and Kubernetes NetworkPolicy",
          "quote": "However, NetworkPolicy is enforced on the host, outside the pod. This means that if you have preexisting NetworkPolicy in place that, for example, will deny list inbound traffic to an ambient pod on every port but 443, you will have to add an exception to that NetworkPolicy for port 15008."
        }
      ],
      "fix": "Add an ingress rule to the NetworkPolicy that admits TCP 15008 (ports: [{port: 15008, protocol: TCP}]). If the chart has a value for extra ingress rules, put the rule there instead of patching the template.",
      "confirm": "Install the chart in a throwaway ambient cluster and watch the traffic between its own pods (replication, clustering): pods that never become Ready or keep restarting, and connection errors toward port 15008 in the ztunnel logs, confirm it. Install it without the mesh as the comparison.",
      "evidence": [
        {
          "kind": "StatefulSet",
          "name": "redis-master",
          "path": "spec.template.metadata.labels",
          "value": "app.kubernetes.io/component=master,app.kubernetes.io/instance=redis,app.kubernetes.io/managed-by=Helm,app.kubernetes.io/name=redis,app.kubernetes.io/version=8.10.2,helm.sh/chart=redis-28.3.9"
        },
        {
          "kind": "NetworkPolicy",
          "name": "redis",
          "path": "spec.podSelector",
          "value": "selects this pod; isolates ingress"
        },
        {
          "kind": "NetworkPolicy",
          "name": "redis",
          "path": "spec.ingress[0]",
          "value": "6379/TCP from any source"
        }
      ]
    },
    {
      "id": "MM-CHART-001",
      "check": "np-hbone-probe",
      "title": "A NetworkPolicy does not admit ambient HBONE traffic (TCP 15008)",
      "mode": "ambient",
      "level": "candidate",
      "resource": {
        "kind": "StatefulSet",
        "namespace": "pilot",
        "name": "redis-replicas"
      },
      "trigger": {
        "kind": "NetworkPolicy",
        "name": "redis",
        "path": "spec.ingress",
        "value": "6379/TCP from any source"
      },
      "reason": "no ingress rule admits TCP 15008 (HBONE)",
      "effect": "Ambient carries traffic between meshed pods over TCP port 15008 (HBONE) and the node enforces this NetworkPolicy on it, so a policy that admits no traffic to 15008 drops pod-to-pod traffic to this workload.",
      "docs": [
        {
          "url": "https://istio.io/latest/docs/ambient/usage/networkpolicy/",
          "heading": "Ambient traffic overlay and Kubernetes NetworkPolicy",
          "quote": "However, NetworkPolicy is enforced on the host, outside the pod. This means that if you have preexisting NetworkPolicy in place that, for example, will deny list inbound traffic to an ambient pod on every port but 443, you will have to add an exception to that NetworkPolicy for port 15008."
        }
      ],
      "fix": "Add an ingress rule to the NetworkPolicy that admits TCP 15008 (ports: [{port: 15008, protocol: TCP}]). If the chart has a value for extra ingress rules, put the rule there instead of patching the template.",
      "confirm": "Install the chart in a throwaway ambient cluster and watch the traffic between its own pods (replication, clustering): pods that never become Ready or keep restarting, and connection errors toward port 15008 in the ztunnel logs, confirm it. Install it without the mesh as the comparison.",
      "evidence": [
        {
          "kind": "StatefulSet",
          "name": "redis-replicas",
          "path": "spec.template.metadata.labels",
          "value": "app.kubernetes.io/component=replica,app.kubernetes.io/instance=redis,app.kubernetes.io/managed-by=Helm,app.kubernetes.io/name=redis,app.kubernetes.io/version=8.10.2,helm.sh/chart=redis-28.3.9"
        },
        {
          "kind": "NetworkPolicy",
          "name": "redis",
          "path": "spec.podSelector",
          "value": "selects this pod; isolates ingress"
        },
        {
          "kind": "NetworkPolicy",
          "name": "redis",
          "path": "spec.ingress[0]",
          "value": "6379/TCP from any source"
        }
      ]
    }
  ],
  "candidates": 2,
  "notes": 0,
  "notice": "These are candidates found by reading manifests, not failures observed in a cluster. A clean result is not a guarantee that the chart works under Istio. A note is weaker than a candidate: it depends on something the manifests cannot show and does not set the exit code.",
  "blindSpots": [
    "Only the rendered manifests are read. A requirement that lives in a container image (for example an image whose USER is 1337 with no runAsUser in the manifest), in application code (the address it binds, the ports it really listens on) or in the cluster is not seen.",
    "The release namespace is assumed to be enrolled in the mesh, and a pod is in the mesh unless it opts out itself. Namespace labels, PeerAuthentication, mesh-wide settings and policies from outside the chart are not read; STRICT mutual TLS is assumed where a finding depends on it.",
    "NetworkPolicy: only policies the chart renders are read, not a default-deny policy from elsewhere; the peers of an ingress rule are not evaluated for port 15008; the IPv6 probe address is not checked; the behavior of a particular CNI (Calico, Cilium) is not modelled.",
    "Nothing is run. Traffic between the chart's own pods, client-first protocols broken by the mesh, whether a webhook is ever called and its failure policy, and whether a container port is really bound are not observed.",
    "A pod that leaves the mesh on purpose (for example a Helm test pod with sidecar injection disabled) is not checked, although under STRICT mutual TLS a pod outside the mesh cannot call one inside it.",
    "helm template cannot evaluate lookup or cluster capabilities, so a real install can render something different."
  ]
}
```

</details>

## Findings

Each rule has a stable id, `MM-CHART-NNN`, one per check. An id is never reused
for another check, and a retired check leaves its number unused. A finding
carries the id, the mode it is about, the resource, the field and value that
triggered it, what will happen under Istio, the documentation it rests on, a
fix, and how to confirm it in a cluster. The fix text is generic manifest-level
guidance: it names no value of any particular chart, because a rendered
manifest cannot show them. For a Bitnami chart the redis fix above is
`networkPolicy.extraIngress`; another chart names it differently or has no such
value.

| id | check | mode | what it reads | documentation |
|---|---|---|---|---|
| MM-CHART-001 | `np-hbone-probe` | ambient | a NetworkPolicy that isolates ingress for a pod and admits no traffic to TCP 15008 (HBONE): a candidate. A kubelet probe port not admitted from 169.254.7.127 is a **note**, because it depends on the CNI | [Ambient and NetworkPolicy](https://istio.io/latest/docs/ambient/usage/networkpolicy/) |
| MM-CHART-002 | `protocol-declared` | sidecar | a server-first port (25, 53, 3306, 27017) declared as HTTP or gRPC; one container port declared with different protocols by its Services | [Application requirements: server first protocols](https://istio.io/latest/docs/ops/deployment/application-requirements/) |
| MM-CHART-003 | `uid-1337` | sidecar | a container that runs as user 1337 | [Application requirements: pod requirements](https://istio.io/latest/docs/ops/deployment/application-requirements/) |
| MM-CHART-004 | `reserved-port` | sidecar, ambient | a containerPort or Service targetPort that Istio uses inside the pod | [Application requirements: ports used by Istio](https://istio.io/latest/docs/ops/deployment/application-requirements/) |
| MM-CHART-005 | `classic-sidecar-startup` | sidecar | a pod with a classic sidecar that has init containers or no `holdApplicationUntilProxyStarts` | [Common problems: injection](https://istio.io/latest/docs/ops/common-problems/injection/) |
| MM-CHART-006 | `classic-sidecar-job` | sidecar | a Job, CronJob or run-to-completion Pod with a classic sidecar | [Native sidecars: sidecar woes](https://istio.io/latest/blog/2023/native-sidecars/) |
| MM-CHART-007 | `webhook-backend-in-mesh` | sidecar, ambient | a webhook, APIService or CRD conversion webhook whose backend Service selects a pod in the mesh | none: see below |
| MM-CHART-008 | `l7-without-waypoint` | ambient | an L7 AuthorizationPolicy, mesh VirtualService or Service-attached route, with no waypoint Gateway in the chart | [Ambient L7 features](https://istio.io/latest/docs/ambient/usage/l7-features/) |

The exact text of each rule (what it reads and when it fires, the effect, the
fix, how to confirm, and every quoted passage) is in the
[`Rules()`](../pkg/chartcheck/rules.go) list and in the JSON of every finding.
The passages are copied word for word from the Istio documentation and were
checked against the 1.31 and 1.32 documentation on 2026-11-11.

### MM-CHART-001 np-hbone-probe (ambient)

> However, NetworkPolicy is enforced on the host, outside the pod. This means
> that if you have preexisting NetworkPolicy in place that, for example, will
> deny list inbound traffic to an ambient pod on every port but 443, you will
> have to add an exception to that NetworkPolicy for port 15008.

This is the candidate. Fix: add an ingress rule that admits TCP 15008. If the
chart has a value for extra ingress rules, use it instead of patching the
template. Confirm: install in a throwaway ambient cluster and watch the traffic
between the chart's own pods; pods that never become Ready or restart
repeatedly, and connection errors toward port 15008 in the ztunnel logs,
confirm it.

**The probe part is a note, not a candidate.** Ambient sends kubelet probes
from 169.254.7.127. The same Istio page, section "Ambient, health probes, and
Kubernetes NetworkPolicy", does not say that a NetworkPolicy blocks them
everywhere. It says:

> Various CNI implementations solve this in different ways and seek to either
> work around the problem by silently excluding kubelet health probes from
> normal policy enforcement, or configuring policy exceptions for them.

> Note: If your workload, namespace, or cluster enforces Kubernetes
> NetworkPolicy, you must allow both the IPv4 and IPv6 addresses used by ambient
> mode. Depending on your CNI, packets with these addresses may otherwise be
> blocked, which will cause application pod health probes to fail once they join
> the ambient mesh.

and, of its example policy, "Depending on your CNI, kubelet probes and
link-local addresses may be ignored by this policy, or be blocked by it". (Read
on <https://istio.io/latest/docs/ambient/usage/networkpolicy/> on 2026-11-12,
Istio 1.32 documentation.) The requirement is therefore conditional on the CNI,
and a manifest cannot show the CNI. So a probe port that the chart's
NetworkPolicy does not admit from 169.254.7.127 is reported as a note: visibly
weaker, with its own wording ("depending on the CNI"), and it never sets exit
code 1. The 2026-11-12 cluster run did not reproduce it on kind's default CNI
(argo-cd's pods stayed Ready), which is what the page leads one to expect of a
CNI that excludes kubelet probes from policy. Fix for a cluster whose CNI does
block them: admit the probed port from `ipBlock` `169.254.7.127/32` (and the
IPv6 address on IPv6 or dual-stack clusters). Confirm: probes that fail in an
ambient cluster with your CNI (pod not Ready, liveness restarts, probe failures
in `kubectl describe pod`) confirm it; probes that pass mean your CNI does not
block them.

### MM-CHART-002 protocol-declared (sidecar)

> Both of these features work by inspecting the initial bytes of a connection to
> determine the protocol, which is incompatible with server first protocols. In
> order to support these cases, follow the Explicit protocol selection steps to
> declare the protocol of the application as TCP.

Fix: set `appProtocol: tcp` on the Service port (or name it `tcp-...`); give
every Service that targets one container port the same protocol. Confirm:
connect to the port from a meshed client and check that the server's first bytes
arrive.

### MM-CHART-003 uid-1337 (sidecar)

> Application UIDs: Ensure your pods do not run applications as a user with the
> user ID (UID) value of 1337 because 1337 is reserved for the sidecar proxy.

Fix: change `runAsUser`. Confirm: `kubectl exec POD -c CONTAINER -- id -u`, and
look for the container's outbound requests in the sidecar's access log.

### MM-CHART-004 reserved-port (sidecar, ambient)

> To avoid port conflicts with sidecars, applications should not use any of the
> ports used by Envoy.

Sidecar list: 15000, 15001, 15002, 15004, 15006, 15008, 15020, 15021, 15053,
15090. Ambient list: 15001, 15006, 15008 (from the ambient traffic redirection
page). Fix: move the application to another port and change the Service
`targetPort` and probes with it. A `containerPort` the application never
listens on can be removed. Confirm: bind errors in the application log.

### MM-CHART-005 classic-sidecar-startup (sidecar)

> Many applications execute commands or checks during startup, which require
> network connectivity. This can cause application containers to hang or restart
> if the istio-proxy sidecar container is not ready.

Applies to pods whose proxy is a classic sidecar: a pod with the annotation
`sidecar.istio.io/nativeSidecar: "false"`, or every pod under
`--native-sidecars=false`. With the default, a chart is flagged only for pods
that opt out. Fix: use native sidecars; or set `holdApplicationUntilProxyStarts`
in `proxy.istio.io/config` (init containers that need the network still need
native sidecars). Confirm: connection errors in the first start's init and
application logs.

### MM-CHART-006 classic-sidecar-job (sidecar)

> If an application container intentionally exits (typically from usage in a
> Job), Istio's container will still run and keep the pod running indefinitely.

Same condition as MM-CHART-005 for the proxy. Fix: use native sidecars, opt the
pod out of injection, or call `/quitquitquit` on port 15020 as the Job's last
step. Confirm: the Job's pod shows the application container Completed while
istio-proxy keeps running.

### MM-CHART-007 webhook-backend-in-mesh (sidecar, ambient)

**No documentation anchor.** Istio's documentation does not say that the
Kubernetes API server cannot reach an in-mesh webhook or APIService backend.
The nearest statement is general and not about webhooks: "When STRICT mutual TLS
is enabled, non-Istio workloads cannot communicate to Istio services, as they
will not have a valid Istio client certificate."
(<https://istio.io/latest/about/faq/security/>). That the API server is refused
is an inference from it. The report says this in the finding instead of citing
nothing.

Fix: keep the backend's port out of mTLS enforcement. For sidecar, annotate the
pod `traffic.sidecar.istio.io/excludeInboundPorts` with the target port (the
check honours this annotation) or use a port-level PERMISSIVE PeerAuthentication;
for ambient, or when the backend needs no mesh, label the pod
`istio.io/dataplane-mode: none` (ambient) or `sidecar.istio.io/inject: "false"`
(sidecar). Confirm: with STRICT mTLS, run `kubectl apply --dry-run=server` on an
object the webhook covers and read the error; `kubectl get apiservice` shows
`Available False` for an APIService.

A configuration that lists the same backend in several webhooks gives one
finding that lists every webhook in its evidence, because they share a cause
and a fix.

### MM-CHART-008 l7-without-waypoint (ambient)

> The ztunnel cannot enforce L7 policies. If a policy with rules matching L7
> attributes is targeted with a workload selector (rather than attached with a
> targetRef), such that it is enforced by a ztunnel, it will fail safe by
> becoming a DENY policy.

Fix: deploy a waypoint (a Gateway with `gatewayClassName: istio-waypoint`) and
attach the policy with `targetRefs` and routes through `parentRefs`, or reduce
the policy to layer 4 attributes. Confirm: `kubectl get gateway -A`, then send
the request the policy or route should affect. A DENY policy with only L7
attributes is skipped by ztunnel and breaks nothing (the L4 policy page); the
check cannot tell it from an ALLOW policy and flags both.

## The JSON report

`schemaVersion` is 1. It changes when a field is removed or changes meaning;
adding a field does not change it. The `title`, `level` and `notes` fields were added with the note level (they are additions).

| field | meaning |
|---|---|
| `schemaVersion` | 1 |
| `tool` | `meshmedic chart check` |
| `istioVersion` | the Istio release the rules were written against, `1.31` |
| `input` | `chart PATH`, `manifests PATH` or `manifests (stdin)`, as given |
| `namespace` | the release namespace used for objects that name none |
| `modes` | the modes checked, sorted |
| `nativeSidecars` | the `--native-sidecars` assumption |
| `examined` | `objects` and `podTemplates` read |
| `checks` | one entry per check and mode that ran: `id`, `check`, `mode`, `findings` |
| `findings` | the candidates (below), sorted by id, mode, resource, reason |
| `candidates` | the findings of level `candidate`; the exit code is 1 when it is not 0 |
| `notes` | the findings of level `note`; they do not affect the exit code |
| `notice` | the sentence that these are candidates and a clean result is no guarantee |
| `blindSpots` | what the checks cannot see |

A finding has `id`, `check`, `title` (the rule's heading), `mode`, `level` (`candidate` or `note`), `resource` (`kind`, `namespace`, `name`;
the namespace is empty for cluster-scoped kinds), `trigger` (`kind`, `name`,
`path`, `value`: the field whose value made the check fire), `reason` (what the
check saw in this resource), `effect` (what will happen under Istio, one
sentence), `docs` (a list of `url`, `heading`, `quote`; empty when no page
states the requirement), `docsNote` (present when `docs` is empty), `fix`,
`confirm`, and `evidence` (every field the check read, as `kind`, `name`,
`path`, `value`).

## Limits

The report prints these in every run.

- Only the rendered manifests are read. A requirement that lives in a container
  image (an image whose `USER` is 1337 with no `runAsUser` in the manifest), in
  application code (the address it binds, the ports it really listens on) or in
  the cluster is not seen.
- The release namespace is assumed to be enrolled in the mesh, and a pod is in
  the mesh unless it opts out itself. Namespace labels, PeerAuthentication,
  mesh-wide settings and policies from outside the chart are not read; STRICT
  mutual TLS is assumed where a finding depends on it.
- NetworkPolicy: only policies the chart renders are read; the peers of an
  ingress rule are not evaluated for port 15008 (HBONE arrives from the client
  pods the rule already admits on the application port); the IPv6 probe address
  is not checked; the CNI is not modelled, which is why the probe finding is a
  note.
- Nothing is run. Traffic between the chart's own pods, client-first protocols
  broken by the mesh, whether a webhook is ever called and its failure policy,
  and whether a container port is really bound are not observed.
- A pod that leaves the mesh on purpose, such as a Helm test pod with
  `sidecar.istio.io/inject: "false"`, is not checked, although under STRICT
  mutual TLS it cannot call a pod inside the mesh. This is a known miss: see
  podinfo below.
- `helm template` cannot evaluate `lookup` or cluster capabilities.

Where candidates tend to be noise, from the validation below: MM-CHART-002 also
fires when two Service ports of one Service reach one container port, which an
application can serve on purpose (argo-cd's server does); MM-CHART-005 and
MM-CHART-006 fire only for pods that opt out of native sidecars, unless you pass
`--native-sidecars=false`; MM-CHART-004 fires on a declared `containerPort` the
application never listens on.

## Validation against cluster observations

The rules came from a controlled experiment's static checks, were moved into the
product, and were then run over the ten charts of the
[compat pilot](compat-pilot.md), at the pinned versions and with the values the
pilot installed them with. The cluster side is one real run on 2026-11-12:
Istio 1.31.3, STRICT mTLS, one kind node per world, ambient and sidecar. The
static side is `hack/chart-check/validate.sh`, which pulls the charts with helm
v4.0.4 and runs the command; it is the reproduction, and the rendered inputs are
not committed (the charts are public and pinned).

What the cluster run saw: ambient, redis and rabbitmq break (their default
NetworkPolicy drops port 15008) and every other chart behaved as without a
mesh. Sidecar, the API server could not reach the webhook services of
cloudnative-pg (`cnpg-webhook-service:443`) and kube-prometheus-stack
(`kube-prometheus-stack-operator:443`), podinfo's `helm test` failed (its test
pods are outside the mesh), and everything else behaved as without a mesh.
valkey never installed, so there is no observation for it.

What the run did not exercise: traffic between a chart's own pods, so a rule
that is flagged and observed "same" may be a real problem the run could not see.
Those rows say "not observed", not "false positive".

| chart (version) | mode | flagged by | observed in a cluster | verdict |
|---|---|---|---|---|
| podinfo 6.15.0 | ambient | nothing | as without a mesh | agree |
| podinfo 6.15.0 | sidecar | nothing | `helm test` fails | **missed**: the test pods opt out of injection and call a pod in the mesh; no check looks at that |
| argo-cd 10.11.1 | ambient | MM-CHART-001, 5 candidates: no rule admits 15008 for 5 workloads (dex-server, notifications-controller, redis, repo-server, application-controller) | as without a mesh | **not observed**. The run made no traffic between the chart's own components (for example the server and the repo-server calling redis), which is what a missing 15008 rule would drop. Candidate for a real problem, unconfirmed |
| argo-cd 10.11.1 | ambient | the other 2 of the 7 findings are probe **notes**: repo-server liveness on 8084 and application-controller readiness on 8082 are admitted only from a namespace selector, not from 169.254.7.127 | pods Ready, no restarts | **explained, conditional on the CNI**. Istio's page says that whether such probes are blocked depends on the CNI ("Depending on your CNI ..."), and some CNIs exclude kubelet probes from policy. That kind's default CNI did so is consistent with the page but was not verified. They are notes, not candidates, and do not set the exit code |
| argo-cd 10.11.1 | sidecar | MM-CHART-002: container port 8080 of the server declared `http` by one Service port and `https` by another | as without a mesh | **not observed**, hypothesis only. The chart sends two ports of one Service (80 and 443) to container port 8080. The notes written for the rule before this run name this exact case as its expected false-positive class (the server answers both on one port); that was not verified here. The rule is broader than the passage it quotes, which speaks of the same port number, and it is left as it is |
| prometheus-pushgateway 3.9.1 | both | nothing | as without a mesh | agree |
| kube-prometheus-stack 92.12.0 | sidecar | MM-CHART-007, 2 findings (the mutating and the validating admission configuration, backend `kube-prometheus-stack-operator:443`) | API server could not reach `kube-prometheus-stack-operator:443` | **confirmed** |
| kube-prometheus-stack 92.12.0 | ambient | MM-CHART-007, same 2 findings | reachable, 3 of 3 runs | **not observed**: the worlds have one node, so the API server shares the node with every pod, and the experiment's notes guess that ambient leaves traffic from the pod's own node alone (not tested). Whether it breaks with the API server on another node is open |
| redis 28.3.9 | ambient | MM-CHART-001, 2 findings (redis-master, redis-replicas): the NetworkPolicy admits 6379 only | `redis-replicas` never Ready | **confirmed** |
| redis 28.3.9 | sidecar | nothing | as without a mesh | agree |
| redis 28.3.9 with the 15008 fix | both | nothing | the certified fix: ambient as without a mesh | agree |
| rabbitmq 16.0.14 | ambient | MM-CHART-001, 1 finding (the StatefulSet): the NetworkPolicy admits its own ports only | StatefulSet never Ready | **confirmed** |
| rabbitmq 16.0.14 | sidecar | nothing | as without a mesh | agree |
| cloudnative-pg 0.29.1 | sidecar | MM-CHART-007, 2 findings (mutating and validating configuration, backend `cnpg-webhook-service:443`) | API server could not reach `cnpg-webhook-service:443` | **confirmed** |
| cloudnative-pg 0.29.1 | ambient | MM-CHART-007, same 2 findings | reachable, 3 of 3 runs | **not observed**, same reason as kube-prometheus-stack |
| nats 2.15.1 | both | nothing | as without a mesh | agree |
| grafana 14.0.1 | both | nothing | as without a mesh | agree |
| valkey 0.12.0 | both | the render fails with the given values (exit 2) | never installed | no observation to compare; the command reports the render error and does not call the chart clean |

Counts. Of the five breaks the cluster run saw (redis and rabbitmq in ambient,
cloudnative-pg and kube-prometheus-stack in sidecar, podinfo's test in
sidecar), four are flagged and one is missed. Over the 18 chart-and-mode cells
(valkey left out, the redis fix not counted): 9 agree (nothing flagged,
nothing observed), 4 are confirmed, 3 are flagged and not observed for a
stated reason (the two ambient webhook cells, argo-cd sidecar), 1 is missed
(podinfo sidecar), and 1 is mixed: argo-cd ambient, where 5 candidates are not
observed and 2 probe notes are explained as conditional on the CNI. **Unexplained
flags: 0.** That is a relabelling, so its basis is the page's wording quoted
under MM-CHART-001, not the cluster run: the run only shows the probes passed.
Before the note level existed the same two findings were candidates and were
counted as unexplained.
No check was removed or tuned to produce this table. The only change made
after the first run was to group the webhooks of one configuration that point
to one backend into a single finding (cloudnative-pg has nine webhooks, so it
first gave nine findings per mode); it changes the count of findings, not which
charts are flagged.

The regression test for the redis pair is
[`TestRedisDefaultVersusFixed`](../pkg/chartcheck/chartcheck_test.go): the
default NetworkPolicy is flagged for ambient, the one with the extra 15008 rule
is not. `np-redis-default.yaml` and `np-redis-fixed.yaml` in
[`pkg/chartcheck/testdata`](../pkg/chartcheck/testdata) are trimmed from the
chart's real render.

### What the static side printed

```console
$ hack/chart-check/validate.sh
podinfo	6.15.0	exit 0	none
argo-cd	10.11.1	exit 1	ambient MM-CHART-001 np-hbone-probe candidate x5; ambient MM-CHART-001 np-hbone-probe note x2; sidecar MM-CHART-002 protocol-declared candidate x1
prometheus-pushgateway	3.9.1	exit 0	none
kube-prometheus-stack	92.12.0	exit 1	ambient MM-CHART-007 webhook-backend-in-mesh candidate x2; sidecar MM-CHART-007 webhook-backend-in-mesh candidate x2
redis	28.3.9	exit 1	ambient MM-CHART-001 np-hbone-probe candidate x2
redis-fixed	28.3.9	exit 0	none
rabbitmq	16.0.14	exit 1	ambient MM-CHART-001 np-hbone-probe candidate x1
cloudnative-pg	0.29.1	exit 1	ambient MM-CHART-007 webhook-backend-in-mesh candidate x2; sidecar MM-CHART-007 webhook-backend-in-mesh candidate x2
nats	2.15.1	exit 0	none
grafana	14.0.1	exit 0	none
valkey	0.12.0	exit 2: meshmedic chart check: helm template failed: Error: execution error at (valkey/templates/statefulset.yaml:3:4): Replica mode requires persistent storage. Please set replica.persistence.size (e.g., '5Gi')
```

Each line is chart, version, exit code, and the findings per mode, id and check,
with a count. `redis-fixed` is the redis chart with
`--set-json 'networkPolicy.extraIngress=[{"ports":[{"port":15008,"protocol":"TCP"}]}]'`.

## Where the rules come from

The eight checks were first written as the static instrument of a compatibility
experiment, against ten charts, and are moved here with their fixtures. Each
rule's documentation quote, and the list of false-positive classes noted above,
come from that work. The fixtures in
[`pkg/chartcheck/testdata`](../pkg/chartcheck/testdata) each exercise one rule
and a test fails if a fixture sets off another one.
