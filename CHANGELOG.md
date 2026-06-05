# Changelog

All notable changes to this project will be documented in this file.

This file follows the [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)
format. Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The authoritative list of what is in v1 versus deferred is
[`docs/plans/product/V1-SCOPE.md`](docs/plans/product/V1-SCOPE.md).

---

## Versioning

The `etude` binary reports its version via `etude --version`. A plain
`make build` stamps the binary with `dev`; a release build passes the version
explicitly (e.g. `make build VERSION=v1.0.0`). See the
[`## Releasing`](README.md#releasing) section of `README.md` for the full
mechanics.

---

## [v1.0.0] — Unreleased

> v1.0.0 has not been tagged. Cutting the tag is a human release action
> covered by the v1 release checklist (etude-kb0.4).

### Added

**Commands**

- `etude init` — scaffold `.etude/workflow.yaml` and rubric placeholders;
  register the `refs/etude/*` fetch/push refspec on a git remote.
- `etude capture` — record a stage artifact for the current run.
- `etude capture-gate` — append a structured gate-reviewer record to a run.
- `etude capture-run` — record a complete multi-stage run from a single YAML
  spec in one operation.
- `etude run list` / `etude run show` — list all stored runs; inspect the
  detail (including gate records) of one run.
- `etude sync` — push and fetch `refs/etude/*` with a git remote.
- `etude replay` — re-execute a recorded stage end-to-end and emit its output.
- `etude bench` — benchmark a cohort of runs by replaying and judging
  replay-vs-original; `eval` is available as an internal library consumed by
  this command (no standalone `etude eval` command in v1).
- `etude retro capture` / `etude retro generate` / `etude retro list` /
  `etude retro show` — store, generate, list, and inspect retrospectives
  as `refs/etude/retros/*` refs.
- `etude retro nudge dismiss` / `etude retro nudge status` — opt-in
  retro-overdue reminder emitted on stderr by any `etude` command when the
  number of runs since the most recent retro reaches a `retros.nudge`
  threshold in `.etude/workflow.yaml`; `dismiss` snoozes the reminder for
  the next N beads, `status` prints the current decision as JSON.
- `etude gc` — report artifact storage and explicitly prune named run refs.
- `etude reindex` — rebuild the derived SQLite query index from run and eval
  refs.
- `etude prime` — print a structured agent-onboarding primer to stdout (no
  args, no side effects).
- `etude log` — narrate runs and retros as a chronological timeline
  (read-only).

**Storage**

- Git-native `refs/etude/*` ref store — run records written atomically to
  per-run refs with no shared write point.
- Content-addressed inline artifacts — blobs stored by hash inside the ref
  store.

**Run manifests**

- Run manifests v2 and v3 — v3 adds gate-reviewer records; the producer
  record is authoritative.
- Retro-meta sidecars — retrospective metadata stored alongside run records.

---

Deferred / post-v1 items (live xenota capture adapter, `etude import`,
standalone `etude eval`, `query` command, external artifact pointer capture
via the CLI, docs site) are tracked in
[`docs/plans/product/V1-SCOPE.md`](docs/plans/product/V1-SCOPE.md).
