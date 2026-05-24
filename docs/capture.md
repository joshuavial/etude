# Manual Capture

`etude capture` records local file artifacts into a run stored under
`refs/etude/runs/<run-id>`.

```bash
etude capture <stage> --run <id> --output <role>=<path>
```

Example:

```bash
etude capture plan --run run-1 --output output=plan.md --ref pr=469
```

The command must run inside a Git repository so it can write
`refs/etude/runs/<run-id>`. By default it also needs at least one commit so it
can record `HEAD`; pass `--git-sha` explicitly when there is no resolvable
`HEAD`.

## Artifacts

Capture v1 stores local file content only.

- `--output role=path` is required exactly once.
- `--input role=path` can be repeated.
- `role` values use letters, digits, `_`, `-`, and `.`.
- File content is stored as a SHA-256-addressed artifact in the run ref.

Media types are inferred from a deterministic built-in extension table. Unknown
extensions are recorded as `application/octet-stream`.

## Metadata

Useful flags:

```bash
--ref key=value
--workflow manual
--workflow-version manual-v1
--produced-by original
--git-sha <sha>
--skill-id <id>
--skill-repo manual
--skill-version manual
--message <text>
```

If `--git-sha` is omitted, `etude` records the current `HEAD`.
If `--skill-id` is omitted, it defaults to the `<stage>` argument.
The other metadata defaults are `--workflow manual`,
`--workflow-version manual-v1`, `--produced-by original`,
`--skill-repo manual`, and `--skill-version manual`.
Use `--message` to override the Git commit message for the run ref update.

## Appending

Running capture again with the same `--run` appends another stage to the run.
Existing stages and artifacts are preserved. New `--ref` values are merged into
the run refs; if a key already exists, the new value replaces it.

`--workflow` and `--workflow-version` are run-level metadata. If either is
explicitly provided during append and conflicts with the existing run, capture
fails.

## Current Limits

Not implemented yet:

- workflow config loading
- pointer/external artifact capture
- replay, eval, and import commands
