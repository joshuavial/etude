# Runs

`etude run list` and `etude run show` let you inspect runs stored under
`refs/etude/runs/*`.

## List runs

```bash
etude run list
```

Prints a tab-aligned table of all stored runs, ordered lexically by run id:

```
RUN ID    WORKFLOW  CREATED               STAGES
run-1     manual    2026-05-23T10:00:00Z  2
run-2     manual    2026-05-23T11:30:00Z  1
```

Columns:

| Column | Content |
|--------|---------|
| `RUN ID` | The run identifier |
| `WORKFLOW` | Workflow name recorded in the run manifest |
| `CREATED` | Creation timestamp in RFC3339 UTC |
| `STAGES` | Number of stages captured in the run |

When there are no runs, the command prints `no runs found` and exits 0.

`run list` takes no arguments or flags. It walks `refs/etude/runs/*` directly;
there is no query index.

If any run's `manifest.json` is missing or cannot be parsed, the command fails
with a non-zero exit code, names the offending run id in an error written to
stderr, and prints nothing to stdout.

## Show a run

```bash
etude run show <run-id>
```

Prints a human-readable detail view of one run. Example:

```
run id:           run-1
workflow:         manual
workflow version: manual-v1
created:          2026-05-23T10:00:00Z
refs:
  pr: 469

stage: plan
  produced_by: original
  git sha:     a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2
  skill:       plan@manual (manual)
  input:  role=context path=artifacts/sha256/abc123... size=1024 storage=content media-type=text/plain; charset=utf-8
  output: role=output path=artifacts/sha256/def456... size=2048 storage=content media-type=text/markdown; charset=utf-8
```

When a stage was captured by a known harness and model, two additional lines
appear before `skill:`:

```
stage: plan
  produced_by: agent-run
  git sha:     a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2
  harness:     dev-workflow 1.2.0
  model:       claude-sonnet-4-6
  skill:       plan@1.0.0 (github.com/example/skills)
  input:  role=context path=artifacts/sha256/abc123... size=1024 storage=content media-type=text/plain; charset=utf-8
  output: role=output path=artifacts/sha256/def456... size=2048 storage=content media-type=text/markdown; charset=utf-8
```

The `refs:` block appears only when the run has external refs; its keys are
printed in sorted order.

Per stage, the detail view shows:

- `produced_by`, `git sha`
- `replay of:` — printed as `<run-id>/<stage>` immediately after `produced_by`; only present for replay stages (`produced_by: replay`)
- `harness` — harness name and optional version; omitted for manual/legacy captures that did not record a harness
- `model` — model name; omitted for manual/legacy captures that did not record a model
- `skill` — always printed as `id@version (repo)`
- Each input as `role=… path=… size=… storage=… media-type=…`
- The output in the same format

`path` is the content-addressed `artifacts/sha256/…` path of the stored
artifact, `size` is its size in bytes, and `storage` is `content` for inline
artifact bytes or `pointer` for an external pointer record.

### Run id validation

The run id is validated before any git call, so the check works even outside a
git repository. Invalid ids produce `invalid run id "<id>"` and a non-zero exit
code. The following are rejected:

- Contains `/`
- Contains `..`
- Starts or ends with `.`
- Consists entirely of dots
- Ends with `.lock`
- Contains characters other than letters, digits, `-`, `_`, and `.`

An unknown run id prints `run "<id>" not found` to stderr and exits non-zero.

## Current limits

- Runs are listed by walking `refs/etude/runs/*` directly. There is no query
  index; performance degrades with a large number of runs.
- There is no `--json` or machine-readable output flag yet. Structured output
  is deferred to a follow-up.
