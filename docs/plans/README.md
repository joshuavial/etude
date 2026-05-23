# Plans

This section is for notes on things `etude` is expected to build, but has not
implemented yet.

The source of truth for the current product direction is still the full
[design brief](product/BRIEF.md). Use this section for smaller implementation
notes, component sketches, sequencing notes, and open design decisions that
should not yet be treated as shipped documentation.

## Product Plans

- [Product plan index](product/README.md) - product-facing plans and feature
  design notes.
- [Design brief](product/BRIEF.md) - current product direction and phased plan.
- [Retrospectives](product/retrospectives.md) - product planning for first-class
  retro artifacts, triggers, CLI shape, and manifest integration.

## Dogfood Plans

These notes describe how this repo is currently using planned `etude` concepts
while the product is still being built. They are not shipped user-facing
behavior.

- [Dogfood plan index](dogfood/README.md) - dogfood process notes used while
  building `etude`.
- [Dev workflow audit](dogfood/dev-workflow-audit.md) - audit of the current
  Claude dev workflow and recommended dogfood workflow shape for building
  `etude`.
- [Review gate process](dogfood/review-gate-process.md) - four-reviewer gate
  process for advancing dogfood workflow phases without human approval gates.
- [Review gate runbook](dogfood/review-gate-runbook.md) - operational checklist
  for running the four-reviewer gate.
- [Verify phase design](dogfood/verify-phase-design.md) - decision and public
  contract for consolidating test writing, manual testing, and QA under one
  Verify gate.
- [Dogfood capture protocol](dogfood/capture-protocol.md) - temporary manual
  capture rules for treating one bead as one future `etude` run.
- [Docs freshness checklist](dogfood/docs-checklist.md) - shipped-docs checks
  for Docs and Final Review phases.
- [Backlog operating model](dogfood/backlog-operating-model.md) - working rules
  for choosing the next bead from the issue graph.
- [Phase 0 critical path](dogfood/phase0-critical-path.md) - current default
  order for core schema, storage, manual capture, run inspection, and sync.
- [Writing style guide](dogfood/writing-style-guide.md) - writing expectations
  for dogfood planning docs and docs verification.
- [Dogfood process retro](dogfood/dogfood-process-retro.md) - retrospective on
  early dogfood workflow issues and recommended process improvements.

## Planned components

- **Go CLI** - command structure, config loading, errors, and command docs.
- **Workflow schema** - `.etude/workflow.yaml` validation and versioning.
- **Git ref store** - immutable run commits under `refs/etude/*`.
- **Capture** - manual capture first, then adapters for real workflows.
- **Run manifests** - JSON manifests tying stages to artifacts, repo SHAs, and
  skill versions.
- **Artifact store** - content-addressed blobs, with external references for
  large binary artifacts.
- **Sync** - explicit push/fetch support for the `refs/etude/*` namespace.
- **Query index** - rebuildable SQLite cache in `.git/etude-index.db`.
- **Replay** - throwaway worktree checkout plus skill-runner adapter.
- **Eval** - rubric, pairwise, and deterministic assertion evaluators.
- **Bench** - batch replay plus eval to report skill-version win rates.
- **Import** - backfill runs from GitHub PR history where enough source data
  exists.
- **Garbage collection** - cleanup for unreachable or oversized artifacts.
- **Documentation site** - likely Hugo, with generated CLI reference docs from
  the Go command tree.

## Notes policy

- Write plans in present-tense engineering language, but make it clear when a
  feature is not implemented.
- Prefer one note per component once details grow beyond this index.
- Keep shipped user documentation outside this section.
