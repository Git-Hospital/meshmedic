# Compat pilot

A finding is an observation that held in every W0 run and differed in every run of another world (3 runs each).

| chart | version | W1 sidecar+init | W2 sidecar+cni | W3 ambient |
|---|---|---|---|---|
| db | 9.0.0 | differs (1) | same | undecided |
| redis | 1.0.0 | same | same | differs (1) |
| redis-fix | 1.0.0 | same | same | same |
| web | 2.3.1 | flaky (1) | same | undecided |

## Certificates

A fix is certified when every world behaves like its W0, 3 of 3 with nothing flaky, and its W0 behaves like the base chart's W0.

| fix | for | clears | verdict |
|---|---|---|---|
| redis-fix | redis | W3 | certified |

## db

### W1: differs (1)

- `port:db:3306:passive`: W0 `bytes`, W1 `silent` (3/3)

### W2: same


### W3: undecided

W3 has 2 of 3 runs.

Environment: W0 kubernetes 1.36.0, istio 1.31.3; W1 kubernetes 1.36.0, istio 1.31.3; W2 kubernetes 1.36.0, istio 1.31.3; W3 kubernetes 1.36.0, istio 1.31.3

## redis

### W1: same


### W2: same


### W3: differs (1)

- `workload:StatefulSet/redis:ready`: W0 `true`, W3 `false` (3/3)

Environment: W0 kubernetes 1.36.0, istio 1.31.3; W1 kubernetes 1.36.0, istio 1.31.3; W2 kubernetes 1.36.0, istio 1.31.3; W3 kubernetes 1.36.0, istio 1.31.3

## redis-fix

### W1: same


### W2: same


### W3: same


Environment: W0 kubernetes 1.36.0, istio 1.31.3; W1 kubernetes 1.36.0, istio 1.31.3; W2 kubernetes 1.36.0, istio 1.31.3; W3 kubernetes 1.36.0, istio 1.31.3

## web

### W1: flaky (1)

- flaky `workload:Deployment/web:ready`: W0 `true`, W1 `false`, `true`, `true` (1/3)

### W2: same


### W3: undecided

- left out: W3 run 1: image pull failed (docker.io/web:1)
- left out: W3 run 2: image pull failed (docker.io/web:1)
- left out: W3 run 3: image pull failed (docker.io/web:1)

W3 has 0 of 3 runs.

Environment: W0 kubernetes 1.36.0, istio 1.31.3; W1 kubernetes 1.36.0, istio 1.31.3; W2 kubernetes 1.36.0, istio 1.31.3; W3 kubernetes 1.36.0, istio 1.31.3
