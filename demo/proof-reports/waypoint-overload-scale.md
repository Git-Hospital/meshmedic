## Incident: Ambient waypoint proxy saturated

Scenario `waypoint-overload-scale` (severity warning) fired for `namespace=demo` `service=payments` `workload=waypoint`.

The signal has held at **76.11** (threshold > 49.81 (3x the learned baseline) for 120s) since 2026-11-12T19:22:03Z.

### Diagnosis

In ambient mode all L7 traffic for an enrolled namespace flows through its waypoint proxies. When per-replica request rate climbs past what one Envoy instance comfortably handles, latency rises across every service behind it at once, which looks confusingly like an application problem. Scaling the waypoint deployment restores headroom. The threshold is relative: once the baseline has warmed up on healthy traffic, the signal fires at baselineMultiplier times this cluster's own per-replica rate, because how much one waypoint handles depends on its node and its traffic, and no one number is right for every cluster. Until then the static threshold applies, so a cold start never fires on noise. Experimental: Gateway-managed waypoints are often scaled by HPA; this entry targets deployments where the replica count lives in the config repo. The target's workload is the waypoint deployment, not an application.

### Evidence

| query | value |
| --- | --- |
| waypoint-cpu{pod="waypoint-58c5bb99db-qm9hd"} | 0.1537 |

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
