# Reindex

`etude reindex` builds a derived SQLite cache of all run and eval refs, stored
at `.git/etude-index.db`. The index is a full rebuild every time; there is no
incremental update in v1.

```bash
etude reindex
```

## What it does

`etude reindex` walks all `refs/etude/runs/*` and `refs/etude/evals/*` refs,
parses each manifest and eval result, and writes the extracted data into a
single SQLite database. The output is a summary line:

```
reindexed N runs, M evals into /path/to/.git/etude-index.db
```

If any manifest or eval result is malformed, the command fails and names the
offending ref. A pre-existing valid index at `.git/etude-index.db` is left
untouched on failure — the new index is built in a temp file first and only
atomically replaces the old one on success.

## Database location

The default path is `<absolute-git-dir>/etude-index.db`, resolved via
`git rev-parse --absolute-git-dir`. This correctly handles:

- Normal repos (`.git/etude-index.db`)
- Linked worktrees (`.git` is a file; resolves to the shared git dir)
- Bare repos

The database lives inside `.git/` and is never committed. `*.db` is in
`.gitignore`.

## Schema version

The index carries a `meta.schema_version` integer. Opening a database whose
`schema_version` differs from the current expected version (1) returns an error
with a message directing you to run `etude reindex`. There is no migration
machinery in v1 — reindex whenever the schema changes.

## Driver

The index uses `modernc.org/sqlite` (pure-Go, cgo-free). No C toolchain is
required; `go install` works on any platform without extra setup.

## Status: index not yet consumed

The index is additive-only in v1. No existing command (`run list`, `gc`, `bench`)
reads from it yet. Consumer beads will rewire those commands in a follow-up.
`etude reindex` is safe to run at any time; it has no side effects beyond
writing `.git/etude-index.db`.

## Flags

| Flag | Description |
|------|-------------|
| (none) | Resolve git dir automatically from cwd. |

The hidden `--db-path` flag overrides the database path and is intended for
tests only.

## Examples

```bash
# Build (or rebuild) the index in the current repo.
etude reindex

# Inspect the schema after a reindex.
sqlite3 .git/etude-index.db ".tables"
sqlite3 .git/etude-index.db "SELECT run_id, workflow, created FROM runs LIMIT 5"
```
