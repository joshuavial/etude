# Dogfood Plans

These notes describe how this repo is using planned `etude` concepts while the
product is still being built. They are not shipped user-facing behavior.

## Session Boot

After a context clear or when starting a new ticket, boot into productive state:

1. Run `bd prime`.
2. Run `bd ready` and pick the next unblocked bead.
3. Read this index.
4. Read [Review gate runbook](review-gate-runbook.md).
5. For workflow shape, read [Verify phase design](verify-phase-design.md).
6. Work the bead through `plan -> implement -> verify -> docs -> final review`.
7. Before advancing any phase gate, run the four-reviewer process from
   [Review gate process](review-gate-process.md).
8. After a gate passes and optional improvements are handled or deferred,
   continue to the next workflow step without waiting for another prompt unless
   blocked.

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
- [Artifacts](artifacts/) - committed dogfood capture artifacts for external
  files or large outputs that are referenced from bead notes.
