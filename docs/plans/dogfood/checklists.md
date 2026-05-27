# Dogfood Checklists

This is an INDEX of the high-frequency dogfood operations — commands and links
to the authoritative rationale. It is NOT a second copy of the rules; each step
points at the command or the runbook section that owns it. The long runbook and
capture protocol keep the rationale.

---

## Session Boot

Source of truth: [README.md — Session Boot](README.md#session-boot).

1. `bd prime` — boot context.
2. `bd ready` — pick the next unblocked bead.
3. `etude run list` / `etude run show <run-id>` — recover what shipped and how
   it was reviewed from `refs/etude/runs/*`.
4. Read the [Dogfood Plans index](README.md), the
   [Review Gate Runbook](review-gate-runbook.md), and the
   [Verify Phase Design](verify-phase-design.md).
5. Confirm phase labels on the claimed bead — see
   [Checklist — At the start of a bead](capture-protocol.md#checklist).
6. Record the starting git SHA and dirty state — see
   [Capture Envelope](capture-protocol.md#capture-envelope).
7. Work the bead through `plan → implement → verify → docs → final-review`.
8. At each phase gate run the [Gate Execution](#gate-execution) checklist below;
   at close run the [Close](#close) checklist below.

---

## Gate Execution

Source of truth: [Review Gate Runbook](review-gate-runbook.md).

1. Pick the tier (Tier 1 full / Tier 2 two-seat / Tier 3 single) — see
   [Gate Weight](review-gate-runbook.md#gate-weight). Highest-risk surface
   determines the tier; when unsure go heavier; escalate to Tier 1 if shipped
   behavior, schema, or storage is in scope.
2. Apply all four review lenses per seat — see
   [Reviewer Roles (review lenses)](review-gate-runbook.md#reviewer-roles-review-lenses).
   Do not restate the lenses here; that section is the authoritative source.
3. Collect the exact current artifacts for the prompt — see
   [Gate Inputs](review-gate-runbook.md#gate-inputs).
4. Dispatch seats with the per-seat invocation pattern — see
   [Invocation](review-gate-runbook.md#invocation). Snapshot changed files to
   `/tmp` with `cp --parents` before dispatch.
5. Classify the result; empirically reproduce disputed BLOCKs — see
   [Result Classification](review-gate-runbook.md#result-classification).
6. Record the prose gate note — see
   [Recording Results](review-gate-runbook.md#recording-results).
7. Capture the structured gate record:
   `scripts/dogfood-gate-capture.sh <bead-id> <gate.json>` — see
   [Structured capture (`etude capture-gate`)](review-gate-runbook.md#structured-capture-etude-capture-gate).
   The structured record is the durable source of truth; do not skip this step.

---

## Close

Source of truth: [Capture Protocol — Checklist](capture-protocol.md#checklist)
and [Dogfood Close Gate](capture-protocol.md#dogfood-close-gate).

1. Run the terminal close command (captures run + gate records + completeness
   audit). The bead is **not complete** until it exits 0:
   ```
   scripts/dogfood-close.sh <bead-id> <commit-sha> <verify-file> <review-file> [gate-dir]
   ```
   See [Dogfood Close Gate](capture-protocol.md#dogfood-close-gate) for the
   full contract.
2. `git status` — confirm the working tree.
3. Commit and `git push` repository changes. The pre-push hook is the hard
   backstop — see
   [`.beads/hooks/pre-push` enforcement](capture-protocol.md#beadshookspre-push-enforcement).
4. `bd dolt push` — push bead storage.
5. `make dogfood-audit` — batch completeness sweep (`--last 9`). Must be clean
   before a code push can land.
6. Exceptional / non-code bead: add an entry to
   `scripts/dogfood-completeness-allow.txt` with a written reason — see
   [Allowlist](capture-protocol.md#allowlist).
