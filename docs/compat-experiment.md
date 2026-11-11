# Compat experiment: pre-registration, version 3

Status: signed off by the maintainer on 2026-11-11, not frozen yet. Nothing has run. This file, the cause list and the whole instrument are committed and tagged `compat-exp-freeze` before the first qualification job; the result quotes that SHA.

## Changes from draft 1

| change | audit finding |
|---|---|
| Patched-chart fixes certifiable; defect = no knob specific to the requirement | 1 |
| Whole instrument frozen before qualification | 2 |
| Fixed cause-key list, new-key rule, 169.254.7.127 decided | 3 |
| Futility over cells | 4 |
| Exhaustive regions; Leave needs P_new >= 2, analyze a tested check | 5 |
| Recheck version pinned; unresolved reported | 6 |
| Replaces pilot 2's next step | 7 |
| Two extensions; Drop scoped to this instrument | 8 |
| Continue needs defects from 2 publishers | thresholds |

## Changes from version 2

- Region B needs a fixed S >= 2 instead of a share of N_new; region E and the "Why" paragraph follow.
- World-universal now covers cause keys and observation classes at 90%; E1 sentence on the apiserver caller added.
- Rule 2: certification runs with `-worlds` and a rule-5 ignore list; work item 6 builds it.
- Two protocol cause keys merged into `protocol`.
- New-key rule: two keys are one if either fix clears the other's reproduction; new keys rank last.
- S checks specified in causes.json at the freeze; region B's static checks move into work item 1.
- Reviewer: one agent per classification, prompt in the freeze commit, first verdict binds.
- Recheck version falls back to the latest 1.31 patch release.
- Acceptance run covers all ten pilot-1 charts and the redis fix; cost table updated.
- Crossrun and the ledger move before the freeze; nothing is built after it; certification order in Stops.
- "(guess)" removed from the three thresholds that are rules.
- Section 1 states that D measures "no mesh-specific values key".
- Sign-off table updated, with a seventh row.

## 1. Question and decision rule

Question: in popular charts nobody picked, how many distinct causes beyond pilot 1's (ambient against a NetworkPolicy, docs/compat-pilot.md "Findings") make a chart that works without a mesh fail under Istio? Metrics, over counted findings:

- N_new: distinct counted cause keys, `ambient-np` excluded.
- P_new: publishers with a counted finding of a new key.
- D: new keys of type defect; P_D: publishers with a counted defect finding.
- S: new keys with a static check over `helm template` output, specified in causes.json at the freeze, or for a new key at its admission from its reproduction only, before it is scored on the sample, that flags every counted finding of its key and at most 10% of charts that were "same" in that world.

The first matching row decides, so every point falls in exactly one.

| region | condition | outcome |
|---|---|---|
| A | N_new >= 3, P_new >= 3, D >= 2, P_D >= 2 | Continue in this repository: ADR 0001 sections 2 to 5 back on the table |
| B | N_new >= 2, P_new >= 2, S >= 2 | Leave: an `istioctl analyze` proposal stream, one check per key with S |
| C | N_new >= 3, P_new >= 3 | Leave: an atlas in its own Git-Hospital repository |
| E | anything else: N_new <= 1, or P_new <= 1, or N_new or P_new = 2 with S <= 1 | Drop: MeshMedic stays an incident tool; findings go out as one-off packets under the queue rules; nothing new is built |

Why. Pilot 1 gives N_new = 0 on 10 enriched charts (its cause is the excluded key), so the rate is anywhere from 0 to about 0.3 per chart (rule of three). Guess, Poisson: at 5 new causes per 50, the least supply that feeds a catalog and a queue, P(N_new >= 3) = 0.88 and P(N_new <= 1) = 0.04; at 1 per 50, 0.08 and 0.74. If half are defects (guess), P(D >= 2) is 0.71 and 0.09. N_new >= 4 would cut power at the useful rate to 0.73 before the D term, so the thresholds stay; P_D >= 2 keeps the product from being a queue to one publisher. Two causes from two publishers buy only analyze proposals, and only if both are statically detectable; S >= 2 keeps the table monotone, so more evidence never gives a weaker outcome. Under the defect definition of section 2, pilot 1's own cause would be a defect, because extraIngress is a generic key. So D measures 'the chart has no mesh-specific values key', not 'an owner would merge a fix', and D is likely to be close to N_new; the guess that half of the new causes are defects is probably low. P_D >= 2 is then the term that does the work.

`ambient-np` charts found again are reported as prevalence, never in N_new, P_new, D or S.

## 2. Counting

Cell: a (chart, world, observation key) that `difftable` calls a finding (W0 3 of 3 agree, the mesh world differs 3 of 3, pull-failure runs left out). It counts for key k when:

1. it differs in two workflow runs, the second started at least 12 h after the first ended, with W0 3 of 3 in both;
2. a fix earns `difftable`'s certificate in two workflow runs, plus `helm lint`. A fix is `values`, or a `patch`: `helm pull --untar` of the pinned version plus a diff stored in `hack/compat-exp/patches/`, changing only what k's mechanism names (reviewer check; at most 40 lines). Certification runs with -worlds limited to the cell's worlds and an ignore list of the rule-5 cells of that chart, because difftable's certificate otherwise fails while any finding remains in any mesh world; this option is built and tested before the freeze (work item 6).
3. it is credited to one key only. A chart and world needing two keys' fixes gets a combined certificate, then each part runs alone; a cell goes to the key whose part alone returns it to W0's value, else to the first key in the list;
4. it reproduces 3 of 3 on the recheck version (section 4), and no fix is merged on the chart's default branch;
5. it is not excluded: pull failure, capacity-Pending, flaky, undecided, C-optout (every failing caller carries the chart's own mesh opt-out, as podinfo's test pods), or world-universal: a cause key or an observation class (E1, E2, helm:test, ports) that differs in a world for at least 90% of the qualified charts that have that class: a world property, not a chart property.

Publisher: the GitHub owner in the Artifact Hub `source` link, else the Artifact Hub repository name.

Type. Defect when the chart has no values key specific to k's requirement (documented, with the stated effect of meeting it: a port `appProtocol`, an ambient NetworkPolicy switch), or either side contradicts its own documentation. Generic keys (podAnnotations, podLabels, extraIngress, extraDeploy, extraManifests, service annotations, anything that disables a component or a security feature) do not count. Gap otherwise; gaps count in N_new, not D.

One fresh-context agent per classification, holding only the ledger evidence and this file, with its prompt stored in the freeze commit, checks each classification; its first verdict binds. A disagreement about type makes the cell a gap; any other disagreement leaves it uncounted. Unexplained cells are reported, never counted. At the time box, unclassified cells are reported as unresolved, with the region they would reach if all counted; the decision uses counted findings only.

## 3. Cause keys, fixed before the freeze

`hack/compat-exp/causes.json`: per key, an Istio documentation anchor (page and heading, istio.io commit checked at the freeze) and a mechanism, at the grain of ADR 0001 section 3's seed list.

| key | anchor | mechanism |
|---|---|---|
| ambient-np | ambient/usage/networkpolicy | policy drops HBONE on 15008 or probes from 169.254.7.127 |
| protocol | protocol selection and application-requirements | declared or detected protocol is not what the port carries, server-first included |
| bind-address | application-requirements | app listens where the proxy does not forward |
| uid-1337 | application-requirements | app runs as the proxy's UID |
| reserved-port | application-requirements | app uses a port Istio reserves |
| startup-order | holdApplicationUntilProxyStarts | app needs network before its proxy |
| job-lifecycle | native sidecars | proxy holds a Job open |
| apiserver-caller | sought at the freeze; none means the key enters only by the rule below | kube-apiserver cannot reach a webhook or APIService backend under STRICT |
| ambient-l7 | ambient/usage/l7-features | chart's L7 config has no waypoint |

server-first and unnamed or misdeclared ports are one key, as ADR 0001 section 3 lists them as one seed entry.

New key: the analyst brings (a) an Istio documentation anchor, or an issue or code reference for an Istio-side defect, (b) a certified fix limited to that mechanism, (c) a chart-independent reproduction run in CI, like `hack/compat-pilot/repro/networkpolicy-15008.sh`. A fresh reviewer who has not seen the tally checks all three and that no existing key covers it; a tie merges. No key is added after the time box. Two keys are one if either key's certified fix clears the other's chart-independent reproduction. New keys rank after the frozen list in rule 3.

169.254.7.127 is the same key as np-15008. ADR 0001 section 3 lists "NetworkPolicy missing 15008 or the probe source in ambient" as one seed entry; both sit on one Istio page; the broken object, fix location, static check and upstream contribution are the same; and a sibling of pilot 1's class must not supply the "beyond pilot 1" the experiment asks for. Merging can only undercount, toward Drop.

## 4. Sample, worlds, versions

Frame and qualification as in draft 1 (kept): Artifact Hub top 300 by stars, no CNIs, meshes, library or CRD-only charts, at most 2 per publisher; W0 defaults only, `helm template` and a server dry-run succeed, 3 of 3 installs Ready in 15 minutes without a pull error; the first 50 plus 15 reserves frozen in `charts-exp.json` by sha256; a chart whose W0 fails later is replaced by the next reserve in both runs. Install deadline: max(10 min, 3 x the slowest qualification install), the same for all.

Gate bias: it drops charts needing configuration or an external service, and Bitnami defaults whose images are gone (docs/compat-pilot.md "The ten charts"); these lean stateful, the mesh-sensitive kind. Overrides would change the thing under test, so the bias is measured: shares with a StatefulSet, NetworkPolicy, non-HTTP port or headless Service, qualified versus excluded; a gap above 1.5x (guess) is a stated limitation.

Worlds: W0, W1 (sidecar, istio-init), W3 (ambient), STRICT, native sidecars at default; W2 out, it matched W1 in every row (docs/compat-pilot.md results table). Pilot versions otherwise; runs on Istio 1.31.3 so the frozen tool can be checked against pilot 1. Recheck version: the latest Istio release on the freeze date, written into the freeze commit: 1.32.0 as of 2026-11-11, published 2026-11-09 (GitHub API, repos/istio/istio/releases), if it supports Kubernetes 1.36, else the latest 1.31 patch release on the freeze date.

This replaces pilot 2's pre-registered next step, operators driven through their own resources (docs/compat-pilot-2.md "Decision"): it needs a hand-written scenario per operator, so it cannot be one frozen instrument and brings back a hand-picked sample, and it measures depth in three operators while the open question is supply across unpicked charts. It stays a follow-up for region A.

## 5. The instrument, frozen

Frozen by SHA before qualification: frame tool, qualify.sh, observe.sh, world.sh, difftable, workflow, causes.json. Fix entries and patches are data added later, refused unless `fixOf` is in the frozen list. Nothing is built after the freeze; crossrun and the ledger are frozen with the rest and only read records.

observe.sh today (`origin/main`) installs with `values[]` only (lines 76-77, 121) and records pod phase but not PodScheduled, so capacity-Pending shows only in Warning events, kept as evidence (179-181); it records no pod labels or annotations. world.sh has no mirror. Added before the freeze: `CHARTS_FILE` and the `patch` form (patch sha256 recorded); per pod, mesh opt-out labels and annotations and whether a proxy or ambient redirection is present; `capacityPending` (PodScheduled False, "Insufficient cpu" or "memory"), left out by difftable like pull failures; a docker.io mirror through mirror.gcr.io with fallback in every world's containerd, W0 included, recorded in the world file.

Blind spots, checked against observe.sh:

| blind spot | holds? | decision |
|---|---|---|
| webhooks never called | partly: later hook Jobs and test pods can hit core-kind webhooks; custom-resource webhooks never | E1; calling them needs chart-specific objects |
| callers outside the mesh | yes, except kubelet probes, opted-out test pods | E1 for kube-apiserver; an outside client fails every chart under STRICT |
| cross-namespace traffic | yes | out (guess: same as one namespace unless the chart ships Istio policy) |
| client-first TCP via consequences | yes | out: needs chart-specific probes |
| only the status of GET / | yes, final status after redirects (line 246) | out |
| nothing after the 30 s settle | partly: tests and probes run later, workloads seen once (128-164) | E2 |
| no Pod Security labels (85-90) | yes | out: istio-init adds NET_ADMIN to every W1 pod, a documented world property |

E1: a GET through the API server's service proxy to each Service port a release webhook or APIService points to (reachable or not), plus APIService Available; about 10 s per run (guess). kube-apiserver is a caller outside the mesh, so E1 may differ for every in-mesh webhook under STRICT; if it does, rule 5 excludes it as world-universal and the key apiserver-caller counts nothing. E2: a second workload snapshot at install + 120 s or after the probes, as new keys; catches late liveness failures; 0 to 90 s per run (guess).

## 6. Cost

Measured (GitHub API, runs/40119399052/jobs): 48 observe jobs, 30711 runner-seconds, mean 639 s; W0 483, W1 605, W3 799 s (run 40109270484: 511, 640, 854 s); 67 minutes wall.

| phase | jobs | runner hours | wall hours |
|---|---|---|---|
| acceptance run on pilot charts | 36 | 7 | 1 |
| qualification, about 100 candidates (guess) | 100 | 14 | 1 |
| 2 runs x 50 x 3 worlds, plus 10% reserves (guess) | 330 | 77 to 99 (pilot means + 180 s extensions, up to x1.3) | 3 to 5 per run |
| fixes, part fixes, prevalence, recheck, reruns (guess: 140 jobs at 900 s) | 140 | 35 | 3 to 4 |
| total | about 600 | about 135 to 155 | 12 to 16, over at least 4 days |

Limits (docs.github.com/en/actions/reference/limits, read in draft 1): 256 matrix jobs per run, 6 h per job, 20 concurrent on Free; a run is 150 observe jobs. Assumption: the organisation is on Free and shares slots, so `max-parallel: 15`. `timeout-minutes: 200` covers three installs at the 45 min worst-case deadline.

## 7. Stops

- Fewer than 40 of 300 qualify: stop as infeasible; the ADR stays narrowed.
- Over 15% of cells undecided after one rerun: fix the instrument once, freeze again, restart from qualification. A second time: Drop.
- Futility, after run 1 and each certification batch: N_max = counted new keys + differing cells neither credited nor excluded (undecided included; rule 3 gives each cell at most one key); P_max likewise over publishers. If N_max <= 1 or P_max <= 1, only region E is reachable: stop, Drop. If A is unreachable, prevalence certificates are skipped.
- Time box: decide 14 days after the freeze. No early Continue. The result is published whatever it says.
- Until the region is decided, only the first cell per candidate key and publisher is certified; prevalence certificates come after the decision.

A Drop covers this instrument (defaults-only installs, one namespace, in-mesh client, these observations) on this sample. It is not a claim that Istio incompatibilities are rare.

## 8. Work list

Before the freeze, one builder session each, with tests:

1. This file and `causes.json`, anchors checked on istio.io, with the static check for each frozen key.
2. `hack/compat-exp/frame` (Go, tests on stored API JSON).
3. observe.sh: `CHARTS_FILE`, `patch` form; tests with stub `helm` and `kubectl`.
4. observe.sh: per-pod mesh fields, `capacityPending`.
5. observe.sh: E1, E2.
6. difftable: drop capacity runs, `-json` output, `-worlds` limited to a cell's worlds and an ignore list of rule-5 cells (for certification).
7. world.sh: mirror.
8. qualify.sh and `compat-exp.yml`: qualify and observe modes, hash and `fixOf` checks, Istio input limited to the two pinned versions.
9. Acceptance run: all ten pilot-1 charts plus the redis fix (redis-np-15008) as values and as a patch; redis W3 differs, both fixes certify, podinfo W1 is C-optout. It passes only if every chart's W1 and W3 verdict matches pilot 1 (docs/compat-pilot.md results table), and any E1 or E2 difference on a chart pilot 1 called "same" is settled under rule 5 before the freeze is tagged.
10. crossrun (Go: two runs into cells, exclusions, credit, bounds), tested on pilot-1 records.
11. Ledger with a schema check, tested on pilot-1 records.

Then tag the freeze. After run 1 only data is added (fix entries, patches, ledger rows).

## 9. Maintainer sign-off

| choice | default | why |
|---|---|---|
| Thresholds of section 1, with P_D >= 2 and S >= 2 in region B | yes | they reject 1 new cause per 50 and accept 5 per 50 about 70% of the time |
| 169.254.7.127 is `ambient-np` | same cause | the ADR's catalog has it as one entry; splitting lets pilot 1's class count as new |
| E1 and E2 in, other blind spots out | E2 in; E1 in with the world-universal rule, the certificate ignore list and the ten-chart acceptance run; other blind spots out | mechanical, cheap, and they open classes pilot 1 could not see |
| Runs on 1.31.3, recheck on the latest release at the freeze | yes | the tool is checked against pilot 1 on one version, and nothing Istio fixed counts |
| Replace pilot 2's operator step | yes | it cannot be one frozen instrument and brings back a hand-picked sample |
| Docker Hub mirror in every world | yes | registry refusals were the pilots' costliest false finding, and the mirror is identical everywhere |
| D counts 'no mesh-specific values key', not upstream acceptance | accept, stated in section 1 | upstream acceptance cannot be observed inside a 14-day box |
