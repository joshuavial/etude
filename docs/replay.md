# Replay

`etude replay` re-executes a recorded stage end-to-end: it resolves the
stage's recorded inputs, checks out the recorded git SHA into a throwaway
worktree, invokes an external runner, and emits the produced output.

Replay does **not** persist a new stage back into the run. It only emits the
output. Recording a replayed stage is deferred to a later release.

```bash
etude replay <run-id> <stage>
```

Example:

```bash
etude replay run-1 plan --runner ./run.sh
etude replay run-1 plan --runner ./run.sh --output result.md
```

## Flags

| Flag | Description |
|------|-------------|
| `--runner <command>` | Runner command spec. Whitespace-split into argv; `Command[0]` is the binary, the remainder are arguments. No shell quoting or expansion is performed (see [Current limits](#current-limits)). Falls back to `git config etude.runner` when omitted. |
| `--output <path>` | Write output to `<path>` instead of stdout. When set, a confirmation line is printed to stdout. |

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
- Replay does not persist the output as a new stage in the run. Emitting only;
  recording is deferred.
- There is no `--json` or machine-readable output flag.
