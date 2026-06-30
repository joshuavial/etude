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
- `etude run <workflow>` — drive a workflow's stage graph LIVE: resolve each
  stage's runner, invoke the external-runner contract, chain output→input
  roles, execute per-stage gates, and capture each stage incrementally so the
  run is a byproduct of execution. `--resume <id>` continues a partial run from
  its frontier; `--task`/`--run-id`/`--git-sha`/`--runner`/`--timeout` flags.
- `etude run list` / `etude run show` — list all stored runs; inspect the
  detail (including gate records) of one run; `run show` works mid-run.
- `etude sync` — push and fetch `refs/etude/*` with a git remote.
- `etude replay` — `etude replay <run> <stage>` re-executes one recorded stage;
  `etude replay <run>` (no stage) FORWARD-replays all stages in order from the
  captured artifacts. `--allow-env` opts a replay into the workflow's env
  allowlist (hermetic by default).
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

**Live execution** (the `etude-2pc` epic — drive and gate arbitrary workflows
live, not just replay)

- Live workflow orchestration — `etude run` walks an arbitrary stage graph in a
  single evolving worktree, auto-generates a sortable run id, captures each
  stage by compare-and-swap on the run ref (valid partial run on crash), and is
  resumable from the frontier. Replay is redefined as forward replay.
- Live gate execution — a stage's `gate` runs deterministic **checks** (any
  failure hard-blocks) and weighted model **seats**, synthesizes a fail-closed
  `pass | rerun | escalated` verdict, re-runs the guarded stage with feedback on
  `rerun`, climbs the tier ladder on `escalated`, and records each attempt as a
  `GateAttempt` automatically.
- Workflow schema — optional per-stage `runner` and `gate` blocks, a run-level
  `default_runner`, and a workflow-level `env_allowlist`, all additive and
  byte-stable round-tripping for legacy files.
- Shared seat/runner registry — `.etude/registry.yaml` defines named seats,
  tier presets (L1–L4), and quorum, referenced by stage runners, gate seats,
  and the `etude-review` skill alike.
- Scoped secret/env passthrough — live runners receive only an operator-declared
  env allowlist (hermetic by default; replay opt-in); the passed NAMES (never
  values) are recorded in the run manifest for audit.

**Configuration**

- The dev workflow is migrated to the new schema; the former `.etude/gates.yaml`
  is retired — its seats/tiers/quorum move to `.etude/registry.yaml` and its
  per-phase gate bindings move onto the workflow stages. `etude init` scaffolds
  `registry.yaml`.

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
