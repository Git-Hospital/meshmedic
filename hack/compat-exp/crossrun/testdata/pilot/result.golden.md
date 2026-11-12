# crossrun

Run 1 40109270484, run 2 40119399052, gap 13.88 h (at least 12 h).

- WARNING: no static check results given: S is 0 and regions B and E are decided without it (S is a lower bound)

Sample: 10 charts of the frozen list, 0 replaced by reserves.

## Decision

N_new=0 P_new=0 D=0 P_D=0 S=0

Region **E**: Drop (anything else).

| row | matched | failed clauses |
|---|---|---|
| A | false | N_new >= 3, P_new >= 3, D >= 2, P_D >= 2 |
| B | false | N_new >= 2, P_new >= 2, S >= 2 |
| C | false | N_new >= 3, P_new >= 3 |
| E | true |  |

S is not computed (no static check results); it is 0 here.

If every unresolved, unexplained or awaiting cell counted (0 cells): N_new=0 P_new=0 D=0 P_D=0 S=0, region E.

## Futility (section 7)

N_max 0, P_max 0 (0 cells neither credited nor excluded, 0 new publishers among them). Reachable regions: E.

**N_max <= 1 or P_max <= 1: only region E is reachable. Stop, Drop.**

Region A is unreachable: prevalence certificates are skipped.

Undecided share (the 15% stop, section 7), under both readings:

- cells reading: 0 of 10 undecided cells over all cells (not over 15%)
- chart-worlds reading: 0 of 20 undecided chart-worlds over compared chart-worlds (not over 15%) [drives the stop]
- chart-worlds reading, before the reruns: 0 of 20 (not over 15%); after the reruns: 0 of 20 (not over 15%); 0 rerun(s) applied, 0 pending
- drives the stop: chart-worlds (the constant UndecidedStop in analyze.go), taken after the reruns

## ambient-np prevalence

8 counted cell(s) in 2 chart(s): rabbitmq, redis.

## Cells

10 cells: counted 8, excluded 2, unexplained 0, unresolved 0, awaiting run 2 0, not reproduced 0.

| chart | world | key | publisher | run 1 | run 2 | status | credit | type | exclusion |
|---|---|---|---|---|---|---|---|---|---|
| argo-cd | W3 | workload:Deployment/argo-cd-argocd-dex-server:restarts | argoproj | flaky | same | excluded |  |  | flaky |
| podinfo | W1 | helm:test | stefanprodan | finding | finding | excluded |  |  | c-optout |
| rabbitmq | W3 | helm:install | bitnami | finding | finding | counted | ambient-np | defect |  |
| rabbitmq | W3 | port:rabbitmq-headless:15672:http | bitnami | finding | finding | counted | ambient-np | defect |  |
| rabbitmq | W3 | port:rabbitmq:15672:http | bitnami | finding | finding | counted | ambient-np | defect |  |
| rabbitmq | W3 | workload:StatefulSet/rabbitmq:ready | bitnami | finding | finding | counted | ambient-np | defect |  |
| rabbitmq | W3 | workload:StatefulSet/rabbitmq:restarts | bitnami | finding | finding | counted | ambient-np | defect |  |
| redis | W3 | helm:install | bitnami | finding | finding | counted | ambient-np | defect |  |
| redis | W3 | workload:StatefulSet/redis-replicas:ready | bitnami | finding | finding | counted | ambient-np | defect |  |
| redis | W3 | workload:StatefulSet/redis-replicas:restarts | bitnami | finding | finding | counted | ambient-np | defect |  |

## World-universal (rule 5)

| kind | name | world | differing / having | universal |
|---|---|---|---|---|
| cause | ambient-np | W3 | 2 / 10 | false |
| class | helm:test | W1 | 1 / 10 | false |
| class | port | W1 | 0 / 9 | false |
| class | helm:test | W3 | 0 / 10 | false |
| class | port | W3 | 1 / 9 | false |

## C-optout evidence (podMesh)

Mechanised, for an asserted row: the key is helm:test, and in every record of both runs each caller the row names is a pod in podMesh or testPodMesh (the pods `helm test` created) that is not the instrument's client and carries the opt-out metadata of that world. Not mechanised: that the named callers are all the failing callers; the ledger row and its reviewer verdict assert that. The pods below are every pod with effective opt-out, for context only.

- podinfo W1: 12 pod(s) with effective opt-out, ledger asserts it: true
  - podinfo-grpc-test-x1 (Pod/podinfo-grpc-test-x1) annotation sidecar.istio.io/inject=false, in 6 record(s)
  - podinfo-jwt-test-x2 (Pod/podinfo-jwt-test-x2) annotation sidecar.istio.io/inject=false, in 6 record(s)
