# Plans

This section holds planning and design notes — some describe components that now
ship, others sketch work that is not built yet. It is not shipped user-facing
documentation (that lives in the top-level [docs/](../README.md)).

The source of truth for the current product direction is still the full
[design brief](product/BRIEF.md). Use this section for smaller implementation
notes, component sketches, sequencing notes, and open design decisions.

## Product Plans

- [Product plan index](product/README.md) - product-facing plans and feature
  design notes.
- [Design brief](product/BRIEF.md) - current product direction and phased plan.
- [Retrospectives](product/retrospectives.md) - product planning for first-class
  retro artifacts, triggers, CLI shape, and manifest integration.
- [etude retro command](product/etude-retro-command.md) - concrete design for the
  `etude retro` CLI and `refs/etude/retros/*` storage model, reconciled with the
  shipped capture/run/eval/bench surfaces.

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
- [Wide retro analysis](dogfood/wide-retro-analysis.md) - cross-retro
  scratchpad and phase plan for enforcing dogfood completeness.

## Components

**Shipped** (see the top-level [docs/](../README.md) for user docs):

- **Go CLI** - command structure, errors, and generated command docs.
- **Workflow schema** - `.etude/workflow.yaml` validation and versioning.
- **Git ref store** - immutable run commits under `refs/etude/*`.
- **Capture** - manual capture (`etude capture`, `capture-run`, `capture-gate`).
- **Run manifests** - JSON manifests tying stages to artifacts, repo SHAs, and
  skill versions.
- **Artifact store** - content-addressed inline blobs.
- **Sync** - explicit push/fetch for the `refs/etude/*` namespace.
- **Replay** - throwaway worktree checkout plus skill-runner adapter.
- **Eval** - rubric, pairwise, and deterministic assertion evaluators (a library
  consumed by `bench`).
- **Bench** - batch replay plus eval to report skill-version win rates.
- **Garbage collection** - `etude gc` storage report and explicit ref pruning.
- **Query index** - rebuildable SQLite cache built by `etude reindex`.
- **Retro** - `etude retro capture/generate/list/show` over `refs/etude/retros/*`.

**Still planned / not yet built:**

- **Capture adapters** - live xenota capture and GitHub PR import (Phase 1); the
  shipped capture is manual/spec-driven only.
- **Import** - backfill runs from GitHub PR history where enough source data
  exists (`etude import --from-github`).
- **Query command** - a CLI that consumes the index; today only `reindex` builds
  it, nothing queries it.
- **Standalone `etude eval`** - the eval package is wired only through `bench`.
- **External artifact pointers** - out-of-tree references for large binary
  artifacts (the content-addressed store is inline-only today).
- **Documentation site** - likely Hugo, with generated CLI reference docs from
  the Go command tree.

## Notes policy

- Write plans in present-tense engineering language, but make it clear when a
  feature is not implemented.
- Prefer one note per component once details grow beyond this index.
- Keep shipped user documentation outside this section.
