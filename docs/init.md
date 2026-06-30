# etude init

## Overview

`etude init` scaffolds the `.etude/` configuration directory in the current
repository and registers the `refs/etude/*` refspecs on the named git remote so
that a plain `git fetch` also picks up the namespace. Transferring the namespace
explicitly is the job of [`etude sync`](sync.md), which passes its own refspecs
and works whether or not `init` configured the remote.

## What it creates

```
.etude/
  workflow.yaml           # canonical 5-stage default workflow
  registry.yaml           # seat/tier registry (edit to configure reviewers)
  evals/
    plan-rubric.md        # rubric placeholder for the plan stage
    verify-rubric.md      # rubric placeholder for the verify stage
```

All files are written to the working tree for normal review and commit on main.
`etude init` never writes to `refs/etude/*` and never auto-commits.

## Refspec configuration

By default init configures `origin` with:

```
remote.origin.fetch = +refs/etude/*:refs/etude/*
remote.origin.push  = refs/etude/*:refs/etude/*
```

The fetch refspec is forced (`+`) so fast-forwards are never required when
pulling run refs. The push refspec is non-forced: a non-fast-forward push fails
loudly rather than silently overwriting a remote ref.

If `origin` does not exist, the refspec step is skipped and init still succeeds
(useful when initializing a repo before the remote is added). Use `--remote` to
target a different remote.

## Idempotency

Running `etude init` twice is safe:

- Existing files are skipped (reported as `skipped <path>`). Use `--force` to
  regenerate them from the canonical default.
- Each refspec is added at most once. Running init twice results in exactly one
  entry per key.

Refspec idempotency is byte-exact: init compares the full refspec string
character-for-character against every existing value for the config key. If the
canonical string already appears, init skips the add and prints
`already configured <key> = <value>`. If a user has hand-edited the refspec to
a non-identical variant (e.g. removing the forced-fetch `+` prefix, or adding a
trailing space), init treats it as a different value and adds the canonical one
alongside it, resulting in two entries for the same key. Init does not attempt
to detect or merge semantically equivalent refspecs.

## Plan → apply pipeline

`etude init` runs a plan → apply pipeline. `plan` derives an ordered action
list (read-only: no writes, no git config queries). `apply` executes each
action and is the sole site that prints output and tallies counts.

After all actions run, a summary line is printed:

```
init: 4 created, 0 skipped, 2 configured
```

The `configured` count covers both freshly configured and already-configured
refspecs. A second `init` run reports `2 configured` (idempotent, already-configured
entries fall into the same bucket).

## --dry-run

`--dry-run` previews the planned actions without writing any files or modifying
git config. It prints `plan: create <path>` / `plan: skip <path>` lines for the
scaffold and `plan: configure fetch refspec on <remote>` / `plan: configure push
refspec on <remote>` lines for the refspecs, followed by a summary:

```
dry-run: 4 to create, 0 to skip, 2 to configure
```

Dry-run behavior:
- **Never errors on a missing remote.** It reports a would-skip note and exits
  with code 0. Use this to preview what `init` would do before a remote is added.
- **Syntactic `--remote` validation still runs.** A malformed name (e.g. `--remote
  "bad name"`) errors immediately, before any reads.
- **Workflow self-check still runs.** The YAML round-trip validation runs during
  plan (read-only) and can error under dry-run.
- **`--force --dry-run`** previews 0 to configure (force is silent on refspecs).

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | false | Preview the planned actions without writing files or modifying git config. |
| `--force` | false | Overwrite existing scaffolded files with freshly generated content. Does not modify git config. `--force` is always silent on refspec configuration. |
| `--remote <name>` | `origin` | Git remote to configure refspecs on. Passing an explicit name for a missing remote is an error (even under `--force`). |

## Example

```bash
# Scaffold a new repository:
etude init

# Inspect what was created:
cat .etude/workflow.yaml
git config --local --get-all remote.origin.fetch
git config --local --get-all remote.origin.push

# Regenerate config files after editing workflow.go upstream:
etude init --force

# Use a different remote:
etude init --remote upstream
```

## workflow.yaml — retros: block

The scaffolded `workflow.yaml` does **not** include a `retros:` block; omitting
it is valid and preserves legacy behavior.  When you add one, it enables
advisory (non-gating) retro triggers that tooling or agents can observe to
decide when to call `retro capture` (manual) or `retro generate` (automated).
Triggers are **never** a precondition for advancing a workflow phase.

```yaml
retros:
  on_run_close: true            # default ON (also the default when block is absent)
  on_repeated_gate_block:
    enabled: false              # default OFF
    threshold: 3                # default 3; must be >= 1 when trigger enabled
  on_failed_verify: false       # default OFF
  on_blocked_state: false       # default OFF
  post_bench: false             # default OFF
  generator: ./retro.sh         # required when any automated trigger is effectively enabled
```

Defaults and rules:

- **`on_run_close`** — true by default regardless of whether the block is present.
  Explicitly set `on_run_close: false` (plus all others off) to opt out entirely
  and suppress the generator requirement.
- **`generator`** — required when at least one trigger is effectively enabled
  (including the `on_run_close` default).  Writing a `retros:` block without a
  generator and without explicitly disabling all triggers is a validation error.
- **Absent block** — omitting `retros:` entirely (legacy / `Default()`) is
  always valid; no generator is required and no retros validation runs.
- **Automated firing** — auto-firing is not yet wired; this block is parsed and
  validated only.  See `docs/plans/product/etude-retro-command.md §4` for the
  full trigger table and Phase C roadmap.

## registry.yaml — seat and tier configuration

The scaffolded `registry.yaml` defines the named seats (model + harness
invocations) and tier presets that live-execution gate blocks reference.

```yaml
quorum: unanimous          # optional; "unanimous" (default) or "majority"

seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: "claude -p --model opus"
    mode: inline            # optional; execution constraint for the seat
    model_fallbacks:        # optional; ordered list of fallback model ids
      - claude-opus-old
  codex:
    provider: openai/gpt-5.5
    harness: codex
    invoke: "codex exec --ephemeral -m gpt-5.5 -s read-only -"
    mode: diff-only

tiers:
  L1:
    name: Full three-seat gate   # optional human-readable label
    seats: [gemini, opus, codex] # required; every entry must resolve to a seat
    use: "Reserve for the riskiest changes."  # optional prose
  L2:
    seats: [opus, codex]
```

Validation rules:

- **`quorum`** — if set, must be `"unanimous"` or `"majority"`.  Omitting it
  is equivalent to `"unanimous"`.
- **`seats`** — `provider`, `harness`, and `invoke` are required per seat.
  `mode` and `model_fallbacks` are optional.  Seat and tier map keys must match
  `[A-Za-z0-9_.-]`.
- **`tiers`** — `seats` is required and must be non-empty.  Every seat key in
  a tier must reference a seat defined in the same file (intra-file check; no
  cross-file resolution at schema time).  `name` and `use` are optional prose.
  The scaffold ships four tier presets, `L1`–`L4`.
- **Unknown fields** are rejected at parse time (strict mode).
- **Trailing documents** after the first are rejected.

## workflow.yaml — optional stage runner, gate, and default_runner fields

These fields are additive; existing `skill`-based workflows remain valid
without them.

```yaml
name: my-workflow

default_runner:            # optional; applied to stages that have no own runner
  name: opus               # registry seat reference  OR  command: "make run"

stages:
  - name: implement
    produces: diff
    inputs: [task, repo-state]
    skill: dev-executor
    runner:                # optional; overrides default_runner for this stage
      name: opus           # -- OR --
      # command: "make implement"  (name and command are mutually exclusive)
    gate:                  # optional review gate for this stage's output
      checks:              # deterministic hard-veto runners (optional)
        - command: make test
        - command: make lint
      seats: [opus, codex] # inline seat list  -- OR --
      # tier: L2           # tier preset (mutually exclusive with seats)
      pass_threshold: 1.0  # 0 < t <= 1; default 1.0
      max_rounds: 3        # >= 1; default 3
      abstraction: "review code correctness against the approved plan"
```

Runner and gate validation rules:

- **`runner`** — exactly one of `name` or `command` must be set; both empty or
  both set is an error.  A bare `runner:` key (null value) is treated as
  present and fails validation.
- **`gate`** — at least one of `checks` (non-empty), `seats`, or `tier` must
  be set; `checks: []` (explicit empty list) is treated as unset.  `seats` and
  `tier` are mutually exclusive.  `abstraction` is free prose; no constraint.
- **`default_runner`** — same rules as per-stage `runner`.

Cross-file reference resolution (e.g. verifying that `runner.name: opus`
exists in `registry.yaml`) is deferred to execution-time; the schema layer
validates intra-file structure only.

Live execution — runner invocation and gate evaluation are not yet wired;
these fields are parsed and validated only.  See
`docs/plans/product/live-execution.md` for the execution roadmap.

## Notes

The `.etude/` directory is not gitignored — config files belong on main where
they can be reviewed alongside code. Rubric placeholders under `evals/` are
minimal stubs; replace the `TODO` line with your actual evaluation criteria.
