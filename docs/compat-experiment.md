# Compat experiment: pre-registration, version 3, amended 2026-11-12

Status: version 3 signed off by the maintainer on 2026-11-11; amended on 2026-11-12 by the maintainer's decisions in "Changes after sign-off, before the freeze"; not frozen yet. Nothing has run. This file, the cause list and the whole instrument are committed and tagged `compat-exp-freeze` before the first qualification job; the result quotes that SHA.

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

## Changes after sign-off, before the freeze

Decided on 2026-11-12, by the maintainer unless an entry says otherwise. Paths are under `hack/compat-exp/` unless they start with `hack/compat-pilot/` or `.github/`; "INTERPRETATIONS n" is entry n of `crossrun/INTERPRETATIONS.md`.

### Decisions on ambiguous points

- P_D leaves out `ambient-np` findings, as N_new, P_new, D and S do; section 1's exclusion sentence omitted it. Direction: away from Continue. (`crossrun/finish.go`)
- Section 1's sentence that S >= 2 keeps the table monotone was false: with N_new = 2, P_new = 2 and S = 2 (region B), one more counted finding that its key's static check does not flag lowers S to 1 and gives region E. The rule stays as written; the sentence is corrected. Direction: none, no rule change.
- The 15% undecided stop of section 7 is measured over chart-worlds: undecided chart-worlds over compared chart-worlds. Direction: compared with counting cells the threshold is harder to cross, so the stop fires less often. crossrun prints both readings; the constant `UndecidedStop` in `crossrun/analyze.go` selects the chart-worlds reading to drive the stop. (INTERPRETATIONS 14, 25)
- A key counts toward D when any counted finding of it is of type defect. Direction: toward Continue; P_D >= 2 still requires defects from two publishers. (`crossrun/finish.go`)
- Rule 3 stays literal: a cell that needs two keys' fixes together and that neither part clears alone goes to the first key in the list, which is `ambient-np` when it is one of the two, so such a cell leaves the new-key counts. Direction: toward Drop. (`crossrun/analyze.go`; list order is causes.json's, new keys last)
- N_max stays literal: every pending cell counts, `ambient-np` candidates included. Direction: delays a futility stop, does not change a final region. (`crossrun/finish.go`)
- A key admitted after the freeze cannot earn S, because its static check would need code written after the freeze. This narrows section 1's former wording "or for a new key at its admission" and is a rule change, not a clarification. Direction: away from region B. (`crossrun/analyze.go`)
- apiserver-caller: section 3's wording stands ("none means the key enters only by the rule below"). Any key flagged `needsNewKeyRule` in causes.json, today only apiserver-caller, counts only with an admitted new-key record in the ledger, ranks among the new keys in rule 3 (so ambient-l7 ranks before it) and never earns S; its static check still runs and, when the key has counted findings, is reported as not counting toward S. A fix entry may name it as `cause` only with that record. Direction: toward Drop, away from region B. (`ledger/ledger.go`, `crossrun/check.go`, `crossrun/finish.go`, `guard.sh`, INTERPRETATIONS 26)
- bind-address, decided by the maintainer's delegate default and subject to the review before the freeze: IST0143 is an Istio documentation page, so the key has an anchor and is a frozen key. Direction: slightly toward Continue, expected to be without effect (see the facts below).
- A cell that differs in one run and is "same" in the other fails rule 1 and can never count, so it is left out of N_max and P_max. Direction: a futility stop can fire sooner; no final region changes. (`crossrun/finish.go`, INTERPRETATIONS 4)
- "After one rerun" is implemented as written: a chart is rerun in a run if and only if that run leaves one of its mesh worlds undecided or its W0 not 3 of 3. The rerun is one separately dispatched observe run of that chart and replaces the chart's records of that run wholesale; it is mandatory and without choice in a final analysis (a missing, unwarranted or second rerun is an error); the same list serves the replacement rule; the undecided share is taken after the reruns. Direction: compared with no rerun of undecided worlds, the stop, and on its second firing Drop, comes less often. (`crossrun/sample.go`, `replacements.json`, INTERPRETATIONS 27)

### Definitions the instrument supplies

- Certification flag: `-cell-worlds` (the text said `-worlds`). The certificate needs the cell's worlds cleared, and the other mesh world may show no difference the base chart lacks; `-worlds W1,W3` stays the compared set. (`hack/compat-pilot/difftable/main.go`, `diff.go`)
- Ignore list: a JSON list of `{chart, world, key, reason}` under `hack/compat-exp/ignore/`, accepted only for cells of the base chart; an entry that matches only a difference of the fix, or no cell, is an error. An ignored cell that is a stable finding of the base must keep its value under the fix; one that is flaky or undecided for the base is listed as not comparable. (`hack/compat-pilot/difftable/diff.go`, `main.go`)
- World not compared: an ignore entry with key `*` names a world in which the base chart has no verdict; the certificate passes that world and lists it under `worldsNotCompared`, and crossrun refuses such a certificate for the cell's own world. (`hack/compat-pilot/difftable/diff.go`, `crossrun/check.go`)
- Ready in qualification: `helm install --wait --wait-for-jobs` returns 0 within the deadline and, after the 30 s settle wait, every workload group is ready or Succeeded by observe.sh's own jq program; Job pods are left to `--wait-for-jobs`. (`qualify.sh`)
- Non-HTTP port, for the gate-bias table: UDP or SCTP; a TCP port whose declared protocol (appProtocol, else a name prefix Istio recognises) is not one of http, http2, https, grpc, grpc-web, h2c, ws, wss; an undeclared TCP port not in 80, 443, 3000, 8000, 8008, 8080, 8081, 8443, 8888, 9090, 9093. (`qualify.sh`)
- Tolerated dry-run errors: "no matches for kind K" when K is defined by a CRD in the same render (rendered with `--include-crds`), noted in the record; every other error fails the step. (`qualify.sh`)
- Gate-bias gap: the larger of the two share ratios; a zero share against a non-zero share exceeds; charts whose render failed are left out of both counts. (`select.sh`)
- "Fewer than 40 of 300 qualify": fewer than 40 qualified among the recorded candidates. A candidate without a record before the 65th qualified one makes the selection incomplete (exit 4), not infeasible. With 40 to 49 qualified the list is all of them, with no reserves. (`select.sh`)
- Install deadline: max(600 s, 3 x the slowest qualification install of any chart in the list or the reserves), written into every entry. (`select.sh`)
- Frame: the top 300 Helm packages by stars, ties in Artifact Hub's order, then repository, then package; deprecated packages are skipped; exclusions are an ordered first-match data file, CRD-only by the `-crds` or `-crd` suffix or the `crds` keyword; "at most 2 per publisher" skips later charts of a publisher without using up places. Publisher: the lower-cased GitHub owner of the first source link on github.com, else the Artifact Hub repository name. `frame fetch`, `frame build` and `frame charts` write the stored API responses, candidates.json and candidates-charts.json. (`frame/build.go`, `frame/exclusions.json`)
- Chart id: the package name, or `<repository>-<name>` when two candidates share a name. (`frame/build.go`, `select.sh`)
- Fix entries: arrays in `hack/compat-exp/fixes/*.json` with `id`, `fixOf`, `cause`, `chart`, `version`, `repo`, `values` or `patch`, `cellWorlds` and `ignore`; chart, version and repo equal the base chart's; `values` holds only `--set`, `--set-string` and `--set-json` pairs. A fix installs under its base chart's release name, so the keys line up. (`guard.sh`, `hack/compat-pilot/render-args.sh`)
- Patch form: `patch -p1 -F0` on the `helm pull --untar` tree, then `helm lint`; at most 40 changed lines, counted hunk by hunk; the patch sha256 is recorded. (`hack/compat-pilot/observe.sh`)
- Docker Hub mirror: mirror.gcr.io first, registry-1.docker.io as fallback, recorded in the world file; qualify.sh refuses a W0 world without it. (`hack/compat-pilot/world.sh`, `qualify.sh`)
- capacityPending: PodScheduled False, reason Unschedulable, a message naming "Insufficient cpu" or "Insufficient memory". (`hack/compat-pilot/observe.sh`)
- E1 and E2: `apiserver:` keys, for each Service port a webhook or APIService of the release names, probed over https through the API server's service proxy and reachable on any HTTP response, plus APIService Available; `late:` keys, from a second workload snapshot at install + `MMC_LATE` (120 s) or after the probes if later. (`hack/compat-pilot/observe.sh`, `RECORD.md`)
- Helm test pods: a second capture after `helm test` records the pods it created in `testPodMesh`, with `testPodMeshStatus`. (`hack/compat-pilot/observe.sh`, `RECORD.md`)
- C-optout, mechanised: only for the key `helm:test`. In both runs and every record of the chart and world, `testPodMeshStatus` is `listed`, there is at least one helm test hook pod, all helm test hook pods carry the world's opt-out (`sidecar.istio.io/inject=false` in W1, `istio.io/dataplane-mode=none` in W3) and none is the instrument's client, and the ledger row's callers are exactly those pods. (`crossrun/check.go`, INTERPRETATIONS 9)
- Reruns and replacement: the rerun is mode observe with `chart_ids` that one chart (all three worlds), started after the run it reruns ended, named in `replacements.json` (`reruns[]` of `{chart, run, rerun}`, and `replacements[]`, which carry their rerun) and given to analyze.sh as `--rerun`. A chart is replaced by the next reserve if and only if its W0 is not 3 of 3 in the run and again in the rerun; otherwise the rerun stands in. Replacement is mandatory, reserves are taken in list order, and a taken reserve is under the same rule. W0 is 3 of 3 when 3 W0 runs are used and `oracleInstall` is ok with 3 of 3 installs. Timing: a run-1 rerun must end before run 2 starts, a run-2 rerun must start at least 12 h after run 1 ended; anything else is an error. With run 1 only, a missing rerun is reported as pending. (`replacements.json`, `crossrun/sample.go`, `analyze.sh`, INTERPRETATIONS 22, 27)
- The first qualify run counts: the qualification run named by the chart list must be the first completed qualify run after the tag commit; an earlier one is void only when `void-runs.json` lists it with a reason and its own conclusion is not success. (`analyze.sh`)
- The 12 h gap: run 1 ends at the latest job completion of any attempt, run 2 starts at the earliest job start of any attempt. A run 2 that starts less than 12 h after run 1 ended is an error: no result, no region, no futility verdict. (`analyze.sh`, `crossrun/analyze.go`)
- Run identity: analyze.sh refuses a run triggered by a push; every run must be a completed `workflow_dispatch` run of `.github/workflows/compat-exp.yml` (the REST run object's `path` and `workflow_id`) on the tag commit or a descendant with frozen files equal to the tag's; its `run-name`, returned as `display_title`, and the guard job's artifact `run-metadata-<run id>-<run attempt>` must agree with the run's role on mode and Istio version; the chart list must reproduce byte for byte from the qualification run's records. Only analyze.sh produces a result of the experiment. (`analyze.sh`, `guard.sh`, `.github/workflows/compat-exp.yml`, INTERPRETATIONS 21, 23)
- Recheck version: the guard accepts any x.y.z in mode recheck; analyze.sh accepts only the second `istio` option of the tag's workflow, the line marked FREEZE EDIT. (`guard.sh`, `analyze.sh`)
- A reviewer verdict is required for every counted row (`agree` or `disagree-type`; `disagree-other` leaves the cell uncounted), every c-optout or world-universal exclusion row (`agree`) and every new key (`admit`; `merge` and `reject` do not admit). Mechanical exclusions need none. (`ledger/ledger.go`)
- Static checks: eight check ids, rules in causes.json (`staticCheckRule`, `staticCheckWorlds`), run on `helm template` output rendered as observe.sh installs, without cluster capabilities, one render for W1 and W3, with the workflow's `HELM_VERSION`. A check that does not apply to a world is written not applicable, and a counted finding there keeps its key out of S. The 10% test is per world, over qualified charts "same" in every analysed run. (`static.sh`, `staticcheck/`, INTERPRETATIONS 12)
- Cells: a key that is a finding, flaky, or undecided with every used world value differing from W0's, in a run; the mechanical reasons apply in rule 5's order. A cell differs in two runs when both reports say `differs`; equal values are not required. (INTERPRETATIONS 1, 3, 5)
- World-universal: the classes are exactly `apiserver` (E1), `late` (E2), the key `helm:test` and `port`; a chart has the class when its world report holds such a key in either run, whatever the verdict. For a cause key the denominator is every qualified chart compared in that world. (INTERPRETATIONS 6, 7, 8)
- Rule 3 with parts: a combined cell's rule 2 certificate is the combined fix entry's; each part runs alone, and when exactly one part returns the cell to W0's value its key gets the cell, otherwise (none or several) the first key in the list does. (`ledger/ledger.go`, `crossrun/analyze.go`, INTERPRETATIONS 10)
- A cell failing rule 2 or rule 4 is `unexplained` or `unresolved` in the ledger, uncounted and pending. "If all counted" gives each open cell its ledger cause, or a distinct new key of type gap when it has none. (INTERPRETATIONS 15, 18)
- What crossrun checks against the reports, and what it takes from the ledger on trust (helm lint, the patch limits, the merged-upstream check, the reviewer verdicts, and the recheck when no recheck report is given): INTERPRETATIONS 16.
- Sample: every chart of the sample must be in both reports with W0, W1 and W3; a missing one is an error, never a smaller denominator. (INTERPRETATIONS 17)
- The analysis is analyze.sh on the maintainer's machine, not a workflow job; the workflow runs `ledger check` before certify and recheck. crossrun's pilot test (work item 10) uses difftable reports of pilot runs 4 and 6 trimmed to few keys, with hand-built podMesh records and run times. (`analyze.sh`, `crossrun/pilot_test.go`)
- Names the instrument fixes: workflow modes acceptance, qualify, observe, certify and recheck; artifacts `compat-exp-report-<run id>`, `exp-<run id>-<chart>-<world>` and `charts-exp-<run id>`; `CHARTS_FILE`, `MMC_SETTLE`, `MMC_LATE`, and `MMC_REGISTRY_MIRROR=1` for every job. (`.github/workflows/compat-exp.yml`, `hack/compat-pilot/observe.sh`)

### Frozen beyond section 5's signed list

Section 5 as signed named the frame tool, qualify.sh, observe.sh, world.sh, difftable, the workflow, causes.json, crossrun and the ledger. guard.sh freezes these as well:

- `select.sh`
- `static.sh`
- `analyze.sh`
- `acceptance-check.sh`
- `acceptance-expected.json`
- `guard.sh`
- `classification-prompt.md`
- `staticcheck/`
- `frame/exclusions.json`, inside `frame/` and listed on its own
- `candidates.json`
- `candidates-charts.json`
- `hack/compat-pilot/render-args.sh`
- the whole `.github/workflows/` directory, not only compat-exp.yml: a workflow file added or changed after the tag, the repository's CI included, fails the freeze
- `docs/compat-experiment.md`
- `go.mod`
- `go.sum`

Data stays outside the freeze and is committed before analyze.sh reads it: `ledger.json`, `replacements.json`, `void-runs.json`, `charts-exp.json` (committed once), `fixes/`, `ignore/`, `patches/`.

### Facts established after sign-off

- valkey, one of the ten pilot charts, never installed in pilot 1: its render fails in every world, W0 included, so its "same" was vacuous. The acceptance run of work item 9 still lists it, and its match with pilot 1 proves nothing; acceptance-check.sh reports a chart whose W0 installs were not all ok as vacuous. The correction to docs/compat-pilot.md is proposed in #49. (`acceptance-charts.json`, `acceptance-expected.json`, `acceptance-check.sh`)
- A blind spot of the observations, found by the static check: argo-cd's NetworkPolicies do not admit port 15008, so its internal pod-to-pod traffic would break in ambient, and the instrument, which does not exercise traffic between a chart's own pods, reported "same". This undercounts findings, toward Drop; section 5's blind-spot table and the scope of a Drop in section 7 now say so. (causes.json, ambient-np `expectedBreadth`)
- apiserver-caller: no Istio documentation anchor was found, as section 3 anticipated; causes.json has `anchor` null with a `noAnchorReason` and `needsNewKeyRule` true.
- bind-address: application-requirements, the page section 3 named, states no bind-address requirement; causes.json anchors the key on reference/config/analysis/ist0143, "LocalhostListener", which adds that since Istio 1.10 a localhost listener fails the same way without Istio (older versions exposed such ports), so the key is unlikely to give a difference from W0 at all. It has no static check (`staticCheck` null, with a `noStaticCheckReason`), so it can never count in S.
- job-lifecycle rests on a blog post, blog/2023/native-sidecars, "Sidecar woes"; no documentation page states the Job requirement.
- uid-1337 applies to the sidecar world only: causes.json gives it `worlds` W1, because ambient's in-pod rules match a packet mark, not a UID.
- startup-order is anchored on ops/common-problems/injection; reserved-port's W3 port list comes from ambient/architecture/traffic-redirection. Section 3's table now gives the anchors of causes.json.
- Version pinning in the frame: the highest version without a pre-release part among the package's available versions; Artifact Hub's prerelease flag is ignored, and a package without such a version is skipped as `no-stable-version`. (`frame/build.go`)
- The acceptance run is started by a push to the branch `compat-exp-acceptance`; the guard refuses a push that is any other mode or on any other branch, and analyze.sh refuses every push-triggered run. Every other mode is a manual dispatch after the instrument is on the default branch. (`.github/workflows/compat-exp.yml`, `guard.sh`, `analyze.sh`)
- An additional adversarial review of the whole instrument takes place before the freeze tag (decided 2026-11-12).

## 1. Question and decision rule

Question: in popular charts nobody picked, how many distinct causes beyond pilot 1's (ambient against a NetworkPolicy, docs/compat-pilot.md "Findings") make a chart that works without a mesh fail under Istio? Metrics, over counted findings:

- N_new: distinct counted cause keys, `ambient-np` excluded.
- P_new: publishers with a counted finding of a new key.
- D: new keys with at least one counted finding of type defect; P_D: publishers with a counted defect finding of a new key.
- S: new keys with a static check over `helm template` output, specified in causes.json at the freeze, that flags every counted finding of its key and at most 10% of charts that were "same" in that world. A key admitted after the freeze has no such check and never counts in S.

The first matching row decides, so every point falls in exactly one.

| region | condition | outcome |
|---|---|---|
| A | N_new >= 3, P_new >= 3, D >= 2, P_D >= 2 | Continue in this repository: ADR 0001 sections 2 to 5 back on the table |
| B | N_new >= 2, P_new >= 2, S >= 2 | Leave: an `istioctl analyze` proposal stream, one check per key with S |
| C | N_new >= 3, P_new >= 3 | Leave: an atlas in its own Git-Hospital repository |
| E | anything else: N_new <= 1, or P_new <= 1, or N_new or P_new = 2 with S <= 1 | Drop: MeshMedic stays an incident tool; findings go out as one-off packets under the queue rules; nothing new is built |

Why. Pilot 1 gives N_new = 0 on 10 enriched charts (its cause is the excluded key; valkey, one of the ten, never installed), so the rate is anywhere from 0 to about 0.3 per chart (rule of three). Guess, Poisson: at 5 new causes per 50, the least supply that feeds a catalog and a queue, P(N_new >= 3) = 0.88 and P(N_new <= 1) = 0.04; at 1 per 50, 0.08 and 0.74. If half are defects (guess), P(D >= 2) is 0.71 and 0.09. N_new >= 4 would cut power at the useful rate to 0.73 before the D term, so the thresholds stay; P_D >= 2 keeps the product from being a queue to one publisher. Two causes from two publishers buy only analyze proposals, and only if both are statically detectable. The table is not monotone in S: at N_new = 2, P_new = 2 and S = 2 (region B), one more counted finding that its key's static check does not flag lowers S to 1 and gives region E. Under the defect definition of section 2, pilot 1's own cause would be a defect, because extraIngress is a generic key. So D measures 'the chart has no mesh-specific values key', not 'an owner would merge a fix', and D is likely to be close to N_new; the guess that half of the new causes are defects is probably low. P_D >= 2 is then the term that does the work.

`ambient-np` charts found again are reported as prevalence, never in N_new, P_new, D, P_D or S.

## 2. Counting

Cell: a (chart, world, observation key) that `difftable` calls a finding (W0 3 of 3 agree, the mesh world differs 3 of 3, pull-failure runs left out). It counts for key k when:

1. it differs in two workflow runs, the second started at least 12 h after the first ended, with W0 3 of 3 in both. A chart that a run leaves with a mesh world undecided or its W0 not 3 of 3 is rerun once, and the rerun's records replace the chart's records of that run; a run-1 rerun ends before run 2 starts, a run-2 rerun starts at least 12 h after run 1 ended;
2. a fix earns `difftable`'s certificate in two workflow runs, plus `helm lint`. A fix is `values`, or a `patch`: `helm pull --untar` of the pinned version plus a diff stored in `hack/compat-exp/patches/`, changing only what k's mechanism names (reviewer check; at most 40 lines). Certification runs with `-cell-worlds` limited to the cell's worlds (the other mesh world may show no difference the base chart lacks) and an ignore list of the rule-5 cells of that chart, because difftable's certificate otherwise fails while any finding remains in any mesh world; this option is built and tested before the freeze (work item 6).
3. it is credited to one key only. A chart and world needing two keys' fixes gets a combined certificate, then each part runs alone; a cell goes to the key whose part alone returns it to W0's value, else to the first key in the list;
4. it reproduces 3 of 3 on the recheck version (section 4), and no fix is merged on the chart's default branch;
5. it is not excluded: pull failure, capacity-Pending, flaky, undecided (after the rerun of rule 1), C-optout (every failing caller carries the chart's own mesh opt-out, as podinfo's test pods), or world-universal: a cause key or an observation class (E1, E2, helm:test, ports) that differs in a world for at least 90% of the qualified charts that have that class: a world property, not a chart property.

Publisher: the GitHub owner in the Artifact Hub `source` link, else the Artifact Hub repository name.

Type. Defect when the chart has no values key specific to k's requirement (documented, with the stated effect of meeting it: a port `appProtocol`, an ambient NetworkPolicy switch), or either side contradicts its own documentation. Generic keys (podAnnotations, podLabels, extraIngress, extraDeploy, extraManifests, service annotations, anything that disables a component or a security feature) do not count. Gap otherwise; gaps count in N_new, not D.

One fresh-context agent per classification, holding only the ledger evidence and this file, with its prompt stored in the freeze commit, checks each classification; its first verdict binds. A disagreement about type makes the cell a gap; any other disagreement leaves it uncounted. Unexplained cells are reported, never counted. At the time box, unclassified cells are reported as unresolved, with the region they would reach if all counted; the decision uses counted findings only.

## 3. Cause keys, fixed before the freeze

`hack/compat-exp/causes.json`: per key, an Istio documentation anchor (page and heading, istio.io commit checked at the freeze) and a mechanism, at the grain of ADR 0001 section 3's seed list.

| key | anchor | mechanism |
|---|---|---|
| ambient-np | ambient/usage/networkpolicy | policy drops HBONE on 15008 or probes from 169.254.7.127 |
| protocol | protocol selection and application-requirements | declared or detected protocol is not what the port carries, server-first included |
| bind-address | reference/config/analysis/ist0143, "LocalhostListener" (application-requirements states no bind-address requirement) | app listens where the proxy does not forward |
| uid-1337 | application-requirements | app runs as the proxy's UID (sidecar world only) |
| reserved-port | application-requirements; ambient/architecture/traffic-redirection for W3 | app uses a port Istio reserves |
| startup-order | ops/common-problems/injection (holdApplicationUntilProxyStarts) | app needs network before its proxy |
| job-lifecycle | blog/2023/native-sidecars, "Sidecar woes" (a blog post; no documentation page states it) | proxy holds a Job open |
| apiserver-caller | none found (sought on 2026-11-11); the key enters only by the rule below | kube-apiserver cannot reach a webhook or APIService backend under STRICT |
| ambient-l7 | ambient/usage/l7-features | chart's L7 config has no waypoint |

server-first and unnamed or misdeclared ports are one key, as ADR 0001 section 3 lists them as one seed entry.

New key: the analyst brings (a) an Istio documentation anchor, or an issue or code reference for an Istio-side defect, (b) a certified fix limited to that mechanism, (c) a chart-independent reproduction run in CI, like `hack/compat-pilot/repro/networkpolicy-15008.sh`. A fresh reviewer who has not seen the tally checks all three and that no existing key covers it; a tie merges. No key is added after the time box. Two keys are one if either key's certified fix clears the other's chart-independent reproduction. New keys rank after the frozen list in rule 3. A key of causes.json flagged `needsNewKeyRule` (apiserver-caller) is a new key in all of this: it counts only with an admitted new-key record, ranks among the new keys and never earns S.

169.254.7.127 is the same key as np-15008. ADR 0001 section 3 lists "NetworkPolicy missing 15008 or the probe source in ambient" as one seed entry; both sit on one Istio page; the broken object, fix location, static check and upstream contribution are the same; and a sibling of pilot 1's class must not supply the "beyond pilot 1" the experiment asks for. Merging can only undercount, toward Drop.

## 4. Sample, worlds, versions

Frame and qualification as in draft 1 (kept): Artifact Hub top 300 by stars, no CNIs, meshes, library or CRD-only charts, at most 2 per publisher; W0 defaults only, `helm template` and a server dry-run succeed, 3 of 3 installs Ready in 15 minutes without a pull error; the first 50 plus 15 reserves frozen in `charts-exp.json` by sha256; a chart whose W0 fails later is replaced by the next reserve in both runs. Install deadline: max(10 min, 3 x the slowest qualification install), the same for all.

Gate bias: it drops charts needing configuration or an external service, and Bitnami defaults whose images are gone (docs/compat-pilot.md "The ten charts"); these lean stateful, the mesh-sensitive kind. Overrides would change the thing under test, so the bias is measured: shares with a StatefulSet, NetworkPolicy, non-HTTP port or headless Service, qualified versus excluded; a gap above 1.5x (guess) is a stated limitation.

Worlds: W0, W1 (sidecar, istio-init), W3 (ambient), STRICT, native sidecars at default; W2 out, it matched W1 in every row (docs/compat-pilot.md results table). Pilot versions otherwise; runs on Istio 1.31.3 so the frozen tool can be checked against pilot 1. Recheck version: the latest Istio release on the freeze date, written into the freeze commit: 1.32.0 as of 2026-11-11, published 2026-11-09 (GitHub API, repos/istio/istio/releases), if it supports Kubernetes 1.36, else the latest 1.31 patch release on the freeze date.

This replaces pilot 2's pre-registered next step, operators driven through their own resources (docs/compat-pilot-2.md "Decision"): it needs a hand-written scenario per operator, so it cannot be one frozen instrument and brings back a hand-picked sample, and it measures depth in three operators while the open question is supply across unpicked charts. It stays a follow-up for region A.

## 5. The instrument, frozen

Frozen by SHA before qualification, the paths guard.sh compares with the tag: in `hack/compat-exp/` the frame tool (`frame/`, with `frame/exclusions.json`), qualify.sh, select.sh, static.sh, analyze.sh, acceptance-check.sh, acceptance-expected.json, guard.sh, causes.json, classification-prompt.md, `staticcheck/`, `ledger/`, `crossrun/`, candidates.json and candidates-charts.json; in `hack/compat-pilot/` observe.sh, render-args.sh, world.sh and `difftable/`; the whole `.github/workflows/` directory; this file; go.mod and go.sum. Fix entries, ignore lists and patches are data added later; a fix entry is refused unless its `fixOf` is in the frozen list. Nothing is built after the freeze; crossrun and the ledger are frozen with the rest and only read records.

observe.sh at sign-off (`origin/main`) installs with `values[]` only (lines 76-77, 121) and records pod phase but not PodScheduled, so capacity-Pending shows only in Warning events, kept as evidence (179-181); it records no pod labels or annotations. world.sh has no mirror. Added before the freeze: `CHARTS_FILE` and the `patch` form (patch sha256 recorded); per pod, mesh opt-out labels and annotations and whether a proxy or ambient redirection is present; `capacityPending` (PodScheduled False, "Insufficient cpu" or "memory"), left out by difftable like pull failures; a docker.io mirror through mirror.gcr.io with fallback in every world's containerd, W0 included, recorded in the world file.

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
| traffic between a chart's own pods (found after sign-off by the static check) | yes: the instrument's client calls the chart's Services, nothing exercises traffic between the chart's own pods; argo-cd's NetworkPolicies do not admit 15008, so its internal traffic would break in ambient, and it was "same" (causes.json, ambient-np `expectedBreadth`) | out: it undercounts findings, toward Drop |

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
- Over 15% of chart-worlds undecided after one rerun, the rerun of rule 1 (the compared chart-worlds of the sample, W1 and W3 per chart; one is undecided when its verdict is `undecided` in any analysed run): fix the instrument once, freeze again, restart from qualification. A second time: Drop.
- Futility, after run 1 and each certification batch: N_max = counted new keys + differing cells neither credited nor excluded (undecided included; a cell that differs in one run and is "same" in the other fails rule 1 and is left out; rule 3 gives each cell at most one key); P_max likewise over publishers. If N_max <= 1 or P_max <= 1, only region E is reachable: stop, Drop. If A is unreachable, prevalence certificates are skipped.
- Time box: decide 14 days after the freeze. No early Continue. The result is published whatever it says.
- Until the region is decided, only the first cell per candidate key and publisher is certified; prevalence certificates come after the decision.

A Drop covers this instrument (defaults-only installs, one namespace, in-mesh client, no traffic between a chart's own pods, these observations) on this sample. It is not a claim that Istio incompatibilities are rare.

## 8. Work list

Before the freeze, one builder session each, with tests:

1. This file and `causes.json`, anchors checked on istio.io, with the static check for each frozen key.
2. `hack/compat-exp/frame` (Go, tests on stored API JSON).
3. observe.sh: `CHARTS_FILE`, `patch` form; tests with stub `helm` and `kubectl`.
4. observe.sh: per-pod mesh fields, `capacityPending`.
5. observe.sh: E1, E2.
6. difftable: drop capacity runs, `-json` output, `-cell-worlds` limited to a cell's worlds and an ignore list of rule-5 cells (for certification).
7. world.sh: mirror.
8. qualify.sh and `compat-exp.yml`: qualify and observe modes, hash and `fixOf` checks, Istio input limited to the two pinned versions.
9. Acceptance run, started by a push to the branch `compat-exp-acceptance`: all ten pilot-1 charts plus the redis fix (redis-np-15008) as values and as a patch; redis W3 differs, both fixes certify, podinfo W1 is C-optout. It passes only if every chart's W1 and W3 verdict matches pilot 1 (docs/compat-pilot.md results table), and any E1 or E2 difference on a chart pilot 1 called "same" is settled under rule 5 before the freeze is tagged. `hack/compat-exp/acceptance-check.sh` checks this on the run's report artifact. valkey never installed in pilot 1, so its match proves nothing; the check reports a chart whose W0 installs were not all ok as vacuous.
10. crossrun (Go: two runs into cells, reruns, exclusions, credit, bounds), tested on pilot-1 records.
11. Ledger with a schema check, tested on pilot-1 records.
12. The links between the tools above: select.sh, guard.sh, static.sh, analyze.sh, acceptance-check.sh and `hack/compat-pilot/render-args.sh`.

Then, after an adversarial review of the whole instrument (decided 2026-11-12), tag the freeze. After run 1 only data is added (fix entries, patches, ledger rows).

## 9. Maintainer sign-off

The first seven rows are the sign-off of version 3 on 2026-11-11. The last row covers the amendments of 2026-11-12.

| choice | default | why |
|---|---|---|
| Thresholds of section 1, with P_D >= 2 and S >= 2 in region B | yes | they reject 1 new cause per 50 and accept 5 per 50 about 70% of the time |
| 169.254.7.127 is `ambient-np` | same cause | the ADR's catalog has it as one entry; splitting lets pilot 1's class count as new |
| E1 and E2 in, other blind spots out | E2 in; E1 in with the world-universal rule, the certificate ignore list and the ten-chart acceptance run; other blind spots out | mechanical, cheap, and they open classes pilot 1 could not see |
| Runs on 1.31.3, recheck on the latest release at the freeze | yes | the tool is checked against pilot 1 on one version, and nothing Istio fixed counts |
| Replace pilot 2's operator step | yes | it cannot be one frozen instrument and brings back a hand-picked sample |
| Docker Hub mirror in every world | yes | registry refusals were the pilots' costliest false finding, and the mirror is identical everywhere |
| D counts 'no mesh-specific values key', not upstream acceptance | accept, stated in section 1 | upstream acceptance cannot be observed inside a 14-day box |
| Amendments of 2026-11-12 ("Changes after sign-off, before the freeze") | decided by the maintainer on 2026-11-12; bind-address's anchor by the maintainer's delegate default, subject to the review before the freeze | the freeze requires them |
