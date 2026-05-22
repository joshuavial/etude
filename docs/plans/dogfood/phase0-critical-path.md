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
- Run manifest: writer plus reader/parser (`ParseJSON`, `ArtifactPaths`).
- Manual `capture` command (first user-facing capture path).

## Recommended Sequence

1. `etude-workflow-schema`
   - Defines and validates `.etude/workflow.yaml`.
   - Unblocks `etude init`.

2. `etude-init-command`
   - Creates initial workflow config and prepares repo-level etude settings.
   - Should document only behavior that actually works.

3. `etude-run-show-list`
   - Makes captured runs inspectable.

4. `etude-sync-command`
   - Pushes/fetches the custom ref namespace once there is useful captured
     data to move between clones.

## Schema And Storage Beads Define Read And Write Together

A serialized format is not a finished contract until both sides exist. The run
manifest shipped writer-only, then the capture bead had to bolt on the parser
and `refstore.ReadCommitFile` — so "manifest done" overstated reality and the
read path surfaced implicitly in a downstream consumer.

For any bead that introduces a serialized format (workflow schema, manifest,
artifact, ref, or eval records):

- define parse/validate alongside write/serialize in the same bead, or
- explicitly defer the reader to a named follow-up bead in the design.

Do not let the missing half surface implicitly when a later bead needs it.
`etude-workflow-schema` is the immediate case: ship both validation (read) and
whatever serialization init relies on, or name the split.

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
