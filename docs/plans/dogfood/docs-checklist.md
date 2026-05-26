# Docs Freshness Checklist

Status: dogfood process note. This checklist keeps shipped docs accurate while
most `etude` behavior is still planned.

Use this checklist during the Docs phase and Final Review for every bead.

## Check Shipped Docs

- Review the top-level `README.md`.
- Review `docs/README.md`.
- If a user-facing command changed, review any command guide or generated
  command reference that already exists.
- If only internal Go APIs changed, keep shipped docs limited to current
  implementation status unless there is a real user-facing workflow to explain.

## Check Planning Docs On Close

Shipped-doc checks above keep `README`/`docs/README` accurate. They do not keep
`docs/plans/` notes fresh, which is how planning docs silently go stale. When a
bead closes, also reconcile planning notes that referenced the work it just
landed:

- Search `docs/plans/` for the bead ID and for status language that the close
  invalidates: "next bead", "until X works", "recommended sequence", "current
  bias", "completed foundations", "not yet".
- Move the just-closed work out of any "next" or "pending" list and into the
  completed/foundations section of the relevant note.
- If a planning note's whole premise is now satisfied, mark it superseded or
  delete the stale section rather than leaving misleading guidance.

## Policy Checks

- Shipped docs describe implemented and verified behavior only.
- Planned behavior, future command shapes, design sketches, and open decisions
  remain under `docs/plans/`.
- Shipped docs do not imply CLI support for internal APIs.
- Links from shipped docs to planning docs are explicit that plans are not
  shipped behavior.

## Common Outcomes

- **No shipped-doc change needed**: record the rationale in the Docs artifact.
- **README status update needed**: update the current implementation summary
  without adding usage docs for unexposed internals.
- **Command docs needed**: update docs only after the command works, and keep
  generated command references separate from hand-written guides.
- **Planning docs needed**: add or update files under `docs/plans/`.

## Mechanical reality check

Run `make docs-reality` (the source-built CLI-inventory gate the docs-reality
retro found missing). It builds etude fresh and fails, listing every finding,
when a shipped command is not advertised in `README.md` (as `etude <cmd>` usage)
or named in the `docs/README.md` index, when a shipped command lacks its
`docs/cli/etude_<cmd>.md` page, when a doc names a command that does not exist,
or when a `docs/plans/**` line still calls a SHIPPED command future/unimplemented.
Resolve every finding before close; for genuinely-planned prose (about unshipped
behavior) add a justified suppression to `scripts/docs-reality-allow.txt`. This
is separate from `make docs-check` (which only diffs generated `docs/cli`).

## Epic-Close Reconciliation (MANDATORY)

This section applies at EPIC close, not at individual bead close. It is distinct
from the per-bead `make docs-reality` step in "Mechanical reality check" above.

Before running `bd close <epic>` (or `bd epic close-eligible`), you MUST:

**1. Run `make reconcile` — it MUST exit 0.**

`make reconcile` composes two whole-surface checks in sequence:

- `make docs-reality` — builds etude fresh; verifies every shipped command is
  advertised in `README.md` (as `etude <cmd>` usage), named in the
  `docs/README.md` index, and has its `docs/cli/etude_<cmd>.md` page; no orphan
  docs/cli pages; no `docs/plans/**` line still calls a SHIPPED command
  future/unimplemented.
- `make docs-check` — diffs generated `docs/cli` against source; fails if stale.

If either leg exits non-zero, resolve every finding before closing the epic. For
genuinely-planned prose (about unshipped behavior) add a justified suppression to
`scripts/docs-reality-allow.txt`. Do NOT close the epic until `make reconcile`
exits 0.

**Why re-run at epic close?** Each bead's `make docs-reality` ran against the
whole-surface CLI inventory at merge time, but a later sibling bead could
introduce drift that no earlier bead's pre-merge check saw (e.g. a command
added by a later bead that the earlier bead's docs don't reference). Re-running
at the integration point catches cross-bead drift.

**2. Human holistic read (one step, not mechanical).**

Read `README.md` and `docs/README.md` once end-to-end and confirm:

- The full set of commands the epic shipped reads coherently together.
- `docs/README.md` still explains why user-facing docs are sparse (if that
  context still applies).
- No narrative inconsistencies were introduced across the epic's beads.

Record the epic-close gate result in the epic bead notes (one-line: command
result + commit SHA), consistent with the gate recording convention in the
review-gate runbook "Epic-Close Gate" section.

## Final Review Prompt

Final Review (per-bead) should ask:

- Does `make docs-reality` pass (no doc/CLI drift)? (Per-bead check; for the
  epic-close holistic check run `make reconcile` — see "Epic-Close
  Reconciliation" above.)
- Does `docs/README.md` still explain why user-facing docs are sparse?
- Does `README.md` match the final diff?
- Did any user-facing behavior ship without documentation?
- Did any planned behavior leak into shipped docs?
- Did closing this bead make any planning note stale (next-bead lists,
  "until X works", recommended-sequence or status sections)?
