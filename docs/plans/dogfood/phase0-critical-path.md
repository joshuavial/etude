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
- Workflow schema: parse + validate `.etude/workflow.yaml` with read+write
  (`internal/workflow`: `ParseYAML`, `Validate`, `YAML`, `Default`).
- `etude init` command: scaffolds `.etude/workflow.yaml` + eval rubric
  placeholders and registers `refs/etude/*` fetch/push refspecs on a git remote.
- `etude run list` / `etude run show` commands: inspect stored runs by walking
  `refs/etude/runs/*` directly (no query index yet).
- `etude sync` command: non-forced porcelain push/fetch of the `refs/etude/*`
  namespace to/from a git remote, with ancestry-based fetch classification and
  push divergence detection (never moves a local ref backward or clobbers a
  remote ref).

## Recommended Sequence

The Phase 0 product path is complete: workflow declaration, capture, run
inspection, and `refs/etude/*` sync all exist. The minimal git-native capture
loop is closed end to end. Remaining work is hardening (`etude-ccj`) and polish
(`etude-cr2`, `etude-88o`) taken opportunistically, plus the deferred follow-ups
(`etude-dpz`, `etude-zcq`); net-new product scope belongs to Phase 1.

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
`etude-workflow-schema` followed this rule — it shipped `ParseYAML`/`Validate`
(read) and `YAML`/`Default` (write) together so `etude-init-command` can
scaffold and re-validate. Apply the same discipline to any future eval/record
format.

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
