# ledger

Schema check for `hack/compat-exp/ledger.json`, the hand-maintained data file
of the analysis phase (docs/compat-experiment.md, section 8 item 11). One row
per cell (chart, world, observation key) that differed, holding what a person
or a classifying agent decided and the evidence. The check validates; it never
decides a judgment field. `crossrun` reads the same file.

    go run ./hack/compat-exp/ledger/cmd/ledger check [-ledger hack/compat-exp/ledger.json] [-causes hack/compat-exp/causes.json]

Exit 0 valid, 1 problems (one per line, each naming its row), 2 usage error.

## Shape

    {"schema": 1, "rows": [Row...], "newKeys": [NewKey...]}

Unknown fields are rejected at every level. Row fields:

| field | meaning |
|---|---|
| `chart`, `world`, `key` | the cell; `key` is the observation key; one row per cell |
| `cause` | candidate cause key: in causes.json, or a `newKeys` entry |
| `type`, `typeBasis` | `defect` or `gap`; `typeBasis.valuesKey` and/or `typeBasis.doc` decide it |
| `fix` | `entry` (the `fixOf` target that earns rule 2's certificate: the credited cause's own entry, or the combined entry when the row has `parts`), `form` (`values` or `patch`), `patchSha256` (patch only), `helmLint` |
| `combined`, `parts` | only when the cell needs two keys' fixes (rule 3). `combined`: `entry` (the combined fix entry) and `certification` (its two certification runs). `parts`: one per key, `cause`, `entry` (that key's fix alone), `runs` (the certification runs holding that entry alone; they show whether it returned the cell to W0's value) and `clearsAlone` (true or false, the row's statement of that result; crossrun recomputes it from `runs` and stops on a difference) |
| `runs` | `run1`, `run2`, `certification` (workflow run ids, at least two distinct for a counted row) |
| `recheck` | `version`, `result` (`reproduces-3of3` or `not-reproduced`), `run` |
| `upstream` | `merged` and `link` (link required when merged) |
| `exclusion` | `reason` (pull-failure, capacity, flaky, undecided, c-optout, world-universal), `evidence`, and for c-optout `callers`: the failing caller pods, each a pod name or a pod-name prefix (matches the name itself or the prefix plus a dash); crossrun requires the set to equal the helm test hook pods of `testPodMesh` |
| `reviewer` | `verdict` (agree, disagree-type, disagree-other) and `run` |
| `status` | counted, excluded, unexplained, unresolved |
| `publisher`, `note` | |

A new key (`newKeys`): `key`, `anchor`, `certifiedFix`, `reproduction` (script
path), `reviewer` (`verdict` admit, `run`). Order in the list is the rank among
new keys.

What `check` enforces: enums; cause known or fully recorded; no two rows for
one cell; a cell credited to one key only (parts name distinct keys, one of
them the cause); with `parts`, `fix.entry` is the combined entry and
`runs.certification` the combined runs (a part that leaves the cell differing
cannot earn a certificate, so rule 2 is met by the combined entry's; credit
follows the parts' own results, which crossrun reads from `parts[].runs`); a counted row has everything rules 1 to 4 need (cause, type
and its basis, a fix with `helmLint`, two run ids, two distinct certification
runs, a reproducing recheck, no merged upstream fix, a reviewer verdict other
than disagree-other, a publisher); an excluded row has a reason and evidence; a c-optout row names its callers; c-optout and world-universal rows need a reviewer verdict `agree` and its run, like counted rows.
It cannot tell whether a claim is true: `crossrun` checks the claims against
the difftable reports.
