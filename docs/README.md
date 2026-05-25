# etude docs

This directory holds user-facing documentation for implemented behavior.

## Sections

- [Init](init.md) - scaffold `.etude/` config and register `refs/etude/*` refspecs.
- [Manual Capture](capture.md) - record local file artifacts into a run ref.
- [Runs](run.md) - list and inspect stored runs.
- [Gate reviewer records](gates.md) - capture (`etude capture-gate`) and inspect review-gate attempts (reviewer seats, verdicts, provider/model/harness).
- [Sync](sync.md) - push and fetch `refs/etude/*` with a git remote.
- [Replay](replay.md) - re-execute a recorded stage end-to-end and emit its output.
- [Bench](bench.md) - replay a stage across a cohort and report replay-vs-original win rates.
- [GC](gc.md) - report artifact storage and explicitly prune named run refs.
- [Reindex](reindex.md) - build the derived SQLite query index from all run and eval refs.
- [Example](../examples/summarize/README.md) - tracker-agnostic end-to-end walkthrough (no beads, no LLM, just git + sh + etude).
- [Plans](plans/README.md) - notes on planned components that do not exist yet.
- [CLI reference](cli/etude.md) - generated per-command flag/synopsis reference (do not edit; run `make docs` to regenerate).

The current implemented state is summarized in the top-level
[README](../README.md).

The storage and manifest packages that exist today are Go APIs internal to this
module. The top-level README mentions them as implementation status; user-facing
command docs cover the implemented CLI only.
