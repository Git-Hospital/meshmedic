# crossrun

Turns difftable `-json` reports, the ledger and the cause list into the numbers
the frozen decision rule of docs/compat-experiment.md reads (section 8 item 10):
the cells that hold in both runs, the rule 5 exclusions, credit under rule 3,
N_new, P_new, D, P_D, S, the region and the section 7 bounds. It only reads;
it never writes the ledger and never decides a judgment field. When the ledger
and the reports disagree it stops with an error naming the row.

    go run ./hack/compat-exp/crossrun -inputs inputs.json [-json out.json] [-md out.md]

`-` is stdout; with neither flag the markdown goes to stdout. Exit 0 ok, 1 the
ledger and the reports disagree or run 2 started under 12 h after run 1 ended (error names the row or both times; no output is written), 2 usage or read error.
Output is deterministic: the same inputs give the same bytes.

## Inputs

`inputs.json` (unknown fields rejected; paths relative to the file):

    {"runs": [{"id", "report", "records" (optional dir), "started", "ended"}, ...],
     "reruns": [same shape as runs], optional,
     "certification": [{"id", "report", "istio" (optional)}, ...],
     "recheck": {"id", "report", "istio"},            optional
     "causes": "...", "ledger": "...", "frame": "...", "static": "..." (optional),
     "replacements": "..." (optional), "provenance": {...} (optional), "testMode": true (optional)}

- `runs`: run 1, then run 2 once it exists (with run 1 only, cells wait for run 2 and the futility bound still works). `started` and `ended` are RFC 3339; the 12 h gap comes from them; a gap under 12 h is an error, not a result. With run 1 only the output says "run 1 only", has `final: false` and declares no region.
- `report`: `difftable -json` output of one workflow run's records (hack/compat-exp/DIFFTABLE-JSON.md). Certification and recheck runs use the same shape; a certification report holds the fix entries with their `certificate`.
- `records`: the observation records of that run (hack/compat-exp/RECORD.md); `podMesh` and `testPodMesh` are read, for c-optout evidence.
- `causes`: hack/compat-exp/causes.json, the real file: an object whose list is under `keys`, in rank order, each entry with `key`, `worlds`, `staticCheck` (a check id, or null for a key without a static check) and, optionally, `needsNewKeyRule: true`. No other shape is read. A flagged key is in the file but enters only by the new-key rule: a ledger row may credit it only with a complete, admitted `newKeys` record, it ranks among the new keys in the order they were admitted, and it never earns S (its check is reported, labelled as not counting toward S; INTERPRETATIONS.md 26).
- `ledger`: hack/compat-exp/ledger.json; it must pass `ledger check`. See hack/compat-exp/ledger/README.md.
- `frame`: the frozen chart list `charts-exp.json` that select.sh writes (`charts`, then `reserves`; each entry has `id`, `publisher` and `qualified`, which select.sh carries from candidates.json and the qualification records).
- `reruns` (optional): the separately dispatched observe runs restricted to one chart that serve as the rerun of that chart's W0 job.
- `replacements` (optional): `{"schema": 1, "reruns": [{"chart", "run", "rerun"}], "replacements": [{"chart", "reserve", "reason", "run", "rerun"}]}`, section 4 replacements. Every chart of the frame's primary list, with the replacements applied, must be in every given report with W0, W1 and W3, or the run stops with an error naming the chart and world. A chart is replaced if and only if its W0 is not 3 of 3 in a run and again in the rerun; crossrun derives that from the reports, errors on a chart that meets it and is not replaced, on a replaced chart that does not, and takes reserves in list order (INTERPRETATIONS.md 22).
- `static` (optional, written by hack/compat-exp/static.sh): `[{"chart", "world", "check", "applicable", "flagged"}]`, one row per chart, world and check. Needed for S. `applicable` false means the check does not apply to that world.

## Output

JSON (`schema` 1) and a short markdown table, both with: the run and gap
summary and warnings; every cell with its two run states, status (counted,
excluded, unexplained, unresolved, not-reproduced, awaiting-run-2), credit,
type and exclusion reasons; the world-universal numerators and denominators;
c-optout evidence from podMesh; ambient-np prevalence; the five metrics, the
static-check detail, the region with the first-match trace; N_max, P_max, the
reachable regions, the stop flags; the undecided share against 15% under both readings (cells; chart-worlds), with the constant that picks the one driving the stop (the chart-worlds reading, INTERPRETATIONS.md 14); the open
cells and the region they would reach if all counted.

How far c-optout is mechanised: crossrun computes the failing callers itself.
For the key `helm:test` only, in every record of both runs, `testPodMeshStatus`
must be `listed`, the callers are all the pods of `testPodMesh` with `testHook`
true, each must be opted out in that world and not the client, and the ledger
row's `exclusion.callers` (pod names or prefixes) must equal that set. A test pod
left in the mesh refuses the exclusion even if the row names only the others.
That the opted-out calls are the failure is still a judgment: the reviewer
verdict (agree). See INTERPRETATIONS.md 9.

crossrun does not check that its inputs are the frozen instrument's: analyze.sh does, and records the freeze commit, HEAD, the runs and the files read in the manifest's `provenance`, which the result carries (INTERPRETATIONS.md 21). A manifest with `testMode: true` gives a result that says it is not valid.

Every ambiguity of the pre-registration is listed with the reading chosen in
INTERPRETATIONS.md.

## Tests

    go test -race ./hack/compat-exp/crossrun/

Table-driven tests for each rule and each threshold boundary (90%, 10%, 15%,
12 h), the decision table (each edge, first match, monotonicity), the
disagreement errors, and a pilot 1 test over `testdata/pilot` (reports are
`difftable -json -worlds W1,W3` over the stored records of workflow runs
40109270484 and 40119399052, trimmed to few keys; the podMesh records, the
publishers, the 12 h gap and the recheck are hand-built).
