# hack/compat-exp: the compat experiment's instrument

The specification is [docs/compat-experiment.md](../../docs/compat-experiment.md).
This directory holds the pieces built for it; the pilot's own scripts stay in
`hack/compat-pilot/` and are reused unchanged in behaviour. Everything below
is built and tested; the workflow has never run on Actions.

## Files

| file | what it is |
|---|---|
| `frame/` | Artifact Hub ranking to `candidates.json` (`frame build`), and `frame charts` to the chart-list shape. `frame/exclusions.json` is part of it. See `frame/README.md` |
| `qualify.sh` | qualify one candidate in a W0 world: template, server dry-run, 3 installs; one JSON record |
| `select.sh` | qualification records plus `candidates.json` to `charts-exp.json` (50 + 15 reserves, deadline, bias table, sha256, publisher and `qualified` per entry); exit 1 when fewer than 40 qualify |
| `guard.sh` | everything the workflow checks before a cluster exists: inputs, freeze tag, the whole frozen instrument, list hash, fix entries, matrix size |
| `static.sh` | per chart and mesh world: `helm template` as `observe.sh` installs, then `staticcheck`; writes the file crossrun reads |
| `analyze.sh` | the analysis procedure for a maintainer's machine, bound to the freeze: tag, frozen files, run identity, qualification reproduction, download, ledger check, static.sh, crossrun; records its provenance |
| `staticcheck/` | the static checks over rendered manifests, one per cause key that has one |
| `ledger/`, `ledger.json` | the ledger tool with its schema check (`ledger check`), and the ledger (data) |
| `replacements.json` | data: the reruns and the replacements of the frozen sample (section 4, section 7); `{"schema": 1, "reruns": [{"chart", "run", "rerun"}], "replacements": [{"chart", "reserve", "reason", "run", "rerun"}]}`, empty until a run leaves a chart-world undecided or a W0 failing. A chart is rerun in a run if and only if that run leaves one of its chart-worlds undecided or its W0 not 3 of 3 (the rerun is a separately dispatched observe run with `chart_ids` that one chart, given to analyze.sh as `--rerun`); it is replaced by the next reserve if and only if its W0 is not 3 of 3 in the run and again in the rerun. crossrun derives both rules from the reports and errors on any other entry (crossrun/INTERPRETATIONS.md 22, 27) |
| `crossrun/` | two runs into cells, exclusions, credit, N_new, P_new, D, P_D, S, region, bounds; `INTERPRETATIONS.md` lists every reading chosen |
| `causes.json` | the nine cause keys: anchors, mechanisms, `worlds`, `staticCheck`; the contract guard.sh, ledger, static.sh and crossrun read |
| `classification-prompt.md` | the prompt of the fresh-context classification check |
| `../compat-pilot/observe.sh`, `world.sh`, `render-args.sh` | one chart, one world, one run; the world; the install arguments observe.sh and static.sh share. `MMC_REGISTRY_MIRROR=1` in every world |
| `../compat-pilot/difftable/` | records to table or `-json` report; `-cell-worlds`, `-ignore` for certification |
| `RECORD.md`, `DIFFTABLE-JSON.md` | record and report reference |
| `acceptance-check.sh`, `acceptance-expected.json` | the pass condition of work item 9 as a tool (input: the report artifact of an acceptance run) and the pilot-1 W1 and W3 verdicts it compares with, transcribed from the results table of docs/compat-pilot.md (a test keeps them in step) |
| `void-runs.json` | data: qualify runs declared void, each with a reason; read by analyze.sh (step 7) |
| `acceptance-charts.json` | the ten pilot-1 charts plus the redis fix as values and as a patch (work item 9) |
| `patches/`, `fixes/`, `ignore/` | fix patches, fix entries and certification ignore lists: data, added after the freeze (`fixes/` and `ignore/` are created when first used) |
| `../../.github/workflows/compat-exp.yml` | the workflow, see below |
| `test/` | the tests, see the end |

The frozen set (what `guard.sh` compares with the tag, byte for byte): `frame/`
(with `exclusions.json`), `qualify.sh`, `select.sh`, `static.sh`, `analyze.sh`,
`guard.sh`, `causes.json`, `classification-prompt.md`, `staticcheck/`,
`ledger/`, `crossrun/`, `candidates.json`, `candidates-charts.json`,
`../compat-pilot/{observe.sh,render-args.sh,world.sh,difftable}`, `acceptance-check.sh`, `acceptance-expected.json`, the whole `.github/workflows/` directory (a workflow file added or changed after the tag fails the freeze, so the repository's other workflows are frozen with it),
`docs/compat-experiment.md`, `go.mod`, `go.sum`. Data stays outside it:
`ledger.json`, `replacements.json`, `patches/`, `fixes/`, `ignore/`, and `charts-exp.json` (which
keeps its one-commit rule). analyze.sh refuses an uncommitted change to any of them.

## Order of operations

Commands run from the repository root.

1. Fetch (the only network step):
   `go run ./hack/compat-exp/frame fetch -out DIR`
2. Candidates:
   `go run ./hack/compat-exp/frame build -in DIR -out hack/compat-exp/candidates.json`
3. Chart-list shape:
   `go run ./hack/compat-exp/frame charts -in hack/compat-exp/candidates.json -out hack/compat-exp/candidates-charts.json`
4. Acceptance run (work item 9): GitHub accepts a manual dispatch only for a workflow file on the default branch, and `compat-exp.yml` is not there until the instrument is merged. So the acceptance run starts by a push to the branch `compat-exp-acceptance` (an empty commit pushed again reruns it; write access to the repository is needed, a fork cannot fire it). A push has no inputs: it runs mode `acceptance`, Istio 1.31.3 and `acceptance-charts.json`, and the guard refuses a push whose mode is anything else or whose ref is another branch. Every other mode (qualify, observe, certify, recheck) is a manual dispatch from the default branch after the instrument is merged; a push can never start them, and analyze.sh refuses any run whose event is `push`. After the merge, `mode=acceptance` can also be dispatched by hand. It needs no tag. Download its report artifact and run `hack/compat-exp/acceptance-check.sh DIR` on it: it passes only if every chart's W1 and W3 verdict matches pilot 1 (on the key classes pilot 1 had), redis W3 differs, both redis fixes certify and podinfo's W1 difference is c-optout; it lists the `apiserver:` and `late:` (E1, E2) differences on pilot "same" charts, which are settled under rule 5 before the tag, and says plainly which chart's W0 never installed (its match is vacuous). Exit 1 on any mismatch.
5. Edit the recheck line in the workflow (the one marked FREEZE EDIT), run all the tests (below), commit.
6. Freeze: tag `compat-exp-freeze` on that commit: `git tag compat-exp-freeze`. The tag holds the whole frozen set above. The guard refuses every mode but acceptance without it.
7. Qualify: dispatch `mode=qualify`, once. analyze.sh requires the qualification run named by the chart list to be the first completed qualify run after the tag; an earlier one must be listed in `void-runs.json` with a reason (`{"schema":1,"void":[{"run":"<id>","reason":"<why>"}]}`) and must itself have a conclusion other than success. A successful qualify run can never be voided. One W0 job per candidate; the last job runs `select.sh hack/compat-exp/candidates.json qual out/charts-exp.json` and uploads `charts-exp.json` (artifact `charts-exp-<run id>`). Fewer than 40 qualified fails the job as infeasible (section 7).
8. Select: commit that `charts-exp.json` once, as `hack/compat-exp/charts-exp.json`, on a descendant of the tag. The guard checks its stored hash and refuses a list touched by more than one commit since the tag.
9. Run 1: dispatch `mode=observe`. Then, before run 2: rerun, once per chart, every chart that run 1 left with a chart-world (W1 or W3) undecided or with its W0 not 3 of 3 (3 usable runs, 3 installs ok): dispatch `mode=observe` with `chart_ids` that one chart (all three worlds), after run 1 ended and done before run 2 starts. A rerun's records replace the chart's records of that run wholesale, never a world at a time. A chart whose W0 fails in the run and again in its rerun is replaced by the next reserve; any other chart keeps its place with the rerun standing in (rerun run ids go in `replacements.json`, `reruns` or `replacements`, and to `analyze.sh --rerun`; one list serves the replacement rule and the undecided stop). Run 2: the same at least 12 h after run 1 ended (section 2 rule 1), then its reruns, each started after run 2 ended. Dispatching run 2 while a run-1 rerun is still running, or leaving out a rerun the rule demands, makes the final analysis an error; after run 1 only, the missing reruns are reported as pending. The 15% undecided stop is read after the reruns (crossrun/INTERPRETATIONS.md 27). Each run uploads `compat-exp-report-<run id>`: `report-<run id>.json` (difftable `-json -worlds W1,W3`), `table.md`, `results/` (the records, one directory per observe artifact: `results/exp-<run id>-<chart>-<world>/<chart>.<world>.run<N>.json`, see RECORD.md).
10. Ledger rows, fix entries and patches (data, from now on): `hack/compat-exp/ledger.json` (checked by `go run ./hack/compat-exp/ledger/cmd/ledger check`), `hack/compat-exp/fixes/*.json`, `hack/compat-exp/ignore/*.json`, `hack/compat-exp/patches/*.patch`.
11. Certify: dispatch `mode=certify` with `fixes_file` and `fix_id`, twice (two workflow runs). difftable gets the entry's `cellWorlds` and `ignore`. The guard job runs `ledger check` first.
12. Recheck: dispatch `mode=recheck`, `istio` = the second choice, `chart_ids` = the cells. The guard job runs `ledger check` first.
13. Analyze, on a maintainer's machine with `gh` logged in (read access to the runs), the tag fetched, and the data files (ledger, chart list, replacements, fixes, patches) committed:

   ```console
   hack/compat-exp/analyze.sh --out analysis-1 --run1 RUN1 --run2 RUN2 \
     --cert CERT_RUN_A --cert CERT_RUN_B --recheck RECHECK_RUN
   ```

   Every run is pinned to the workflow file, not to its display name: the REST run object must have path `.github/workflows/compat-exp.yml` and that file's workflow id, and its `display_title` is the workflow's `run-name`, `compat-exp mode=<mode> istio=<istio> charts=<charts_file> fixes=<fixes_file> ids=<chart_ids> fix=<fix_id>`, which GitHub evaluates from the file at the run's commit; mode and Istio version must equal the metadata artifact and the run's role.

   It refuses to run unless the analysis is bound to the freeze: the tag
   `compat-exp-freeze` exists locally, HEAD descends from it, every frozen file is
   byte-identical to the tag's (`guard.sh freeze`, from the tag's own copy of
   guard.sh), and the data files are committed. The tools are built from the checked
   tree; `CROSSRUN`, `LEDGER`, `STATICCHECK`, `STATIC_CAUSES`, `STATIC_WORKFLOW` are
   refused unless `MMC_ANALYZE_TEST=1`, which marks every output as not valid.
   For every run id (the qualification run named by the chart list, run 1, run 2,
   certification, recheck) it checks `gh run view` (head commit, workflow
   `compat-exp`, `workflow_dispatch`, completed) and the run metadata artifact
   `run-metadata-<id>-<attempt>` of every attempt (mode and Istio version for the role: run 1 and 2 observe, certify,
   recheck on the version decided at the freeze, the qualification run qualify on the
   primary version). It downloads the qualification run's records, runs `select.sh`
   on them and `candidates.json`, and stops unless the chart list is
   byte-identical. It then downloads each run's `compat-exp-report-<id>`, runs
   `ledger check`, `static.sh hack/compat-exp/charts-exp.json analysis-1/static.json <chart ids of the reports>`
   and `crossrun`, and leaves `result.json`, `result.md`, `provenance.json`,
   `inputs.json`, `static.json`, `static.json.meta.json` and `ledger-check.txt` in
   `analysis-1/`. The result records the freeze tag commit, HEAD, each run id with
   headSha, mode, Istio version and start and end times (the end is the latest job
   completion of any attempt, the start the earliest job start of any attempt), and
   the sha256 of the chart list, `ledger.json`, `causes.json` and `replacements.json`.
   A run 2 that started less than 12 h after run 1 ended stops with an error. With
   only `--run1` (the futility check after run 1, use a ledger without counted rows)
   the output says "run 1 only" and declares no region. Run ids are digits only.
   The two steps by hand: `hack/compat-exp/static.sh hack/compat-exp/charts-exp.json static.json`
   and `go run ./hack/compat-exp/crossrun -inputs inputs.json -json result.json -md result.md`.

   The run metadata: the workflow's guard job (the only writer; it never runs a
   chart) uploads the artifact `run-metadata-<run id>-<run attempt>` holding
   `run-metadata.json`, written from workflow context only, with `schema` (1), `mode`,
   `istio`, `chartsFile`, `chartsSha256`, `fixesFile`, `chartIds`, `fixId` (the dispatch
   inputs; chartsFile is the resolved default when the input is empty), `headSha`,
   `freezeTagSha`, `workflowRef`, `eventName`, `runId`, `runAttempt`, because
   `gh run view --json` does not return the inputs. The file is still only an artifact,
   so analyze.sh downloads it for every attempt (attempt 1 must exist; a rerun of failed
   jobs does not run the guard job again, so a later attempt may have none), checks it
   against `gh run view` (run id, head sha, event, workflow name against `workflowRef`),
   the freeze tag and the chart list analysed, and stops unless the attempts agree on
   everything but `runAttempt`. `test/lib-runmeta.sh` runs the workflow's own step for the
   tests of both sides. The select job must also keep `qual/` in the `charts-exp-<run id>`
   artifact (it does) and call `select.sh` with `GITHUB_RUN_ID` set (it is, on Actions).

The order differs from the first draft of this list in one place: the tag comes
before qualification, because sections 4 and 5 freeze the whole instrument
before the first qualification job and the guard enforces that.

## One shape per shared file

| file | writer | readers | shape |
|---|---|---|---|
| `causes.json` | a person, at the freeze | guard.sh, ledger, crossrun, static.sh | object; list under `keys`; per key `key`, `worlds`, `staticCheck` (id or null) |
| `candidates.json` | `frame build` | `frame charts`, select.sh | `candidates[]` with `package`, `repository`, `publisher`, `chart`, `repo`, `version`, `rank` |
| `charts-exp.json` | select.sh | guard.sh, static.sh, crossrun (as its frame) | `charts[]`, `reserves[]` of `id`, `chart`, `version`, `repo`, `deadline`, `publisher`, `qualified`, `rank`, `why` |
| plan `charts.json` | guard.sh plan | observe.sh (`CHARTS_FILE`) | flat array of charts, reserves and fix entries |
| records | observe.sh | difftable, crossrun (`podMesh`) | `RECORD.md` |
| report | difftable `-json` | crossrun | `DIFFTABLE-JSON.md` |
| `static.json` | static.sh | crossrun | `[{chart, world, check, applicable, flagged}]` |
| `ledger.json` | a person | ledger check, guard.sh (new keys), crossrun | `ledger/README.md` |

Chart ids are one string everywhere: the package name, or `<repository>-<name>`
when two candidates share a name. The rule is implemented twice, in `frame
charts` and in select.sh (the e2e test compares them); observe.sh records it as
`chart`, difftable reports it as `chart`, and the ledger and crossrun use it.

## File formats the guard and select.sh use

`charts-exp.json` (written by `select.sh`): `schema`, `sha256`,
`installDeadlineSeconds`, `counts`, `charts` (50), `reserves` (15), `bias`,
`qualification` (`run`: the qualification run id, `candidatesSha256`,
`recordsSha256`: what the list was selected from; analyze.sh reruns select.sh on
that run's records and refuses a list that does not reproduce byte for byte).
Entries carry `id`, `chart`, `version`, optional `repo`, `why`, `rank`,
`publisher`, `qualified` (true) and `deadline` (the same for all, `<n>s`). The
hash is the sha256 of `jq -S -c 'del(.sha256)'` of the file.

A fix entry (an array in a file under `hack/compat-exp/fixes/`): the fields
observe.sh reads (`id`, `chart`, `version`, `repo`, `values`, `patch`,
`fixOf`) plus `cause` (a key of `causes.json`, or a new key recorded in
`ledger.json` with anchor, certified fix, reproduction and an admitting
reviewer verdict), and for certification `cellWorlds` (a list from W1, W3) and
`ignore` (a file under `hack/compat-exp/ignore/`). Chart, version and repo
must equal the base chart's. `values` may hold only `--set`, `--set-string` and
`--set-json` pairs. `patch` must sit under `hack/compat-exp/patches/`.

## Tests

```console
hack/compat-exp/test/qualify_test.sh    # qualify.sh and select.sh
hack/compat-exp/test/guard_test.sh
hack/compat-exp/test/acceptance_check_test.sh  # acceptance-check.sh on fixtures, and its table against docs/compat-pilot.md
hack/compat-exp/test/workflow_test.sh
hack/compat-exp/test/observe_test.sh
hack/compat-exp/test/world_test.sh
hack/compat-exp/test/static_test.sh
hack/compat-exp/test/analyze_test.sh    # analyze.sh refusals, in a throwaway repository with a stub gh
hack/compat-exp/test/e2e_test.sh        # the whole chain with stub helm, kubectl, istioctl and gh
go test ./hack/...
```

They need `bash`, `jq`, `yq` (mikefarah), `git`, `go` and `python3`. `actionlint` runs
inside `workflow_test.sh` when installed. `e2e_test.sh` lists its hand-made
intermediates in its header.
