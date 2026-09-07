# Dogfood Plans

These notes describe how this repo is using planned `etude` concepts while the
product is still being built. They are not shipped user-facing behavior.

## Session Boot

Run `bd prime`, then read [Developing Etude](../../development.md). That is the
current operational contract for `dev-claude` and `dev-codex`, including
model delegation, documentation-driven planning, independent QA and proportional
review gates. Verify the actual bead database is accessible, inspect the bead's
mapped run and continue its recorded work.

The documents below are historical design and retrospective material. Their
five-per-phase gate requirements are superseded by the current development
contract. Capture/gate storage contracts remain useful background, but do not
restart the old approval loop.

## Index

- [Supervisor runbook](supervisor-runbook.md) - the worker-lane + supervisor
  model: where a phase artifact lands, the two commands that advance a phase,
  the `etude gate` contract, the seat envelope contract, and stop-the-line.
- [Review gate process](review-gate-process.md) - the gate policy: seats vote,
  humans do not approve, a seat that cannot complete is not a pass.
- [Review gate runbook](review-gate-runbook.md) - how to judge: review lenses,
  gate weight, prompt template, result classification, degraded gate policy, and
  the recurring defect classes each phase gate should look for.
- [Verify phase design](verify-phase-design.md) - public Verify phase contract
  and internal test/QA lane design.
- [Docs freshness checklist](docs-checklist.md) - Docs and Final Review checks
  that keep shipped docs aligned with implemented behavior.
- [Backlog operating model](backlog-operating-model.md) - how to choose next
  beads from the issue graph without confusing epics, product work, and polish.
- [Phase 0 critical path](phase0-critical-path.md) - current default order for
  the core schema, storage, capture, run inspection, and sync work.
- [Writing style guide](writing-style-guide.md) - writing expectations for
  dogfood planning docs and docs verification.
- [Dev workflow audit](dev-workflow-audit.md) - agent workflow gaps and the
  recommended dogfood workflow shape.
- [Dogfood process retro](dogfood-process-retro.md) - retrospective on early
  dogfood workflow issues and process improvements.
- [Retro impact ledger](retro-ledger.md) - inventory of every retro performed
  and the concrete process improvements each produced.
- [Wide retro analysis](wide-retro-analysis.md) - cross-retro scratchpad for the
  completeness failure and the `etude-8hq` enforcement phase.
- [Artifacts](artifacts/) - committed artifacts for external files or large
  outputs referenced from bead notes.

## Scripts

| Script | Purpose |
|--------|---------|
| `scripts/dogfood-gate-capture.sh` | Append a hand-assembled gate attempt to a bead's run and push it. The bootstrap path: used before `etude gate` exists, and when a documented seat exception means a verdict was genuinely produced out-of-band. See [Supervisor runbook — The bootstrap path](supervisor-runbook.md#the-bootstrap-path). |
| `scripts/dogfood-completeness-audit.sh` | Audit whether closed beads have their run refs, gate records, and pushed refs. Run via `make dogfood-audit`. |
| `scripts/docs-reality-check.sh` | Guard against doc/CLI drift (also run via `make docs-reality`). |
| `scripts/backfill-gate-records.sh` | One-time backfill of missing gate records. |
| `scripts/retro-meta-index.sh` | Read-only cross-retro index: aggregates failure modes, root causes, follow-up beads, and durable-changes timeline across all current cadence-retro sidecars. Run via `make retro-index`; `--json` for machine form. Standalone analysis tool — not wired into any gate. |
