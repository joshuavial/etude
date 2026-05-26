# Batch Capture

`etude capture-run` captures a complete multi-stage run from a single YAML spec
in one operation.

```bash
etude capture-run <spec.yaml>
```

The spec file describes the run and all its stages. On success the command
prints the commit SHA and the created ref:

```
captured a1b2c3d4...
ref refs/etude/runs/my-run-2026-05-26
```

## Spec schema

All artifact paths in the spec are relative to the spec file's directory.

### Run-level fields

| Field | Required | Default | Description |
|---|---|---|---|
| `run_id` | yes | — | Run identifier (letters, digits, `-`, `_`, `.`; no `/` or `..`) |
| `workflow` | no | `manual` | Workflow name |
| `workflow_version` | no | `manual-v1` | Workflow version |
| `git_sha` | no | current `HEAD` | Commit SHA for all stages; per-stage `git_sha` overrides this |
| `harness` | no | `` (empty) | Harness name (run-level default, overridable per stage) |
| `harness_version` | no | `` (empty) | Harness version |
| `model` | no | `` (empty) | Model identifier (run-level default, overridable per stage) |
| `refs` | no | `{}` | Key/value external references merged into the run manifest |
| `stages` | yes | — | Ordered list of stage specs (at least one required) |

### Stage fields

Each entry in `stages:` describes one stage:

| Field | Required | Default | Description |
|---|---|---|---|
| `stage` | yes | — | Stage name (letters, digits, `-`, `_`, `.`) |
| `skill.id` | yes | — | Skill identifier for this stage |
| `skill.repo` | no | `manual` | Skill repository |
| `skill.version` | no | `manual` | Skill version |
| `produced_by` | no | `original` | Producer label (e.g. `original`, `agent-run`); `replay` is not supported here (it requires a replay source) |
| `git_sha` | no | run-level `git_sha` | Per-stage commit SHA override |
| `harness` | no | run-level `harness` | Per-stage harness override |
| `harness_version` | no | run-level `harness_version` | Per-stage harness version override |
| `model` | no | run-level `model` | Per-stage model override |
| `inputs` | no | `[]` | List of input artifacts (`role` + `path`) |
| `output` | yes | — | Single output artifact (`role` + `path`) |

Each `inputs` entry and the `output` entry have:

| Field | Required | Description |
|---|---|---|
| `role` | yes | Artifact role label |
| `path` | yes | Path relative to the spec file's directory |

## Artifact path confinement

All `path` values must be relative (not absolute) and must not escape the spec
file's directory via `..`. Subdirectory paths like `artifacts/plan.md` are
allowed. This makes a spec and its artifacts a self-contained directory.

Confinement is enforced on the resolved real path, not just lexically: a symlink
inside the spec directory whose target points outside it (via an absolute path,
a `..` target, a chain, or an intermediate symlinked directory) is rejected with
`escapes the spec directory`. A symlink whose target stays inside the spec
directory is allowed. A path that does not exist is reported as
`does not exist` rather than a confinement error.

## Create-only semantics

`capture-run` creates the run ref only if it does not already exist. If
`refs/etude/runs/<run_id>` already exists the command fails with an error. To
capture an additional stage on an existing run, use `etude capture` (which
appends to an existing run ref).

## Example spec

```yaml
run_id: my-run-2026-05-26
workflow: dev
workflow_version: dev-v1
harness: claude-code
harness_version: "1.2.0"
model: claude-sonnet-4-6
refs:
  pr: "123"
stages:
  - stage: plan
    skill:
      id: dev-planner
    inputs:
      - role: task
        path: task.md
    output:
      role: plan
      path: plan.md
  - stage: implement
    model: claude-opus-4-7      # per-stage model override
    skill:
      id: dev-coder
    inputs:
      - role: plan
        path: plan.md
    output:
      role: diff
      path: changes.diff
```

## See also

- [Manual Capture](capture.md) — `etude capture` for single-stage append to an existing run.
- [Gate reviewer records](gates.md) — `etude capture-gate` to append a gate attempt.
- [CLI reference](cli/etude_capture-run.md) — generated flag reference.
