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
- Reading: the manifest gives `started` and `ended` per run (RFC 3339); a gap under 12 h (exactly 12 h passes) makes no cell hold under rule 1, status `not-reproduced`, with a prominent warning. It is not an error.
- Alternative: stop with an error, since the instrument was run wrongly.

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
- Reading: the ledger row asserts c-optout; crossrun requires records (podMesh) for the sample runs and at least one non-client pod of that chart and world with the world's opt-out metadata (`sidecar.istio.io/inject=false` in a sidecar world, `istio.io/dataplane-mode=none` in W3, the only ambient world, a constant in the code). It prints every such pod. It cannot know which pods were the failing callers, or that no other caller failed: that stays the ledger's judgment. For open cells it prints the same evidence when opted-out pods exist, without asserting.
- Alternative: derive the callers from helm:test keys and test-pod names, which the report does not carry.

## 10. Rule 3 credit

- Sentence: "a cell goes to the key whose part alone returns it to W0's value, else to the first key in the list".
- Reading: the ledger row lists `parts` (cause, fix entry, part-alone run ids) and a `combined` certificate. A part returns the cell when its fix entry's key verdict is `same` in every listed part run. Exactly one returning part gets the cell; otherwise (none or several) the part whose cause ranks first in causes.json (new keys after, in ledger `newKeys` order). If the row's `cause` differs from this, crossrun stops. A row without parts is credited to its `cause`.
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
- Reading: undecided cells (primary reason undecided) over all cells listed in the result (including excluded and not-reproduced ones); over means strictly more than 15%; 3 of 20 is not over. "After one rerun" is a procedure, not an input: crossrun reports the share of the reports it is given.
- Alternative: count pull-failure and capacity cells as undecided too, or use findings only as the denominator.

## 15. "The region they would reach if all counted"

- Reading: counted cells plus every unresolved, unexplained or awaiting cell, each with the ledger's cause and type when the row has them; a cell with no cause is its own distinct new key of type gap, not flagged by any static check. Excluded and not-reproduced cells stay out. So it is a hypothetical, not a bound; the bound is the reachable list of section 7.
- Alternative: assume the best case (every open cell a distinct defect key with a flagged check).

## 16. What crossrun cross-checks and what it takes from the ledger

- Reading: against the reports it checks the run ids, that the cell is held in both runs, that each certification run id is in the manifest, the fix entry's `fixOf` equals the cell's chart, its certificate is earned, lists the cell's world in `cellWorlds` and `cleared`, does not ignore the cell and does not pass the world uncompared, in each of two distinct runs; the recheck report (when given) shows the cell differing; the mechanical exclusion reasons. It takes on trust: helm lint, the patch sha256 and its 40-line limit, the merged-upstream check, the reviewer verdicts, the recheck when no recheck report is given (a warning counts those rows), and the type basis. "A fix whose fixOf is unknown" is read as: the fix entry is not in the certification report, or its `fixOf` is not the cell's chart.
- Alternative: fail rows whose unverifiable fields are unchecked.

## 17. Frame input

- Reading: the frame is the frozen chart list select.sh writes (charts-exp.json): `charts` then `reserves`, each with `id`, `publisher` and `qualified` (select.sh takes the publisher from candidates.json and writes `qualified` true for every listed chart, since only qualified ones are listed). Publisher is the frame's, per rule "Publisher". A chart in a report but not in the frame, or marked not qualified, stops the run. Reserves replaced in both runs are the frame's concern.
- Alternative: take publishers from the ledger only.

## 18. Rule 4 and rule 2 failures

- Reading: a cell failing rule 2 or rule 4 is not in rule 5's list, so it is neither excluded nor counted. crossrun reads the ledger's `unexplained` or `unresolved` status for it and counts it as pending; a ledger `counted` row that fails the cross-checks of entry 16 stops the run.
- Alternative: a separate `uncertified` status.

## 19. New-key reviewer verdict

- Sentence: section 3, "a fresh reviewer ... checks all three and that no existing key covers it; a tie merges".
- Reading: the ledger's new-key reviewer verdict is `admit`, `merge` or `reject`; only `admit` passes the check. The order of `newKeys` is the rank among new keys.
- Alternative: reuse agree, disagree-type, disagree-other, which do not fit.
