# Phase 0 Critical Path

Status: dogfood process note. This is the current operating path for Phase 0,
not a shipped product guarantee.

Phase 0 aims to create a minimal, git-native capture loop:

1. declare a workflow,
2. capture artifacts and run metadata,
3. inspect captured runs,
4. sync `refs/etude/*`.

## Completed Foundations

- Go CLI scaffold.
- Internal `refs/etude/*` Git ref storage.
- Internal content-addressed artifact storage with pointer records.

## Recommended Sequence

1. `etude-run-manifest`
   - Defines the JSON record that ties workflow, refs, repo SHAs, stages,
     artifacts, skills, and timestamps together.
   - Unblocks manual capture, run list/show, and GitHub import.

2. `etude-workflow-schema`
   - Defines and validates `.etude/workflow.yaml`.
   - Unblocks `etude init`.

3. `etude-init-command`
   - Creates initial workflow config and prepares repo-level etude settings.
   - Should document only behavior that actually works.

4. `etude-capture-manual`
   - First user-facing capture path.
   - This is where user-facing docs outside planning notes should start
     growing materially.

5. `etude-run-show-list`
   - Makes captured runs inspectable.

6. `etude-sync-command`
   - Pushes/fetches the custom ref namespace once there is useful captured
     data to move between clones.

## Hardening Along The Way

Work `etude-ccj` before a product bead if that bead depends on the refstore
edge being hardened.

Work `etude-cr2` opportunistically only if type-safety polish is cheap and does
not interrupt the Phase 0 path.

## Out Of Scope For Phase 0

- Replay and skill-runner integration.
- Eval and bench.
- Documentation site.
- Garbage collection and query index polish.
