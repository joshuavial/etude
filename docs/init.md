# etude init

## Overview

`etude init` scaffolds the `.etude/` configuration directory in the current
repository and registers the `refs/etude/*` refspecs on the named git remote
so that a future `etude sync` can push and fetch the namespace.

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

## Notes

The `.etude/` directory is not gitignored — config files belong on main where
they can be reviewed alongside code. Rubric placeholders under `evals/` are
minimal stubs; replace the `TODO` line with your actual evaluation criteria.
