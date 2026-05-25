# GC

`etude gc` reports artifact storage across all run and eval refs, and optionally
deletes named run refs that pass the safety invariant. Object cleanup is `git
gc`'s job; `etude gc` only removes refs.

```bash
# Default: print a storage report, never delete anything.
etude gc [--max-size N]

# Explicit, safety-checked deletion of named runs.
etude gc --prune <run-id> [<run-id>...]
```

## Report mode (default)

Running `etude gc` without `--prune` collects and prints:

**TOTAL** — sum of `ArtifactRef.Size` for all content-storage artifacts across
all run refs, plus run and eval ref counts. The label reads *logical artifact
bytes (pre-dedup)*: the number reflects the sizes recorded in the manifests at
capture time, before any git-level deduplication of blobs. Identical content
stored in multiple runs is counted each time.

**OVERSIZED** (only when `--max-size N` is given) — runs whose total
content-artifact bytes exceed `N`. Useful for spotting large outliers.

**EXTERNAL** (when any run has pointer artifacts) — pointer artifacts listed
per run with their external URIs. `etude gc` does not fetch, validate, or delete
external URIs in v1; the section is informational.

Report mode exits 0 and never modifies any ref or object.

## Prune mode (--prune)

`etude gc --prune <run-id>...` deletes the named run refs after checking the
**safety invariant**: a run is refused if it is pinned by any live reference.

Before each deletion `etude gc` builds the live pin-set by walking all run and
eval refs:

- A run ID is **pinned** if it appears as `ArtifactSource.RunID` in any eval
  result (Targets or Context), or as `ReplayOf.RunID` in any stage of any run.
- A run that is not pinned is safe to delete.

Pinned runs and runs that do not exist are **refused** (printed to stderr;
never deleted). Eligible runs are deleted with
`git update-ref -d --no-deref refs/etude/runs/<id>`. Unreferenced git objects
are left for `git gc`.

### Exit codes

| Condition | Exit |
|-----------|------|
| Report mode (no `--prune`) | 0 always |
| `--prune` with all named runs deleted | 0 |
| `--prune` with any refusal or unknown id | non-zero |
| `--prune` with no run-id args | non-zero (usage error) |

### Output

```
pruned <id>           # written to stdout for each deleted run
refused <id>: <why>   # written to stderr for each refused run
```

## Flags

| Flag | Description |
|------|-------------|
| `--max-size N` | Enable the OVERSIZED section for runs exceeding `N` bytes of content-artifact storage. |
| `--prune` | Enable deletion mode. Requires one or more run-id arguments. |

## Design notes

There is **no automatic garbage collection** in v1. Leaf runs (no inbound eval
or replay edge) are normal captured work — deleting them automatically would
destroy the user's data. `etude gc` only deletes runs the user explicitly names,
and only when the safety invariant permits.

Eval refs are not deletable by `etude gc` in v1.

Pointer artifact URIs in the EXTERNAL section are informational only. No
remote-URI fetch, validity check, or deletion is performed.

## Examples

```bash
# Storage report for the current repo.
etude gc

# Show runs exceeding 50 MB.
etude gc --max-size 52428800

# Delete two leaf runs.
etude gc --prune run-abc run-def

# Attempt deletion of a pinned run (refused, non-zero exit).
etude gc --prune run-pinned-by-eval
# stderr: refused run-pinned-by-eval: pinned by eval eval-1 (target)
```
