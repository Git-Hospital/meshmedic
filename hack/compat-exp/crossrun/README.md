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
     "certification": [{"id", "report", "istio" (optional)}, ...],
     "recheck": {"id", "report", "istio"},            optional
     "causes": "...", "ledger": "...", "frame": "...", "static": "..." (optional)}

- `runs`: run 1, then run 2 once it exists (with run 1 only, cells wait for run 2 and the futility bound still works). `started` and `ended` are RFC 3339; the 12 h gap comes from them; a gap under 12 h is an error, not a result. With run 1 only the output says "run 1 only", has `final: false` and declares no region.
- `report`: `difftable -json` output of one workflow run's records (hack/compat-exp/DIFFTABLE-JSON.md). Certification and recheck runs use the same shape; a certification report holds the fix entries with their `certificate`.
- `records`: the observation records of that run (hack/compat-exp/RECORD.md); only `podMesh` is read, for c-optout evidence.
- `causes`: hack/compat-exp/causes.json, the real file: an object whose list is under `keys`, in rank order, each entry with `key`, `worlds` and `staticCheck` (a check id, or null for a key without a static check). No other shape is read.
- `ledger`: hack/compat-exp/ledger.json; it must pass `ledger check`. See hack/compat-exp/ledger/README.md.
- `frame`: the frozen chart list `charts-exp.json` that select.sh writes (`charts`, then `reserves`; each entry has `id`, `publisher` and `qualified`, which select.sh carries from candidates.json and the qualification records).
- `static` (optional, written by hack/compat-exp/static.sh): `[{"chart", "world", "check", "applicable", "flagged"}]`, one row per chart, world and check. Needed for S. `applicable` false means the check does not apply to that world.

## Output

JSON (`schema` 1) and a short markdown table, both with: the run and gap
summary and warnings; every cell with its two run states, status (counted,
excluded, unexplained, unresolved, not-reproduced, awaiting-run-2), credit,
type and exclusion reasons; the world-universal numerators and denominators;
c-optout evidence from podMesh; ambient-np prevalence; the five metrics, the
static-check detail, the region with the first-match trace; N_max, P_max, the
reachable regions, the stop flags; the undecided share against 15%; the open
cells and the region they would reach if all counted.

How far c-optout is mechanised: the ledger row names the failing callers
(`exclusion.callers`, pod names or prefixes as in `podMesh`). crossrun accepts
the exclusion for the key `helm:test` only, and only when, in every record of
both runs, each named caller is a pod in `podMesh`, is not the instrument's
client and carries the opt-out metadata of that world. That the named callers
are all the failing callers is a judgment: the row's reviewer verdict (agree) asserts it.
Helm test pods are normally not in `podMesh` (it is taken before `helm test`),
so on real records the exclusion fails; see INTERPRETATIONS.md 9.

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
