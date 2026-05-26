# Replay

`etude replay` re-executes a recorded stage end-to-end: it resolves the
stage's recorded inputs, checks out the recorded git SHA into a throwaway
worktree, invokes an external runner, and emits the produced output.

Without `--record`, replay only emits the output and does not persist anything.
With `--record`, it writes a new linked run; see [Recording (--record)](#recording---record) below.

```bash
etude replay <run-id> <stage>
```

Example:

```bash
etude replay run-1 plan --runner ./run.sh
etude replay run-1 plan --runner ./run.sh --output result.md
etude replay run-1 plan --runner ./run.sh --record
etude replay run-1 plan --runner ./run.sh --record --output result.md
```

## Flags

| Flag | Description |
|------|-------------|
| `--runner <command>` | Runner command spec. Whitespace-split into argv; `Command[0]` is the binary, the remainder are arguments. No shell quoting or expansion is performed (see [Current limits](#current-limits)). Falls back to `git config etude.runner` when omitted. |
| `--output <path>` | Write output to `<path>` instead of stdout. When set, a confirmation line is printed to stdout. May be combined with `--record`. The file is opened without following symlinks (on Unix, atomically via `O_NOFOLLOW`): a non-existent path is created, an existing regular file is overwritten, but an existing symlink (or other non-regular file) is rejected rather than followed — so `--output` cannot clobber a file the symlink points at. |
| `--timeout <duration>` | Per-invocation timeout for the runner (default `10m`; `0` disables). The runner process is killed when the timeout elapses and the command returns a "timed out" error. A small grace period bounds cleanup even if the runner backgrounds a child that holds its output pipe open. |
| `--record` | Persist the replay output as a new linked run. See [Recording (--record)](#recording---record). |
| `--skill-version <v>` | Override `producer.skill.version` in the recorded producer. Only affects the recorded run; unset fields inherit from the source stage's producer. Requires `--record` to have any effect. |
| `--skill-id <id>` | Override `producer.skill.id` in the recorded producer. |
| `--skill-repo <repo>` | Override `producer.skill.repo` in the recorded producer. |
| `--model <model>` | Override `producer.model` in the recorded producer. |
| `--harness <name>` | Override `producer.harness.name` in the recorded producer. |
| `--harness-version <v>` | Override `producer.harness.version` in the recorded producer. |

## Recording (--record)

When `--record` is passed, `etude replay` persists the replay output as a new
**linked run** rather than simply emitting it. The source run is never modified.

### New run id

The new run id is derived from the source run id and the current UTC timestamp:

```
<source-run-id>-replay-<yyyymmddThhmmssZ>
```

For example, replaying `run-1` at `2026-05-25T10:30:00Z` produces
`run-1-replay-20260525T103000Z`. If that id is already taken, a numeric suffix
is appended (`-2` through `-10`) until a free slot is found.

The new run is stored at `refs/etude/runs/<new-run-id>`.

### Recorded stage

The new run contains a single stage with:

- `stage` — same name as the source stage.
- `produced_by` — `"replay"`.
- `git_sha` — the source stage's recorded git SHA (the replay runs at that
  commit, so the SHA is preserved).
- `producer` — the source stage's producer, merged with any producer-override
  flags (`--skill-version`, `--model`, etc.). Unset override flags inherit from
  the source.
- `inputs` — the source stage's artifact refs, copied verbatim (byte-identical
  content-addressed refs).
- `output` — the artifact produced by the replay run.
- `replay_of` — a link back to the source: `{run_id, stage, commit}`, where
  `commit` is the immutable git commit of the source run ref (not the stage's
  `git_sha`). This pins the link durably even if the source run ref is later
  updated.

The `replay_of` field is required for any stage with `produced_by: "replay"`,
and forbidden otherwise. The manifest validator enforces this bidirectionally.

### Confirmation and output

After the new run is written, `etude replay` prints:

```
recorded replay run <new-run-id> (commit <git-oid>)
```

Here `<git-oid>` is the commit of the **new replay run** (not the source
commit recorded in `replay_of.commit`).

Recording does not suppress emitting: after the confirmation line, the replay
output is still emitted — to stdout when `--output` is not given, or written to
`<path>` when it is. So `--record` and `--output` may coexist: the output is
both written to `<path>` and recorded as the new run's output artifact.

If the runner produces no output, `--record` fails with:

```
replay produced no output; cannot record empty run
```

### Runner resolution

If `--runner` is not supplied, `etude replay` reads `git config etude.runner`
(any git config scope). If neither source provides a value, the command fails
with:

```
no runner configured (set --runner or git config etude.runner)
```

## Runner I/O contract

The runner is invoked as an external process with:

- **Working directory** set to the throwaway worktree (a pristine checkout of
  the stage's recorded git SHA).
- **Environment** restricted to three variables:
  - `PATH` — inherited from the calling process.
  - `ETUDE_INPUTS_DIR` — path to a directory containing one file per stage
    input, named `<NN>-<role>` (two-digit zero-padded index, then the input
    role). For example: `00-context`, `01-rubric`.
  - `ETUDE_OUTPUT_FILE` — path the runner must write its output to. The file
    does not exist before the runner is called; the runner must create it.
- **Stdout and stderr** are captured but not forwarded to the terminal. Stderr
  appears in error messages only when the runner exits non-zero.

All environment variables other than `PATH`, `ETUDE_INPUTS_DIR`, and
`ETUDE_OUTPUT_FILE` are stripped from the runner's environment.

After the runner exits, `etude replay` reads the file at `ETUDE_OUTPUT_FILE`
and emits its bytes as the replay output (to stdout or to `--output <path>`).
The output is read through a hard size cap (default 64 MiB); a runner whose
output exceeds the cap is rejected with an "output too large" error rather than
read into memory unbounded.

The worktree and all scratch files are always removed when the command
finishes, whether it succeeds or fails.

## Errors

| Condition | Message |
|-----------|---------|
| Invalid run id format | `invalid run id: <id>` |
| Run id not found | `run not found: <id>` |
| Stage name not found | `stage not found: "<name>" not found in run "<id>"; available: <names>` |
| Stage name ambiguous (duplicate stages) | `ambiguous stage: "<name>" appears N times in run "<id>": ...` |
| Stage has no recorded git SHA | `stage "<name>" has no recorded git sha` |
| Recorded git SHA is malformed | `invalid git sha "<sha>": ...` |
| Git SHA not present in repository | `git sha "<sha>" not found in repository` |
| No runner configured | `no runner configured (set --runner or git config etude.runner)` |
| Runner exits non-zero | `runner failed: <binary>: <exit status>: <stderr>` |
| Runner did not write output file | `output missing` |
| Runner output is not a regular file | `output is not a regular file` |
| Input is a pointer artifact | `input "<role>" is a pointer artifact and cannot be replayed yet` |

## Current limits

- The `--runner` spec is whitespace-split with no shell quoting or expansion.
  A runner path containing spaces cannot be expressed in v1; use a wrapper
  script instead.
- Pointer/external artifact inputs are not replayable yet. All stage inputs
  must be stored as inline content artifacts.
- There is no `--json` or machine-readable output flag.
