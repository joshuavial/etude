# Dogfood Plans

These notes describe how this repo is using planned `etude` concepts while the
product is still being built. They are not shipped user-facing behavior.

## Session Boot

After a context clear or when starting a new ticket, boot into productive state:

1. Run `bd prime`.
2. Run `bd ready` and pick the next unblocked bead.
3. Inspect recent dogfood runs with `etude run list` and `etude run show <run-id>`
   (recent closed beads are captured as `refs/etude/runs/*`, including their gate
   reviewer records) to recover what shipped and how it was reviewed — not only `bd`.
4. Read this index.
5. Read [Review gate runbook](review-gate-runbook.md).
6. For workflow shape, read [Verify phase design](verify-phase-design.md).
7. Work the bead through `plan -> implement -> verify -> docs -> final review`.
8. Before advancing any phase gate, run the four-reviewer process from
   [Review gate process](review-gate-process.md).
9. After a gate passes and optional improvements are handled or deferred,
   continue to the next workflow step without waiting for another prompt unless
   blocked.
10. At bead close, run `scripts/dogfood-close.sh <bead> <commit> <verify> <review> [gate-dir]`
    as the terminal step. A bead is not complete until this exits 0.

- [Dev workflow audit](dev-workflow-audit.md) - current agent workflow gaps and
  recommended dogfood workflow shape.
- [Review gate process](review-gate-process.md) - four-reviewer gate process
  for advancing workflow phases.
- [Review gate runbook](review-gate-runbook.md) - operational checklist for
  running the four-reviewer gate.
- [Verify phase design](verify-phase-design.md) - public Verify phase contract
  and internal test/QA lane design.
- [Dogfood capture protocol](capture-protocol.md) - temporary manual capture
  rules for treating one bead as one future `etude` run.
- [Docs freshness checklist](docs-checklist.md) - Docs and Final Review checks
  that keep shipped docs aligned with implemented behavior.
- [Backlog operating model](backlog-operating-model.md) - how to choose next
  beads from the issue graph without confusing epics, product work, and polish.
- [Phase 0 critical path](phase0-critical-path.md) - current default order for
  the core schema, storage, manual capture, run inspection, and sync work.
- [Writing style guide](writing-style-guide.md) - writing expectations for
  dogfood planning docs and docs verification.
- [Dogfood process retro](dogfood-process-retro.md) - retrospective on early
  dogfood workflow issues and process improvements.
- [Retro impact ledger](retro-ledger.md) - inventory of every retro performed
  and the concrete process improvements each produced (the manual stand-in until
  retros become a first-class etude artifact).
- [Wide retro analysis](wide-retro-analysis.md) - cross-retro scratchpad for the
  dogfood completeness failure and the `etude-8hq` enforcement phase.
- [Artifacts](artifacts/) - committed dogfood capture artifacts for external
  files or large outputs that are referenced from bead notes.

## Dogfood Scripts

| Script | Purpose |
|--------|---------|
| `scripts/dogfood-close.sh` | Orchestrate the full bead-close sequence: preflight, capture run, capture gate records, terminal completeness audit. Run this to close a bead. |
| `scripts/dogfood-capture.sh` | Capture a closed bead's dev-workflow phases as an etude run and push the ref. |
| `scripts/dogfood-gate-capture.sh` | Append a structured gate attempt to a bead's run and push. |
| `scripts/dogfood-completeness-audit.sh` | Audit whether closed beads have their run refs, gate records, and pushed refs. |
| `scripts/docs-reality-check.sh` | Guard against doc/CLI drift (also run via `make docs-reality`). |
| `scripts/backfill-gate-records.sh` | One-time backfill of missing gate records. |
