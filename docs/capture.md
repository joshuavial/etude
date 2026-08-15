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
--harness <name>
--harness-version <version>
--model <model-id>
--expect create|append
```

If `--git-sha` is omitted, `etude` records the current `HEAD`. When provided, it
must be a full lowercase hex commit id (40 characters for SHA-1, 64 for SHA-256);
any other value is rejected.
If `--skill-id` is omitted, it defaults to the `<stage>` argument.
The other metadata defaults are `--workflow manual`,
`--workflow-version manual-v1`, `--produced-by original`,
`--skill-repo manual`, and `--skill-version manual`.
Use `--message` to override the Git commit message for the run ref update.
`--harness`, `--harness-version`, and `--model` are optional; all default to
empty string. They populate the run manifest's producer block (harness name,
harness version, and model that produced the stage). When `--harness` is
omitted, the harness sub-block is omitted from the manifest entirely; when
`--model` is omitted, no model field is written.

## Appending

Running capture again with the same `--run` appends another stage to the run.
Existing stages and artifacts are preserved. New `--ref` values are merged into
the run refs; if a key already exists, the new value replaces it.

`--workflow` and `--workflow-version` are run-level metadata. If either is
explicitly provided during append and conflicts with the existing run, capture
fails.

## Create versus append

Capture creates the run when `refs/etude/runs/<run-id>` does not exist and
appends when it does. Every successful capture reports which one it did:

```
captured <commit>
ref refs/etude/runs/run-1
action create
```

`--expect` makes that choice a requirement rather than an observation:

| flag | requires | on violation |
|---|---|---|
| `--expect create` | the run must NOT already exist | fails, naming the existing ref and its tip; the ref is not modified |
| `--expect append` | the run MUST already exist | fails, naming the missing ref; no run is created |
| omitted | either | creates or appends, and says which |

Pass `--expect append` from anything that believes it is extending a run — a
multi-stage workflow, a script capturing successive phases. A missing run ref at
that point is a **data-loss signal, not a fresh start**: without the flag the
capture would silently begin a new manifest and the run's recorded stages and
gate attempts would be gone while capture still printed `captured <commit>`.
Investigate why the ref disappeared before re-capturing.

`etude capture-run` (create-only) and `etude capture-gate` (append-only) already
fail rather than choose.

## Capturing review gates

`etude capture` records stage *producers*. To record the review *gate* a stage
passed through (reviewer seats, verdicts, provider/model/harness), use
`etude capture-gate` — see [Gate reviewer records](gates.md).

## Current Limits

Not implemented yet:

- workflow config loading
- pointer/external artifact capture
- eval and import commands
