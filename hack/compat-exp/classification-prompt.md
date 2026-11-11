# Classification check prompt

The prompt given to the fresh-context agent that checks one classification
(docs/compat-experiment.md, section 2, last paragraph). One agent per
classification. It receives this prompt with the two inputs filled in and
nothing else. Its first answer binds; it is not asked again.

---

You check one classification of one cell of a compatibility experiment. You
have exactly two inputs, below: the pre-registration (docs/compat-experiment.md)
and the ledger evidence for the cell. Use nothing else. Do not browse, run
commands, or rely on what you know about the chart beyond the evidence. Do not
consider what any answer does to a metric, a region or a decision.

The classification under review names a cause key k, a type (defect or gap) and
a fix. Answer three questions, each from the evidence alone.

1. KEY. Does the evidence show k's mechanism (section 3 table, or for a key
   admitted later, its ledger entry) causing the cell's difference, and does no
   other key's mechanism fit the evidence as well? If two keys fit, rule 3 of
   section 2 decides; apply it as written. If the evidence does not show the
   mechanism, answer DISAGREE.

2. TYPE. Apply section 2 "Type" as written. DEFECT when the evidence shows the
   chart's values reference and no values key in it is specific to k's
   requirement (documented, with the stated effect of meeting it), or when the
   evidence shows the chart or Istio contradicting its own documentation.
   Generic keys do not count as specific: podAnnotations, podLabels,
   extraIngress, extraDeploy, extraManifests, service annotations, and anything
   that disables a component or a security feature. GAP otherwise, including
   when the evidence holds no values reference to decide from.

3. FIX_SCOPE. Does the certified fix change only what k's mechanism names
   (section 2, rule 2)? A values fix: every value set is one the mechanism
   names. A patch: every changed line serves the mechanism, and at most 40 lines
   change. Any other change, or a fix not in the evidence, is DISAGREE.

Answer in exactly this form, one line each, nothing before or after:

    KEY: AGREE | DISAGREE
    KEY_REASON: <one sentence, citing the evidence item by its label>
    TYPE: DEFECT | GAP
    TYPE_REASON: <one sentence: the specific values key found, or that none is, with the line of the values reference you read; or the contradiction>
    FIX_SCOPE: AGREE | DISAGREE
    FIX_SCOPE_REASON: <one sentence naming any change outside the mechanism, or "none">
    EVIDENCE_MISSING: <"none", or the item that would have decided a DISAGREE or a GAP>

TYPE is your own determination, not agreement with the claimed type; the
claimed and the checked type are compared afterwards by the rule in section 2.

## Pre-registration

{{docs/compat-experiment.md at the freeze commit}}

## Ledger evidence

{{the ledger row of the cell: chart, version, world, observation key, claimed
key, claimed type, fix (values or patch file), certificate runs, records,
values reference excerpt, documentation excerpts}}
