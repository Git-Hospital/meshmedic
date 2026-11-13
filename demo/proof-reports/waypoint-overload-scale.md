## Incident: Ambient waypoint proxy saturated

Scenario `waypoint-overload-scale` (severity warning) fired for `namespace=demo` `service=payments` `waypoint_rps_per_pod=40` `workload=waypoint`.

The signal has held at **1.39** (threshold > 1 for 120s) since 2026-11-13T02:19:43Z.

### Diagnosis

In ambient mode all L7 traffic for an enrolled namespace flows through its waypoint proxies. When per-replica request rate climbs past what one Envoy instance comfortably handles, latency rises across every service behind it at once, which looks confusingly like an application problem. Scaling the waypoint deployment restores headroom. How many requests one waypoint pod handles depends on its node, its limits and its traffic, so no number in the catalog is right for every cluster: the target states it as the parameter waypoint_rps_per_pod, and the signal is the per-replica rate as a share of it, firing above 1. A target without that parameter does not evaluate this entry. A learned baseline does not work here: an idle namespace learns 0, and scaling the waypoint moves the per-replica normal. Experimental: Gateway-managed waypoints are often scaled by HPA; this entry targets deployments where the replica count lives in the config repo. The target's workload is the waypoint deployment, not an application.

### Evidence

| query | value |
| --- | --- |
| requests-per-waypoint-pod | 55.58 |
| waypoint-cpu{pod="waypoint-58c5bb99db-2xgg6"} | 0.4796 |

### Proposed patch (Deployment)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: waypoint
  namespace: demo
spec:
  replicas: 3
```

### Rollback

Scale back down once namespace request rate returns to baseline, or replace this scenario with an HPA on the waypoint so the mesh handles it without a PR at all. If latency stays high after scaling, the bottleneck is not the waypoint; look at the destination workloads.

This scenario requires human approval; nothing was applied to the cluster.
