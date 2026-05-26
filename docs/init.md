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
  workflow.yaml           # canonical 6-stage default workflow
  evals/
    plan-rubric.md        # rubric placeholder for the plan stage
    test-plan-rubric.md   # rubric placeholder for the test-plan stage
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

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--force` | false | Overwrite existing scaffolded files with freshly generated content. Does not modify git config. |
| `--remote <name>` | `origin` | Git remote to configure refspecs on. Passing an explicit name for a missing remote is an error. |

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

## Notes

The `.etude/` directory is not gitignored — config files belong on main where
they can be reviewed alongside code. Rubric placeholders under `evals/` are
minimal stubs; replace the `TODO` line with your actual evaluation criteria.
