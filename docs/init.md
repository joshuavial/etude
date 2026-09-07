# etude init

## Overview

`etude init` scaffolds the `.etude/` configuration directory and configures
safe fetching of Etude metadata from one Git remote. It does not install a push
refspec. Ordinary `git push` therefore continues to follow the repository's
existing `push.default`, upstream, branch, and remote policy.

Publish and reconcile `refs/etude/*` with [`etude sync`](sync.md). Sync supplies
its own explicit refspecs, independently of configured Git push policy.

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

By default init configures these fetch mappings on `origin`:

```
remote.origin.fetch = +refs/etude/runs/*:refs/etude-mirror/origin/runs/*
remote.origin.fetch = +refs/etude/retros/*:refs/etude-mirror/origin/retros/*
remote.origin.fetch = +refs/etude/evals/*:refs/etude-mirror/origin/evals/*
```

The fetch refspecs are forced because their destinations are disposable mirror
refs. Init does not create `remote.origin.push`. If that key is absent, normal
Git rules decide what `git push` sends. If it contains user mappings, they remain
in effect.

If `origin` does not exist, the refspec step is skipped and init still succeeds
(useful when initializing a repo before the remote is added). Use `--remote` to
target a different remote.

### Local refs and remote mirrors

```
refs/etude/runs/*                     YOURS. Authoritative. Never pruned.
refs/etude/retros/*                   YOURS.
refs/etude/evals/*                    YOURS.
refs/etude-mirror/<remote>/runs/*     That remote's copy. Disposable, prunable.
refs/etude-mirror/<remote>/retros/*   Mirror.
refs/etude-mirror/<remote>/evals/*    Mirror.
```

Fetched refs land in the **mirror**; refs you produce stay in your own namespace
and are never a fetch destination. That asymmetry is what makes
`git fetch --prune` safe: prune deletes refs that the remote no longer has, and
the only refs it can reach are mirror refs, which are re-created by the next
fetch. A run you have produced and not yet pushed is not reachable by prune at
all.

The mirror is a **sibling** of `refs/etude/`, not nested inside it. Etude's
explicit `refs/etude/*:refs/etude/*` sync mapping therefore cannot match the
mirror and upload this clone's copy back to the remote. A user-authored broader
mapping can have different semantics and remains the user's policy.

Two consequences worth knowing:

- **Doctor reads the mirror as a snapshot.** `etude doctor` compares local refs
  with the last-fetched mirror while reporting that the remote may since have
  changed. `etude run show`, `etude log`, `etude gc`, and the index continue to
  read the authoritative local namespace only.
- **Mirror refs pin objects.** A plain `git fetch` now downloads the remote's
  etude objects and the mirror refs keep them reachable, so they are not
  reclaimed by `git gc`. There is no expiry for the mirror yet, including after a
  remote is renamed or removed; `refs/etude-mirror/<name>/` for a remote that no
  longer exists will linger. Delete such a namespace by hand if it matters:
  `git for-each-ref --format='%(refname)' refs/etude-mirror/<name>/ | xargs -r -n1 git update-ref -d`

### Why the fetch refspec must never target your local namespace

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

The fix is not to go without a fetch refspec — it is to point it somewhere
disposable. A refspec whose destination is `refs/etude-mirror/<remote>/…` gives
prune nothing of yours to delete, which is what `init` now configures.

Push and fetch solve different problems. Init keeps configured fetch pruning
away from authoritative local refs. `etude sync` performs a non-forced explicit
metadata transfer. Ordinary branch pushing remains under normal Git policy.

### Migrating an existing repository

Older Etude versions installed two mappings that current init migrates:

```text
+refs/etude/*:refs/etude/*  # fetch into the authoritative local namespace
 refs/etude/*:refs/etude/*  # push only metadata on ordinary git push
```

Re-run init against the same remote. Normal init removes both legacy mappings
and installs safe per-kind mirrors. `--force` also removes both mappings but
retains its existing behavior of not adding mirror mappings:

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

Any fetch entry whose part *after* the colon begins with `refs/etude/` is the
prune hazard.
(An entry with no colon at all is harmless — git fetches it to `FETCH_HEAD`
without creating a local ref, so there is nothing for `--prune` to delete.)

To inspect the local migration state directly:

```bash
git config --local --get-all remote.origin.fetch
git config --local --get-all remote.origin.push
```

Push migration is deliberately exact and local. Init removes every local value
equal byte-for-byte to `refs/etude/*:refs/etude/*`, including duplicates. It
preserves the order and bytes of every other value: custom branch mappings,
forced or name-changing Etude mappings, colonless mappings, surrounding
whitespace, and multiline values. The literal is an ownership heuristic, not
proof; if you intentionally configured that exact local value, init removes it
as part of this migration.

System, global, and included configuration is never edited. An inherited
`refs/etude/*:refs/etude/*` mapping can therefore remain effective and continue
to affect ordinary pushes. Init does not manufacture a branch refspec to
counter it. Inspect effective policy with `git config --show-origin --get-all
remote.origin.push` and edit its owning configuration if needed.

### Safety warnings

Every `init` run ends by checking that the repository is actually in the safe
state, and prints a `warning:` line when it is not. `init` configures one
remote, so these catch what it cannot fix itself:

- an etude-registered fetch refspec still present on the target remote;
- the target remote not existing, so `etude sync` cannot publish metadata there;
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
would not parse when pasted. Producing safe remediation is the job of
[`etude doctor`](doctor.md), a read-only health check. It prints an exact
command when one can be derived; when required contents or policy are
unknowable, it explicitly requests human authorship instead of inventing one.

These checks are deliberately narrow, and they compare **exactly**. Detecting an
etude-registered fetch refspec on another remote needs no more than that same
exact check applied to another config key, which is why it is here. But whether
some *other* refspec is equivalent to the canonical one, whether a refspec
broader than the namespace (`+refs/*:refs/*`) also prunes it, whether a mapping
preserves ref names — those need a full model of refspec semantics, and
answering them confidently but wrongly is worse than not answering. That is the
job of [`etude doctor`](doctor.md); `init` does not guess.

In particular a fetch refspec broader than `refs/etude/*` can be dangerous and
init neither removes nor reports it. It is user configuration, so deleting it
could break branch fetching. [`etude doctor`](doctor.md) performs the semantic
check.

## Idempotency

Running `etude init` twice is safe:

- Existing files are skipped (reported as `skipped <path>`). Use `--force` to
  regenerate them from the canonical default.
- Removing legacy fetch and push mappings is idempotent: the next run finds none.
- Safe mirror fetch mappings are present exactly once after normal init.
- Git config writes use Git's lock and retry briefly when linked worktrees run
  init concurrently.

## Plan → apply pipeline

`etude init` runs an ordered plan and apply pipeline. Fetch-hazard cleanup comes
first, push migration comes second, and normal init adds safe mirrors last. An
error or interruption during a later addition therefore cannot leave the old
hazardous fetch mapping in place.

After all actions run, a summary line is printed:

```
init: 4 created, 0 skipped, 3 configured
```

The `configured` count covers configured, already-configured, and removed
refspec values. Fresh and repeated normal init each count the three safe mirror
mappings. Each removed duplicate is also counted. Informational and `warning:`
lines are not counted.

## --dry-run

`--dry-run` previews the planned actions without writing any files or modifying
git config. It prints `plan: create <path>` / `plan: skip <path>` for the
scaffold; `plan: configure` or `plan: keep` for safe mirror mappings; and one
`plan: remove <key> = <value>` per legacy fetch or exact local legacy push value.
It then prints a summary such as:

```
dry-run: 4 to create, 0 to skip, 3 to configure
```

Dry-run behavior:
- **Never errors on a missing remote.** It reports a would-skip note and exits
  with code 0. Use this to preview what `init` would do before a remote is added.
- **Syntactic `--remote` validation still runs.** A malformed name (e.g. `--remote
  "bad name"`) errors immediately, before any reads.
- **Workflow self-check still runs.** The YAML round-trip validation runs during
  plan (read-only) and can error under dry-run.
- **`--force --dry-run`** does not preview mirror additions. It does preview
  legacy fetch and push removals because real `--force` performs both.
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
| `--force` | false | Overwrite existing scaffolded files. It still removes legacy fetch and exact local legacy push mappings, but does not add mirror mappings. |
| `--remote <name>` | `origin` | Git remote to configure refspecs on. Passing an explicit name for a missing remote is an error (even under `--force`). |

## Example

```bash
# Scaffold a new repository:
etude init

# Inspect what was created:
cat .etude/workflow.yaml
git config --local --get-all remote.origin.push
# A fresh init prints nothing here. Existing user policy may still appear.
git config --local --get-all remote.origin.fetch
# Safe Etude entries point only at refs/etude-mirror/origin/.

# Publish metadata explicitly:
etude sync

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
