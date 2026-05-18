# Plans

This section is for notes on things `etude` is expected to build, but has not
implemented yet.

The source of truth for the current product direction is still the full
[design brief](BRIEF.md). Use this section for smaller implementation notes,
component sketches, sequencing notes, and open design decisions that should not
yet be treated as shipped documentation.

## Planning notes

- [Dev workflow audit](dev-workflow-audit.md) - audit of the current Claude dev
  workflow and recommended dogfood workflow shape for building `etude`.

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
