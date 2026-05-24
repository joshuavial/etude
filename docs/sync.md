# etude sync

## Overview

`etude sync` pushes and fetches the `refs/etude/*` namespace with a git remote.

Custom refs like `refs/etude/runs/*` and `refs/etude/evals/*` do not travel
with ordinary `git push` or `git fetch`: those commands only transfer
`refs/heads/*` and `refs/tags/*` by default. `etude sync` makes the transfer
explicit, so captured run data moves between clones.

```bash
etude sync [--remote <name>]
```

The default remote is `origin`.

## What it does

Sync runs in order:

1. Validates the `--remote` name.
2. Verifies the remote exists in the local git config.
3. Fetches `refs/etude/*` from the remote (non-forced).
4. Checks whether any local `refs/etude/*` exist.
5. Pushes `refs/etude/*` to the remote (non-forced) if local refs exist.

Both the fetch and push pass an explicit refspec on the command line, so sync
works even if `etude init` was never run.

## Reconciliation behavior

Sync is safe by design: it never moves a local ref backward and never
force-clobbers a remote ref. Each direction is a plain, non-forced transfer.

**Remote-ahead (fast-forward).** The remote has commits the local clone does
not. Fetch fast-forwards the local ref; the push step then runs and reports the
now up-to-date ref. Sync prints:

```
fetched refs/etude/* from origin (fast-forwarded: refs/etude/runs/<id>)
pushed refs/etude/* to origin (refs/etude/runs/<id>)
```

**Local-ahead.** The local clone has commits the remote does not. Fetch cannot
fast-forward the local ref onto the remote's older copy, so it leaves the local
ref unchanged and reports the ref as not fast-forwardable; push then advances the
remote. Sync prints:

```
fetched refs/etude/* from origin (some refs not fast-forwardable: refs/etude/runs/<id>)
pushed refs/etude/* to origin (refs/etude/runs/<id>)
```

**Up to date.** Both sides already agree. Fetch reports nothing new. The push
still lists the ref: `git push` emits a `=` `[up to date]` status line for the
matched ref, which sync reports in its push summary. Sync prints:

```
fetched refs/etude/* from origin
pushed refs/etude/* to origin (refs/etude/runs/<id>)
```

**Diverged.** The same ref advanced differently in two clones (e.g. the same
run id was written independently). The fetch first reports the ref as not
fast-forwardable (on stdout); the push is then rejected and sync exits non-zero
with a message naming the full ref path (on stderr):

```
fetched refs/etude/* from origin (some refs not fast-forwardable: refs/etude/runs/<id>)
push rejected (diverged refs):
refs/etude/runs/<id> diverged from origin; manual resolution required
```

Nothing is clobbered on either side. Resolving the divergence is a manual step.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--remote <name>` | `origin` | Git remote to sync with. |

## Error cases

**Missing remote.** Unlike `etude init`, sync always errors when the remote is
absent (including when the default `origin` does not exist). Exit is non-zero:

```
remote "origin" not found
```

**Invalid remote name.** Rejected before any git call:

```
invalid remote name "...": ...
```

**No local refs.** The fetch still runs first; then, when `refs/etude/*` is empty
locally, the push step is skipped and sync exits 0:

```
fetched refs/etude/* from origin
no local refs/etude/* to push
```

**Transport or local failures.** A genuine fetch failure (unreachable remote,
authentication, a stuck ref lock) aborts the sync before the push with a
non-zero `fetch failed: ...` error that surfaces git's own message; a remote
that rejects the push (for example a server-side hook) surfaces as a non-zero
`push failed: ...`. These are real failures, distinct from the diverged-ref case
above, and never leave a ref partially clobbered.

## Example invocations

```bash
# Sync with origin (the default):
etude sync

# Sync with a different remote:
etude sync --remote upstream

# Typical output when local is ahead of the remote:
# fetched refs/etude/* from origin (some refs not fast-forwardable: refs/etude/runs/run-1)
# pushed refs/etude/* to origin (refs/etude/runs/run-1)
```

## Relationship to etude init

`etude init` writes the `+refs/etude/*:refs/etude/*` fetch refspec into git
config so that `git fetch` picks up the namespace automatically on subsequent
plain fetches. `etude sync` passes the refspec explicitly and does not rely on
the config entry, so it works independently of whether init was run.

See [Init](init.md) for the init command.

## Current limits

- No `--json` or machine-readable output flag. Structured output is deferred.
- Diverged refs are surfaced for manual resolution; sync does not attempt an
  automatic merge.
