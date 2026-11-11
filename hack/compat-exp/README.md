# hack/compat-exp: the compat experiment's instrument

The specification is [docs/compat-experiment.md](../../docs/compat-experiment.md).
This directory holds the pieces built for it; the pilot's own scripts stay in
`hack/compat-pilot/` and are reused unchanged in behaviour.

## Files

| file | what it is | state on this branch |
|---|---|---|
| `frame/` | Artifact Hub ranking to `candidates.json`, and `frame charts` to the chart-list shape | built, see `frame/README.md` |
| `qualify.sh` | qualify one candidate in a W0 world: template, server dry-run, 3 installs; one JSON record | built |
| `select.sh` | qualification records plus `candidates.json` to `charts-exp.json` (50 + 15 reserves, deadline, bias table, sha256); exit 1 when fewer than 40 qualify | built |
| `guard.sh` | everything the workflow checks before a cluster exists: inputs, freeze tag, frozen files, list hash, fix entries, matrix size | built |
| `../compat-pilot/observe.sh`, `world.sh` | one chart, one world, one run; the world. `MMC_REGISTRY_MIRROR=1` in every world | built (other work items) |
| `../compat-pilot/difftable/` | records to table or `-json` report; `-cell-worlds`, `-ignore` for certification | built (other work item) |
| `RECORD.md`, `DIFFTABLE-JSON.md` | record and report reference | built |
| `acceptance-charts.json` | the ten pilot-1 charts plus the redis fix as values and as a patch (work item 9) | built |
| `patches/` | fix patches; data, added after the freeze | `redis-np-15008.patch` only |
| `fixes/`, `ignore/` | fix entries and certification ignore lists; data, added after the freeze | empty, created when first used |
| `causes.json` | the nine cause keys | not built on this branch (guard refuses fix entries without it) |
| `staticcheck/` | static check over `helm template` output, `-world W1\|W3` | not built on this branch |
| `ledger/`, `crossrun/` | ledger with schema check; two runs into cells, credit, bounds | not built on this branch |
| `../../.github/workflows/compat-exp.yml` | the workflow, see below | built, never run on Actions |
| `test/` | `qualify_test.sh` (qualify.sh and select.sh), `guard_test.sh`, `workflow_test.sh`, plus the other builders' tests | built |

The workflow has one hook for the unbuilt tools: after difftable, a step lists
which of `staticcheck`, `ledger` and `crossrun` exist as directories. It runs
none of them; wire them in when their interfaces are fixed, before the freeze.

## Order of operations

1. `go run ./hack/compat-exp/frame fetch -out DIR`: the only network step.
2. `frame build -in DIR -out hack/compat-exp/candidates.json`.
3. `frame charts -in hack/compat-exp/candidates.json -out hack/compat-exp/candidates-charts.json`.
4. Acceptance run (work item 9): dispatch `compat-exp` with `mode=acceptance`. It needs no tag. Settle any E1 or E2 difference on a pilot "same" chart under rule 5 first.
5. Build and test the rest (`causes.json`, `staticcheck`, `ledger`, `crossrun`: not built here), edit the recheck line in the workflow, commit.
6. Freeze: tag `compat-exp-freeze` on that commit. It holds the stored responses, `candidates.json` and `candidates-charts.json`. The guard refuses every mode but acceptance without this tag.
7. Qualify: `mode=qualify`. One W0 job per candidate; the last job runs `select.sh` and uploads `charts-exp.json` (artifact `charts-exp-<run id>`). Fewer than 40 qualified fails the job as infeasible (section 7).
8. Select: commit that `charts-exp.json` once, as data, on a descendant of the tag. The guard checks its stored hash and refuses a list touched by more than one commit since the tag.
9. Run 1: `mode=observe`. Run 2 at least 12 h after run 1 ended (section 2 rule 1).
10. Certify: `mode=certify` with `fix_id` and `fixes_file`; difftable gets the entry's `cellWorlds` and `ignore`.
11. Recheck: `mode=recheck`, `istio` = the second choice, `chart_ids` = the cells.
12. Crossrun and ledger over the stored reports: not built here.

The order differs from the first draft of this list in one place: the tag comes
before qualification, because sections 4 and 5 freeze the whole instrument
before the first qualification job and the guard enforces that.

## File formats the guard and select.sh use

`charts-exp.json` (written by `select.sh`): `schema`, `sha256`,
`installDeadlineSeconds`, `counts`, `charts` (50), `reserves` (15), `bias`.
Entries carry `id`, `chart`, `version`, optional `repo`, `why`, `rank` and
`deadline` (the same for all, `<n>s`). The hash is the sha256 of
`jq -S -c 'del(.sha256)'` of the file.

A fix entry (an array in a file under `hack/compat-exp/fixes/`): the fields
observe.sh reads (`id`, `chart`, `version`, `repo`, `values`, `patch`,
`fixOf`) plus `cause` (a key of `causes.json`), and for certification
`cellWorlds` (a list from W1, W3) and `ignore` (a file under
`hack/compat-exp/ignore/`). Chart, version and repo must equal the base
chart's. `values` may hold only `--set`, `--set-string` and `--set-json`
pairs. `patch` must sit under `hack/compat-exp/patches/`.

## Tests

```console
hack/compat-exp/test/qualify_test.sh
hack/compat-exp/test/guard_test.sh
hack/compat-exp/test/workflow_test.sh
```

They need `bash`, `jq`, `yq` (mikefarah), `git` and `python3`. `actionlint` runs
inside `workflow_test.sh` when installed.
