# difftable -json

`go run ./hack/compat-pilot/difftable -json [-runs 3] [-oracle W0] [-worlds W1,W2,W3] RESULTS_DIR`
writes this report to stdout instead of the table. It holds everything the
table shows, per chart, world and observation key, for a tool that joins two
workflow runs. Without `-json` the output is the table, unchanged.

The output is indented with two spaces and ends in a newline. Lists are sorted
(charts by name, worlds in `-worlds` order, keys by name, runs by number) and
no JSON object is used as a map, so one input always gives the same bytes,
whatever order the records were read in. `schema` is raised when a field is
renamed or changes meaning; adding a field does not raise it.

## Report

| field | type | meaning |
|---|---|---|
| `schema` | int | 1 |
| `oracle` | string | the world every other world is held against (`-oracle`) |
| `worlds` | string[] | the compared worlds (`-worlds`) |
| `runsRequired` | int | runs per world needed for a verdict (`-runs`) |
| `charts` | ChartReport[] | one per chart or fix entry |

## ChartReport

| field | type | meaning |
|---|---|---|
| `chart` | string | chart id |
| `chartVersion` | string | version from the records |
| `fixOf` | string | the base chart, only for a fix entry |
| `environment` | string | the versions each world ran with, as the table prints them |
| `worlds` | WorldReport[] | one per compared world |
| `certificate` | CertReport | only for a fix entry |

## WorldReport

| field | type | meaning |
|---|---|---|
| `world` | string | |
| `verdict` | string | `same`, `differs`, `flaky` or `undecided` |
| `undecidedReason` | string | only when `undecided` |
| `findings` | int | keys that differ in every used run |
| `flakyKeys` | int | keys that differ in some used runs |
| `runsUsed` | int[] | run numbers compared, in this world |
| `runsLeftOut` | RunLeftOut[] | runs of this world not used |
| `oracleRunsUsed` | int[] | run numbers compared, in the oracle world |
| `oracleRunsLeftOut` | RunLeftOut[] | oracle runs not used |
| `analyzeCodes` | string[] | `istioctl analyze` codes seen in the used runs |
| `keys` | KeyReport[] | every observation key seen in either side |

RunLeftOut: `run` (int), `reason` (`pull-failure` or `capacity`), `detail`
(the image for a pull failure; the first pod and its message, `pod: message`, for capacity). A run that is both is a `pull-failure`.

## KeyReport

| field | type | meaning |
|---|---|---|
| `key` | string | the observation key |
| `class` | string | the part of the key before the first colon (`helm` for `helm:test`; a key without a colon is its own class) |
| `verdict` | string | `same`, `differs` (every used run differs from the oracle), `flaky` (some do), `left out` or `undecided` (the world has too few runs) |
| `reason` | string | why the key is `left out` (unstable in the oracle, or a probe that carries no information) or `undecided` |
| `oracleValue` | string | the value every used oracle run agreed on; absent when they did not agree or the oracle has too few runs. A key a run did not produce has the value `absent` |
| `worldValues` | string[] | the value in each used run of the world, in run order |
| `misses` | int | used runs whose value differs from `oracleValue` |

A key's verdict `differs` is what the pilot calls a finding.

## CertReport

Present on a fix entry (a chart whose records carry `fixOf`).

| field | type | meaning |
|---|---|---|
| `fix`, `base` | string | the fix entry and the chart it repairs |
| `certified` | bool | true when `problems` is empty and `cleared` is not |
| `cleared` | string[] | cell worlds where the base differed and the fix does not |
| `problems` | string[] | why it is not certified |
| `cellWorlds` | string[] | the worlds the fix had to clear: `-cell-worlds`, else all of `worlds` |
| `ignored` | IgnoreCell[] | every ignore-list entry for a key that the certificate did not count, sorted by world, key, chart |
| `worldsNotCompared` | IgnoreCell[] | ignore entries with key `*`: whole worlds with no verdict that the certificate passed; never in `ignored` |

IgnoreCell: `chart`, `world`, `key`, `reason`, as in the ignore file.

## Certification options

By default a fix fails while any mesh world still differs from the oracle.

- `-cell-worlds W3[,W1]`: a subset of `-worlds`. Only these worlds must come out
  clean (and one of them must have been cleared). In the other worlds of
  `-worlds` the fix may differ from its oracle only on keys the base chart also
  differs on there; a key that differs for the fix and not for the base fails
  the certificate. The oracle world must still match the base's oracle world.
- `-ignore FILE`: a JSON array of `{"chart", "world", "key", "reason"}`, all
  four required, no other fields. The cell is not counted as a remaining
  finding or flaky key, and it does not count as a cleared finding either. The
  chart is the fix entry or its base. The key `"*"` stands for a world with no
  verdict (too few runs); it is the only way past an undecided world, and it is
  printed apart ("Worlds NOT COMPARED") and reported in `worldsNotCompared`. An entry
  that matches no differing (or, for `"*"`, undecided) cell of any fix entry is
  an error, as is an unknown world, the oracle world or a repeated entry; the
  run exits with status 2 and prints nothing else. The table lists each
  ignored cell under the certificates, and the JSON lists it in `ignored`.

## Input

The records are the ones in `hack/compat-exp/RECORD.md`. difftable reads
`world`, `chart`, `chartVersion`, `run`, `fixOf`, `environment`, `obs`,
`analyze`, `ports`, `workloads`, `pullFailures` and `capacityPending` (an array
of `{pod, message}`; non-empty leaves the run out, absent, null or empty does
not) and ignores every other field. Unknown fields are not rejected.
