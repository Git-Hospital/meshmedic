# Interpretations

Where docs/compat-experiment.md is ambiguous or cannot be computed as worded,
crossrun and the ledger implement the most literal reading and list it here.
Each entry: the sentence, the ambiguity, the reading implemented, the
alternative. None of these is a decision about the experiment; each is a place
where the maintainer may want the other reading before the freeze.

## 1. What a cell is

- Sentence: section 2, "Cell: a (chart, world, observation key) that difftable calls a finding"; rule 5 then excludes "pull failure, capacity-Pending, flaky, undecided".
- Ambiguity: by definition none of the four is a finding, so rule 5 would exclude nothing, and section 7 counts "undecided" cells.
- Reading: a cell is a key that is a finding in a run, or flaky in a run, or undecided in a run with every used world value differing from the oracle value. An undecided key whose oracle value is unknown is not a cell; its world is listed under `undecidedWorlds`.
- Alternative: findings only; then the four mechanical exclusions and the undecided share are always 0.

## 2. The 12 h gap and run metadata

- Sentence: rule 1, "the second started at least 12 h after the first ended".
- Ambiguity: the reports carry no times, and what happens when the gap is short is not said.
- Reading: the manifest gives `started` and `ended` per run (RFC 3339). With run 2 given, a gap under 12 h (exactly 12 h passes; a run 2 that starts before run 1 ended is a gap under 12 h) is an error: crossrun exits 1 naming both times and writes no result, so no region and no futility verdict. An operator slip must not read as "Drop" (every cell not reproduced, N_max 0, region E).
- Which times: analyze.sh takes them conservatively against workflow reruns. End of run 1 is the latest `completedAt` of any job in any attempt (`gh run view ID --attempt N --json jobs` for every attempt 1..`attempt`, N from `gh run view ID --json attempt`). Start of run 2 is the earliest `startedAt` of any job in any attempt, and not later than the run's own `startedAt`. Jobs with no real time (skipped jobs report a zero time) are ignored. A run that is not completed is refused.
- Run 1 only (analyze.sh without --run2, the futility check "after run 1"): cells that differ in run 1 are `awaiting-run-2` and count as pending toward N_max and P_max. No region is final: the result has `final: false`, the region is "none", the markdown starts with "RUN 1 ONLY" and prints no region or first-match table. The stop flag `stopDrop` is then only a statement about the bounds.
- Alternative: warn and report region E (the first implementation).

## 3. "Differs in two workflow runs", "W0 3 of 3 in both"

- Reading: a cell holds when difftable's verdict for the key is `differs` in both reports. crossrun also re-checks 3 of 3 from each report (runsRequired >= 3, three agreeing oracle runs, an oracle value, every world run differing) and stops if a report claims `differs` without it. The two runs need not show the same world values.
- Alternative: require equal world values in both runs.

## 4. Differs in one run only

- Reading: with both reports given, a cell that is a finding in one run and `same` in the other is `not-reproduced`: not counted, not excluded, not in N_max. A cell finding in one run and flaky, undecided, pull-failure or capacity in the other is excluded for that reason.
- Alternative: keep it open as neither credited nor excluded, so it raises N_max.

## 5. Mechanical reasons and their precedence

- Sentence: rule 5, "pull failure, capacity-Pending, flaky, undecided".
- Reading: pull-failure or capacity when the world (or its oracle) of either run left out a run for that reason (pull-failure wins, as in difftable); flaky when the key is flaky in either run; undecided otherwise, and when a chart or world is absent from a run. A cell can have several reasons; the primary one is the first in rule 5's order (pull-failure, capacity, flaky, undecided, c-optout, world-universal).
- Alternative: classify per key only; or require the reason in both runs.

## 6. World-universal: which classes

- Sentence: "an observation class (E1, E2, helm:test, ports)".
- Reading: exactly four class ids: `apiserver` (E1), `late` (E2), the key `helm:test` (class `helm` is split so helm:install is not it), and `port`. workload and job classes are not tested.
- Alternative: every class in RECORD.md, which would excuse any class that fails in 90% of charts.

## 7. World-universal: denominator and numerator

- Sentence: "differs in a world for at least 90% of the qualified charts that have that class".
- Reading: qualified charts are the frame charts with `qualified` not false that appear in every given report (fix entries are not charts). "Has the class": the chart's world report holds a key of that class in either run, whatever its verdict (undecided included). "Differs": the chart has a cell of that class holding in both runs (run 1 only when run 2 is not given). Universal when numerator * 100 >= 90 * denominator. Every (class, world) with a denominator is printed.
- Alternative: leave undecided charts out of the denominator; or compute per run.

## 8. World-universal for a cause key

- Sentence: "a cause key ... that differs in a world for at least 90% of the qualified charts that have that class".
- Ambiguity: a cause key has no class, so "charts that have that class" has no meaning. Using the charts its static check flags makes a precise check 100% and excludes every good key.
- Reading: denominator is every qualified chart compared in that world; numerator is the charts with a held cell credited (rule 3) to the key by the ledger. So a key breaking 90% of all charts in a world is a world property. No static file is needed.
- Alternative: denominator is the charts the key's mechanism applies to, which records cannot say.

## 9. C-optout: how far it is mechanised

- Sentence: "every failing caller carries the chart's own mesh opt-out".
- Reading: the ledger row asserts c-optout and names the failing callers in `exclusion.callers`: a list of pod names or pod-name prefixes as recorded in `testPodMesh` (a pod matches an entry when its name equals it or starts with it and a dash, so `podinfo-grpc-test` matches `podinfo-grpc-test-x1`; generated suffixes differ between installs). crossrun accepts the exclusion only when all of these hold:
  1. the key is `helm:test`. The helm test is the one observation whose caller is a pod of the chart; ports are called by the instrument's client, workloads and jobs are not calls, `apiserver:` keys are called by kube-apiserver, `late:` keys are workload state. For any other key the assertion is an error.
  2. in both runs, in every record of that chart and world: `testPodMeshStatus` is `listed` (any other status means the test pods were not captured and the callers are not known); the callers are ALL the pods of `testPodMesh` with `testHook: true` (the pods `helm test` ran, by their `helm.sh/hook` annotation), computed by crossrun and not chosen by the analyst, and there is at least one; each is not the instrument's own client pod and carries the opt-out metadata that applies to the world (`sidecar.istio.io/inject=false` in a sidecar world, `istio.io/dataplane-mode=none` in W3, the only ambient world, a constant in the code); and the row's `callers` equals that set: every test hook pod is matched by an entry and every entry matches a test hook pod. A test pod in the mesh (no opt-out) therefore refuses the exclusion even when the row names only the opted-out one (the second audit's app-test-connection / app-test-grpc case); the pods of `testPodMesh` that are not test hook pods, and all of `podMesh`, are not callers.
  3. the ledger row has a reviewer verdict `agree` (the ledger check enforces it; world-universal rows too). Mechanical exclusions (pull-failure, capacity, flaky, undecided, world-universal without a row) need no row and no reviewer.
  A pod that merely carries opt-out metadata but is not a helm test hook pod proves nothing: the audit's Prometheus port finding was excluded because an unrelated admission Job pod was opted out; that case is now an error.
- Helm test pods: `observe.sh` takes `podMesh` before it runs `helm test`, so the pods that `helm test` creates (the callers of this rule) are not in it. A second capture after `helm test` records them in `testPodMesh` (hack/compat-exp/RECORD.md), with their Helm hook annotations, and `testPodMeshStatus` says whether it could. If the chart's hook delete policy removed the test pod before the capture, the status is `no-new-pods`, the pod is in neither field, and a c-optout assertion is an error that quotes the status; the rule is not loosened.
- Alternative: any non-client pod of the chart with the opt-out (the first implementation).

## 10. Rule 3 credit

- Sentence: "a cell goes to the key whose part alone returns it to W0's value, else to the first key in the list".
- Reading: a cell that needs two keys' fixes is modelled in the ledger row as `combined` (the combined fix entry and its certification runs), `parts` (per key: `cause`, the part's own fix `entry`, the `runs` that hold it alone, and the row's statement `clearsAlone`) and `fix.entry` equal to the combined entry. The certificate that satisfies rule 2 for such a cell is the combined entry's (`combined.certification`, which must equal `runs.certification`); the credited key's own part is not asked for a certificate, because a part that leaves the cell differing cannot earn one (difftable's certificate needs the cell cleared). Credit follows rule 3 from the parts' real results: a part returns the cell when its entry's key verdict is `same` in every listed part run; exactly one returning part gets the cell; otherwise (none or several) the part whose cause ranks first in causes.json (new keys after, in ledger `newKeys` order). `clearsAlone` must equal the computed result, and a row whose `cause` differs from the computed credit stops crossrun. A row without parts is credited to its `cause` and its own `fix.entry` must hold the certificate. Defined here because the document does not say which certificate a combined cell uses: beyond its wording.
- Alternative: crossrun computes the combination itself, which needs part-run records the ledger does not name.

## 11. N_new, P_new, D, P_D

- Sentence: "N_new: distinct counted cause keys, ambient-np excluded"; "D: new keys of type defect".
- Reading: "new" means any key other than ambient-np (all other frozen keys are new beyond pilot 1). Type belongs to a row; a key counts for D, and its publishers for P_D, when at least one counted finding of it is a defect. A reviewer verdict `disagree-type` turns the row into a gap. Publisher comes from the frame and must equal the ledger row's.
- Alternative: a key is a defect only if all its findings are.

## 12. S

- Sentence: "flags every counted finding of its key and at most 10% of charts that were 'same' in that world".
- Reading: the check id is the `staticCheck` of the key's causes.json entry. A key whose `staticCheck` is null has no check and is never in S. A key outside causes.json (a new key admitted after the freeze) has no static check either: the frozen static.sh cannot render a check that did not exist at the freeze, so the key never earns S, no static row is required for it, and the S table says "no static check: admitted after the freeze". This narrows section 1's wording for S ("or for a new key at its admission from its reproduction only"): the reproduction-only route is not implemented, so a new key can only lower the chance of region B (it counts in N_new and P_new). For each world holding counted findings of the key: every counted cell is flagged, and flagged * 100 <= 10 * same, where same is the qualified charts whose world verdict is `same` in every given run. A missing static result for a counted cell or a same chart stops with an error. Without a static file S is 0 and a warning says so. The 10% test is per world, not pooled; zero same charts passes.
- Not applicable: a check "applies" to a world when static.sh says so and, for a frozen key, the world is in the key's `worlds`. A counted finding in a world the check does not apply to is reported as `notApplicableFindings`, not as unflagged, and the key does not qualify (a check that cannot look at a world cannot flag a finding there; "flags every counted finding" is not met). That world has no 10% test (`notApplicable`), so no chart is counted as flagged or not flagged there. In practice a key's counted findings lie in its own worlds, so this fires only when a ledger credits a cell to a key outside its worlds.
- Alternative: pool the worlds; treat missing results as not flagged; treat a finding outside the check's worlds as out of the key's scope instead of a miss.

## 13. N_max and P_max

- Sentence: "counted new keys + differing cells neither credited nor excluded (undecided included; rule 3 gives each cell at most one key)".
- Reading: pending cells are those held (or, with run 1 only, found) that are unresolved, unexplained, without a ledger row, or awaiting run 2, plus cells whose primary reason is undecided. Pull-failure, capacity and flaky cells are excluded and not pending. Each pending cell adds one to N_max even if several would share a key, and even when its ledger cause is ambient-np but it is not counted. P_max adds the distinct publishers of pending cells not already among P_new. Reachable regions are found by enumerating final metrics inside the bounds: D_max = D + pending, P_D max = P_D + pending publishers not yet defect publishers, S up to N_max, S allowed to fall, D <= N, P_D <= P.
- Alternative: count undecided only through the 15% stop; count ambient-np pending cells as 0.

## 14. The undecided share

- Sentence: "Over 15% of cells undecided after one rerun".
- Ambiguity: "cells" can mean the cells of the result, or the chart-worlds the instrument compares.
- Reading: both are computed and printed, labelled, the one that drives the stop marked. Cells reading: undecided cells (primary reason undecided) over all cells listed in the result (including excluded and not-reproduced ones). Chart-worlds reading: a chart-world is a (chart, mesh world) pair of the sample, W1 and W3 per chart, after replacements (a replaced chart is out, its reserve is in); it is undecided when difftable's verdict for that world is `undecided` (too few usable runs in the world or in W0) in at least one of the runs analysed, and it counts once however many runs left it undecided. The denominator is every such pair of the sample; sample.go already errors when a sample chart lacks W1 or W3 in any given report, so no pair is skipped for being absent. Over means strictly more than 15% (`undecided * 100 > 15 * compared`); 3 of 20 is not over.
- Decision (2026-11-12, the maintainer): the stop is driven by the chart-worlds reading, set by the named constant `UndecidedStop` in analyze.go (`drivesTheStop` in the output). Direction: the chart-worlds denominator (two worlds per chart) is usually larger than the number of cells and the numerator is one per world however many keys it holds, so compared with the cells reading the threshold is harder to cross and the stop fires less often. The cells reading is still printed. Before this the constant selected the cells reading (the first implementation).
- "After one rerun": crossrun's stop is computed once, over the reports it is given, with the only reruns the code knows applied: the W0-failure reruns of entry 22 (a chart whose W0 was not 3 of 3 gets its rerun's report as stand-in, or is replaced, before any share is computed). There is no rerun of undecided worlds or cells in crossrun: it does not take a second look at a chart-world that was undecided, and no input says "this world was rerun for being undecided". Whether the experiment's "after one rerun" means that, or a separate rerun of undecided worlds, is a procedure outside crossrun; the share is that of the run reports given, so a rerun of undecided worlds would have to arrive as replaced report files. Nothing more is implemented.
- Alternative: count pull-failure and capacity cells as undecided too, or use findings only as the denominator.

## 15. "The region they would reach if all counted"

- Reading: counted cells plus every unresolved, unexplained or awaiting cell, each with the ledger's cause and type when the row has them; a cell with no cause is its own distinct new key of type gap, not flagged by any static check. Excluded and not-reproduced cells stay out. So it is a hypothetical, not a bound; the bound is the reachable list of section 7.
- Alternative: assume the best case (every open cell a distinct defect key with a flagged check).

## 16. What crossrun cross-checks and what it takes from the ledger

- Reading: against the reports it checks the run ids, that the cell is held in both runs, that each certification run id is in the manifest, the fix entry's `fixOf` equals the cell's chart, its certificate is earned, lists the cell's world in `cellWorlds` and `cleared`, does not ignore the cell and does not pass the world uncompared, in each of two distinct runs; the recheck report (when given) shows the cell differing; the mechanical exclusion reasons. It takes on trust: helm lint, the patch sha256 and its 40-line limit, the merged-upstream check, the reviewer verdicts, the recheck when no recheck report is given (a warning counts those rows), and the type basis. "A fix whose fixOf is unknown" is read as: the fix entry is not in the certification report, or its `fixOf` is not the cell's chart.
- Alternative: fail rows whose unverifiable fields are unchecked.

## 17. Frame input, the sample and replacements

- Sentence: section 4, "the first 50 plus 15 reserves frozen in `charts-exp.json` by sha256; a chart whose W0 fails later is replaced by the next reserve in both runs".
- Reading: the frame is the frozen chart list select.sh writes (charts-exp.json): `charts` (primary), then `reserves`, each with `id`, `publisher` and `qualified`. Publisher is the frame's, per rule "Publisher". The sample is the primary charts with the replacements applied. Every chart of the sample must be in both reports (run 1 only: in that report), with W0 records and with W1 and W3; anything missing is an error naming run, chart and world, never a smaller denominator. A report chart that is neither in the sample nor replaced is an error too (it would let the sample be chosen after the fact). Whether a chart whose W0 was not 3 of 3 is replaced is not a choice: see 22.
- Replacements are a data file (`hack/compat-exp/replacements.json`, manifest field `replacements`; schema 1, `replacements[]` of `chart`, `reserve`, `reason`, `run`, `rerun`, and an optional `reruns[]` of `chart`, `run`, `rerun`; entry 22 defines when a chart is replaced). The replaced chart's cells are dropped in both runs and the reserve must be complete in both runs. The file is data, committed like the ledger; analyze.sh refuses it uncommitted.
- Not checked by crossrun: that the frame is the one select.sh produces from the qualification run's records. analyze.sh checks that (INTERPRETATIONS 21).
- Alternative: take whatever charts the reports share (the first implementation), which lets a missing chart shrink the denominators.

## 18. Rule 4 and rule 2 failures

- Reading: a cell failing rule 2 or rule 4 is not in rule 5's list, so it is neither excluded nor counted. crossrun reads the ledger's `unexplained` or `unresolved` status for it and counts it as pending; a ledger `counted` row that fails the cross-checks of entry 16 stops the run.
- Alternative: a separate `uncertified` status.

## 19. New-key reviewer verdict

- Sentence: section 3, "a fresh reviewer ... checks all three and that no existing key covers it; a tie merges".
- Reading: the ledger's new-key reviewer verdict is `admit`, `merge` or `reject`; only `admit` passes the check. The order of `newKeys` is the rank among new keys.
- Alternative: reuse agree, disagree-type, disagree-other, which do not fit.

## 20. P_D leaves out ambient-np

- Sentence: section 1, "P_D: publishers with a counted defect finding"; "`ambient-np` charts found again are reported as prevalence, never in N_new, P_new, D or S".
- Ambiguity: the second sentence lists N_new, P_new, D and S but not P_D.
- Reading: the code leaves ambient-np findings out of P_D as well (they are skipped before any metric is computed, in `metricsOf`), so a publisher whose only counted defect finding is ambient-np is not a P_D publisher. The list in section 1 omits P_D by oversight; leaving it out is the only reading under which pilot 1's class cannot supply the "beyond pilot 1" the experiment asks for.
- Alternative: count ambient-np defect findings toward P_D, which would let a sibling of pilot 1's class lift region A.

## 21. What binds the analysis to the freeze

- Sentence: section 5, "crossrun and the ledger are frozen with the rest and only read records"; section 2, the result "quotes that SHA".
- Gap: crossrun reads whatever files the manifest names, so it could be run on other runs, other tools or another sample.
- Reading: analyze.sh is the only supported way to produce a result, and it refuses unless: the tag exists, HEAD descends from it, frozen files equal the tag's (working tree included), and the data files are committed; every run id was a `compat-exp` `workflow_dispatch` run on a head commit that descends from the tag with equal frozen files, of the mode and Istio version its role needs; the chart list reproduces from the qualification run's records and the frozen `candidates.json`; the tools are built from the tree. The result records the tag commit, HEAD, every run (headSha, mode, Istio, times) and the sha256 of the chart list, ledger, causes and replacements. crossrun itself checks none of this: run by hand it gives a result without provenance, which is not a result of the experiment.
- Times: run end is the latest job `completedAt` in any attempt, run start the earliest job `startedAt` in any attempt (`gh run view --attempt N --json jobs`).
- Needs from the workflow: the guard job's `run-metadata-<id>-<attempt>` artifact (README.md, step 13), because `gh run view` does not return the dispatch inputs.
- Test mode (`MMC_ANALYZE_TEST=1`) allows the tool and file overrides and marks every output not valid.
- Alternative: trust the manifest.

## 22. The replacement rule (beyond the document's wording)

- Sentence: section 4, "a chart whose W0 fails later is replaced by the next reserve in both runs".
- Ambiguity: "fails" is undefined, and the first implementation treated any W0 run left out (one pull failure, one capacity run) as a failure and let the analyst choose which affected chart to replace. Replacing a chart deletes its cells, so the choice was a lever: a chart with a finding could be dropped, a clean one kept.
- Reading implemented (a rule without choice): a chart of the sample (a primary chart, or a reserve once taken) is replaced if and only if its W0 is not 3 of 3 in a sample run AND again in one rerun of that chart's W0 job. "W0 is 3 of 3" for a chart in a report means: in every world entry of the chart, difftable used 3 of 3 W0 runs (none left out as a pull failure or a capacity run), and the chart's `oracleInstall` is `ok` with 3 of 3 installs (`helm:install` ok in every W0 record), which is what qualification demanded. A report without `oracleInstall` fails the test.
- The rerun: the observe artifact is named `exp-<run id>-<chart>-<world>`, with no attempt number, and the workflow's upload step has no overwrite. A rerun of the W0 job inside the same run (Re-run failed jobs, a second attempt) therefore cannot keep its records beside the first attempt's: upload-artifact v4 refuses a second artifact of that name in a run, and where it overwrites, the first attempt's records are gone, so the two attempts cannot be told apart from the artifacts (not exercised on Actions here). The rerun is therefore defined as a separate dispatched run: mode observe, Istio 1.31.3, the same chart list, `chart_ids` exactly that chart, started after the run it re-runs ended. analyze.sh takes it as `--rerun ID` (metadata and report must hold exactly that one chart, no fix entry) and replacements.json names it: `replacements[]` for a replaced chart carries `run` (the sample run in which W0 failed) and `rerun`; `reruns[]` carries `{chart, run, rerun}` for every other rerun. One rerun per chart and run.
- What crossrun enforces: a chart whose W0 is not 3 of 3 in a run needs a named rerun, else error (it is neither replaced nor accepted). If the rerun's W0 is not 3 of 3 either, the chart must be replaced, else error; if it is 3 of 3 in the rerun, the chart must not be replaced, else error, and the rerun's report (and records, when given) stands in for the run's own for that chart: "a W0 that loses runs once and is fine on rerun is not replaced; its rerun records are used". A rerun named for a run in which W0 was 3 of 3, a rerun that is not named by any entry, and a rerun that holds another chart or started before the run ended are errors. Reserves are taken strictly in list order: the k-th replaced chart in the frame's list order gets the k-th reserve, and a taken reserve is subject to the same rule. A stand-in changes when the chart was observed, so rule 1's 12 h gap is checked per chart on the effective observation times and a violation is an error (a run-1 rerun delays the earliest possible run 2).
- Not asked: why W0 failed (a registry refusal, a chart bug); the rule counts only the W0 evidence in the reports.
- Alternative: let the analyst decide per affected chart (the first implementation), or replace on the first failure without a rerun.

## 23. Run identity by workflow file and run-name, and the first qualify run (beyond the document's wording)

- Sentence: section 5, the analysis quotes the freeze SHA and the runs; section 4, the qualification run selects the sample.
- Gap: a run was accepted as an experiment run by the display name of its workflow and by a metadata artifact the run itself uploaded. A later workflow file that names itself `compat-exp` passed. Nothing stopped qualify from being run twice and the better list committed.
- Reading (R1): analyze.sh requires the REST run object (`gh api actions/runs/ID`) to have `path` `.github/workflows/compat-exp.yml` and the `workflow_id` of that file; no workflow file at the run's commit may differ from the tag's; guard.sh freezes `.github/workflows/` as a whole. The workflow has a fixed `run-name`, which GitHub itself evaluates and returns as `display_title`; mode and Istio version in it must equal the metadata artifact and the role of the run (mode and istio are choices, so a free-text input cannot forge them, and the tail must equal the metadata's charts, fixes, ids and fix). Consequence: any change to any workflow file after the tag, including the repository's CI, stops the experiment.
- Reading (R6): the qualification run named in the chart list must be the first completed qualify-mode run (by `display_title`, created at or after the tag commit's time) of the workflow. An earlier one is refused, unless `void-runs.json` lists it with a reason of at least 10 characters and the run's own conclusion is not success. The tag commit's committer time, not the time the tag was made, is the lower bound, which is the stricter choice. Runs that are not yet completed are not looked at.
- Alternative: trust the artifact; trust the workflow name.

## 24. The acceptance check (beyond the document's wording)

- Sentence: section 8 item 9, "It passes only if every chart's W1 and W3 verdict matches pilot 1 ... and any E1 or E2 difference on a chart pilot 1 called same is settled under rule 5 before the freeze is tagged".
- Reading: acceptance-check.sh compares, per chart and world, pilot 1's verdict with the verdict computed from the keys of the classes pilot 1 already had (everything but `apiserver:` and `late:`); `flaky` keys count as same, as in the pilot's table, an `undecided` world is not a match. E1 and E2 differences (differs or flaky) on a chart and world pilot 1 called same are listed, not failed: settling them is the maintainer's act under rule 5. "Pilot 1 called same" is read per chart and world. "Podinfo W1 is C-optout" is read as: the only pre-existing-class difference there is `helm:test`, and in every record `testPodMeshStatus` is `listed` with at least one helm test hook pod, all opted out and none the client (crossrun's R5 condition, applied to one run). A chart whose W0 installs were not all ok is reported as vacuous and does not fail.
- Alternative: fail on E1 and E2 differences; read the match from difftable's world verdict.

## 25. Decisions of 2026-11-12 recorded with the maintainer's name

All five were decided by the maintainer on 2026-11-12 and are implemented as below (checked against the code on that date). The undecided stop is entry 14.

- P_D leaves out ambient-np findings: entry 20; `metricsOf` in finish.go skips ambient-np cells before any metric, P_D included.
- A key counts toward D (and its publishers toward P_D) if any counted finding of it is a defect: entry 11; `metricsOf` sets `defKeys[c.key]` on the first defect cell.
- A cell needing two keys' fixes together is credited to the first key in causes.json order, which is ambient-np when it is one of the two: entry 10; `credit` in analyze.go sorts the parts by `Causes.Order` (causes.json order, new keys last) when none or several parts return the cell alone, and ambient-np is first in causes.json.
- Pending cells that are ambient-np candidates count toward N_max: entry 13; `finish` adds every pending cell to N_max whatever its ledger cause.
- A key admitted after the freeze never earns S: entry 12; `checkID` returns `NoCheckAfterFreeze` for a key outside causes.json.

## 26. A key that enters only by the new-key rule (decided on 2026-11-12 by following the document's own wording)

- Sentence: section 3, the table row for apiserver-caller: "none found (sought on 2026-11-11); the key enters only by the rule below"; the rule is "New key": an anchor or an issue or code reference, a certified fix limited to the mechanism, a chart-independent reproduction, an admitting reviewer; "New keys rank after the frozen list in rule 3"; section 1, S: "A key admitted after the freeze has no such check and never counts in S".
- Gap: the instrument treated the key as an ordinary frozen key (it is in causes.json): a ledger row could credit it with no new-key record, a new-key record for it was refused as "already in causes.json", it ranked eighth in rule 3 and its check `webhook-backend-in-mesh` could earn S. The document had already decided that no anchor was found, so none of that follows from it.
- Reading implemented, for any key whose causes.json entry carries `needsNewKeyRule: true` (the code reads the flag; no key is named in it):
  1. A ledger row, or a part of a row, may credit such a key only if the ledger also holds a complete, admitted new-key record for it (`anchor` an issue, code reference or documentation anchor, `certifiedFix`, `reproduction` script path, `reviewer.verdict` admit with its `run`), exactly as for a key that is not in causes.json. Without it, `ledger check` reports the row and crossrun stops, naming the row (also when crossrun is given a ledger that was not checked: `checkNewKeyRule` in check.go). A new-key record for such a key is accepted by `ledger check`; for a frozen key it is still refused as already in causes.json.
  2. Rank in rule 3 (the ledger's `Causes.Order`): the frozen list is causes.json without the flagged keys; a flagged key ranks after it, among the new keys, in the order of `newKeys` (the order they were admitted). A flagged key with no record ranks after all of them. With the real file, ambient-l7 (ninth in causes.json) now ranks before apiserver-caller.
  3. S: it never earns S. Its static check is still run by static.sh and, when the key has counted findings, reported in the static table with the label "does not count toward S: admitted by the new-key rule" (`notCountedTowardS` in the JSON); `qualifies` is false whatever the check says, and missing static rows for it are not an error. This follows entry 12 and 25 (a key admitted after the freeze never earns S); only the reason differs.
  4. guard.sh: a fix entry's `cause` must be a frozen key (causes.json without flagged keys) or a new key with a complete, admitted record in ledger.json, so a flagged key needs the record there too.
- N_new, P_new, D and P_D count the key like any new key. World-universal for a cause key lists it once.
- Not changed: the frozen static.sh and staticcheck still compute the check for every chart (the check was frozen with the key, causes.json "notes"); the classification prompt says "section 3 table, or for a key admitted later, its ledger entry", which covers it; acceptance-check.sh does not read the cause list.
- Alternative: the instrument's earlier reading (a frozen key), which leans toward Continue and region B (section "Open before the freeze", point 1).
