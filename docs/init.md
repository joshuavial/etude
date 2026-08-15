# etude init

## Overview

`etude init` scaffolds the `.etude/` configuration directory in the current
repository and registers the `refs/etude/*` **push** refspec on the named git
remote. Transferring the namespace is the job of [`etude sync`](sync.md), which
passes its own refspecs on the command line and works whether or not `init`
configured the remote.

`init` deliberately does **not** register a *fetch* refspec for the namespace,
and removes one left behind by an older version — see
[Why there is no fetch refspec](#why-there-is-no-fetch-refspec).

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

By default init configures `origin` with exactly one refspec:

```
remote.origin.push = refs/etude/*:refs/etude/*
```

The push refspec is non-forced: a non-fast-forward push fails loudly rather
than silently overwriting a remote ref.

If `origin` does not exist, the refspec step is skipped and init still succeeds
(useful when initializing a repo before the remote is added). Use `--remote` to
target a different remote.

### Why there is no fetch refspec

A fetch refspec whose *destination* is the local `refs/etude/*` namespace —
`+refs/etude/*:refs/etude/*`, which `etude init` used to add — makes every local
run ref a remote-tracking ref as far as git is concerned. Per `git-fetch(1)`,
refs fetched due to an explicit configured refspec **are subject to pruning**.
So a single

```bash
git fetch --prune
```

anywhere in the repository deletes **every run ref that has not yet been pushed**
to the remote.

That is not hypothetical. It destroyed three recorded gate attempts during one
epic, and it fires from ordinary tooling — `workmux remove --gone`, for example,
runs `git fetch --prune` automatically. In a repository with linked worktrees,
every worktree shares one ref store, so one lane running an unrelated command
takes every other lane's unpushed run refs with it. The failure is silent:
`etude capture` treats a missing run ref as "create", so the next capture
starts a fresh manifest and reports success.

Nothing needed the fetch refspec: `etude sync` passes
`refs/etude/*:refs/etude/*` explicitly on its own `git fetch` command line, so
syncing is unaffected. Removing it costs nothing and closes the failure.

The push refspec is **not** affected and must stay: it is what makes
`git push origin` carry run refs at all. Pushing cannot delete a local ref.

### Migrating an existing repository

Any repository initialised by an older `etude init` still carries the dangerous
refspec and will not fix itself. Re-run init — it removes it, on both the normal
and `--force` paths:

```bash
etude init                      # repairs the DEFAULT remote (origin)
etude init --remote upstream    # repairs a different remote
```

**Name the same remote the old init was pointed at.** `init` configures exactly
one remote, so a plain `etude init` repairs `origin` and leaves a hazardous
`remote.upstream.fetch` in place — it will *warn* you about that remote, but it
will not edit it. If you ever ran `etude init --remote <name>`, re-run it with
that same `--remote`. To check which remotes are affected yourself:

```bash
for r in $(git remote); do
  echo "== $r"; git config --local --get-all "remote.$r.fetch"
done
```

Any entry whose part *after* the colon begins with `refs/etude/` is the hazard.
(An entry with no colon at all is harmless — git fetches it to `FETCH_HEAD`
without creating a local ref, so there is nothing for `--prune` to delete.)

Or fix it by hand, without running init. List the fetch refspecs, then unset the
one whose destination is inside `refs/etude/`:

```bash
git config --local --get-all remote.origin.fetch
git config --local --unset-all remote.origin.fetch '^\+refs/etude/\*:refs/etude/\*$'
git config --local --get-all remote.origin.fetch   # verify: no refs/etude entry
git config --local --get-all remote.origin.push    # verify: still present
```

The value is a POSIX regex, so the `+` and `*` are escaped and the pattern is
anchored — that removes exactly that entry and cannot touch your `refs/heads/*`
refspec. Adjust the pattern if your entry is spelled differently (for example
without the leading `+`); `git config --local --get-all remote.origin.fetch`
above shows you the exact value to escape.

Unset only the **fetch** refspec. Unsetting the push refspec too would stop run
refs reaching the remote, which loses the same data by another route.

### Safety warnings

Every `init` run ends by checking that the repository is actually in the safe
state, and prints a `warning:` line when it is not. `init` configures one
remote, so these catch what it cannot fix itself:

- an etude-registered fetch refspec still present on the target remote;
- the target remote not carrying the canonical `refs/etude/*:refs/etude/*` push
  refspec, so run refs never reach it;
- the target remote not existing at all, so run refs stay local-only;
- an etude-registered fetch refspec on **any other remote**. init configures the
  one remote it was pointed at, so a hazardous entry on a sibling remote survives
  the run, and `git fetch --prune <that remote>` deletes unpushed run refs just
  the same. Those are reported but never edited — `--remote` named one remote,
  and silently changing another's configuration is not what was asked for. Re-run
  `etude init --remote <name>` against each one to repair it.

Warnings do not fail the command and are not counted in the summary. They name
the condition and point here; they deliberately **do not embed a runnable
command**. Emitting a remediation that is correct in every state a setup command
can observe turned out to be its own source of bugs — a placeholder URL git
accepts, a preview that dropped the `--remote` selection, a quoted command that
would not parse when pasted. Producing exact remediation is the job of
`etude doctor`, a read-only health check where the remediation string is the
deliverable rather than a by-product.

These checks are deliberately narrow, and they compare **exactly**. Detecting an
etude-registered fetch refspec on another remote needs no more than that same
exact check applied to another config key, which is why it is here. But whether
some *other* refspec is equivalent to the canonical one, whether a refspec
broader than the namespace (`+refs/*:refs/*`) also prunes it, whether a mapping
preserves ref names — those need a full model of refspec semantics, and
answering them confidently but wrongly is worse than not answering. That is the
job of `etude doctor`, a read-only health check tracked separately; `init` does
not guess.

In particular a refspec broader than `refs/etude/*` **is** dangerous and init
neither removes nor reports it. It is also your own configuration — deleting it
would break your branch fetching — so it is not a setup command's call to make.

## Idempotency

Running `etude init` twice is safe:

- Existing files are skipped (reported as `skipped <path>`). Use `--force` to
  regenerate them from the canonical default.
- The push refspec is added at most once. Running init twice results in exactly
  one entry for the key.
- Removing a fetch refspec into `refs/etude/*` is idempotent: the second run
  finds none and says nothing.

Refspec idempotency is byte-exact: init compares the full refspec string
character-for-character against every existing value for the config key. If
exactly one canonical entry already exists, init leaves it alone and prints
`already configured <key> = <value>`.

Otherwise it writes with `git config --replace-all` against a pattern matching
exactly the canonical value. That has two consequences worth knowing:

- **Concurrent runs converge.** Two `etude init` processes racing on the same
  repository — normal when linked worktrees share one `.git/config` — cannot
  leave duplicate entries, because `--replace-all` collapses every matching line
  to one and adds when none match.
- **Pre-existing duplicates are collapsed.** A repository carrying two identical
  canonical entries, left by an older version of this command, ends up with one.

A refspec hand-edited to a *non-identical* variant (e.g. without the forced-fetch
`+` prefix, or with a trailing space) is a different value: init adds the
canonical one alongside it rather than rewriting your edit. Init does not attempt
to detect or merge semantically equivalent refspecs — see `etude doctor`.

## Plan → apply pipeline

`etude init` runs a plan → apply pipeline. `plan` derives an ordered action
list (read-only: no writes, no git config queries). `apply` executes each
action and is the sole site that prints output and tallies counts.

After all actions run, a summary line is printed:

```
init: 4 created, 0 skipped, 1 configured
```

The `configured` count covers freshly configured, already-configured, and
removed refspecs. A second `init` run on a repo that needed no removal reports
`1 configured` (idempotent, already-configured entries fall into the same
bucket). `warning:` lines are not counted.

## --dry-run

`--dry-run` previews the planned actions without writing any files or modifying
git config. It prints `plan: create <path>` / `plan: skip <path>` lines for the
scaffold, `plan: configure push refspec on <remote>` for the push refspec, and
`plan: remove <key> = <value>` for any fetch refspec into `refs/etude/*` it
would remove, followed by a summary:

```
dry-run: 4 to create, 0 to skip, 1 to configure
```

Dry-run behavior:
- **Never errors on a missing remote.** It reports a would-skip note and exits
  with code 0. Use this to preview what `init` would do before a remote is added.
- **Syntactic `--remote` validation still runs.** A malformed name (e.g. `--remote
  "bad name"`) errors immediately, before any reads.
- **Workflow self-check still runs.** The YAML round-trip validation runs during
  plan (read-only) and can error under dry-run.
- **`--force --dry-run`** previews 0 to configure, *except* for a fetch refspec
  removal, which is previewed on the force path too.
- **Safety warnings are printed under `--dry-run`** — the check is read-only and
  runs on every path. Its *output* is not identical between a preview and a real
  run, and cannot be: a real run removes the hazardous fetch refspec before the
  check reads the config, so the entry is reported only under `--dry-run` — and
  there with preview wording that points at `etude init --remote <name>` rather
  than telling you to unset it by hand.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | false | Preview the planned actions without writing files or modifying git config. |
| `--force` | false | Overwrite existing scaffolded files with freshly generated content. Silent on refspec configuration, with one exception: it still removes a fetch refspec into `refs/etude/*`, because leaving a known data-loss setting in place is never the right outcome of a setup command. It does **not** add the push refspec — if it is missing, `--force` reports it and a plain `etude init` adds it. |
| `--remote <name>` | `origin` | Git remote to configure refspecs on. Passing an explicit name for a missing remote is an error (even under `--force`). |

## Example

```bash
# Scaffold a new repository:
etude init

# Inspect what was created:
cat .etude/workflow.yaml
git config --local --get-all remote.origin.push
# Should print no refs/etude entry:
git config --local --get-all remote.origin.fetch

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
    invocation_fallbacks:   # optional; ordered alternate harness commands
      - harness: agy
        invoke: "agy --model opus --print"
        mode: inline
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
  `mode`, `model_fallbacks`, and `invocation_fallbacks` are optional. When set,
  `mode` must match exactly `inline`, `diff-only`, or `inline-no-tools`. Each
  invocation fallback requires its own `harness` and `invoke`; `mode` is
  optional and uses the same closed set. Seat and tier map keys must match
  `[A-Za-z0-9_.-]`.
- **`tiers`** — `seats` is required and must be non-empty.  Every seat key in
  a tier must reference a seat defined in the same file (intra-file check; no
  cross-file resolution at schema time).  `name` and `use` are optional prose.
  The scaffold ships four tier presets, `L1`–`L4`.
- **Unknown fields** are rejected at parse time (strict mode).
- **Trailing documents** after the first are rejected.

`model_fallbacks` lists alternate model identifiers for the primary harness.
It cannot change harnesses. `invocation_fallbacks` lists complete alternate
harness commands; consumers must decide when to retry and try them in order
after the primary invocation. A fallback that omits `mode` inherits the primary
seat's mode. The live-run engine does not infer the surrounding orchestrator
from environment variables or automatically select a fallback.

The generated registry comes from canonical defaults that are tested for
semantic equality with this repository's checked-in `.etude/registry.yaml`.
When that scaffold is regenerated, `etude init --force` may remove its comments
but preserves its canonical machine-readable invocation and fallback behavior.
Custom registry settings are still overwritten by `--force`.

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
