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
there is no query index. (A SQLite index exists at `.git/etude-index.db` and can
be built with `etude reindex`, but `run list` does not yet consume it.)

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

When the workflow's `env_allowlist` is non-empty, an `env allowlist:` line
appears in the header immediately after `created:` (before `refs:` when present),
listing the configured names (never values):

```
env allowlist:    ETUDE_TEST_MARKER, ANTHROPIC_API_KEY
```

See [Env passthrough](#env-passthrough) for details.

Per stage, the detail view shows:

- `produced_by`, `git sha`
- `replay of:` — printed as `<run-id>/<stage>` immediately after `produced_by`; only present for replay stages (`produced_by: replay`)
- `harness` — harness name and optional version; omitted for manual/legacy captures that did not record a harness
- `model` — model name; omitted for manual/legacy captures that did not record a model
- `skill` — always printed as `id@version (repo)`
- Each input as `role=… path=… size=… storage=… media-type=…`
- The output in the same format
- `session` evidence — printed only when the stage's producer was an agentic
  runner that reported a session (see [Producer session evidence](#producer-session-evidence))

`path` is the content-addressed `artifacts/sha256/…` path of the stored
artifact, `size` is its size in bytes, and `storage` is `content` for inline
artifact bytes or `pointer` for an external pointer record.

### Producer session evidence

When a stage is produced by an agentic runner (i.e. not a `deterministic`
provider or `shell` harness) that reports its session, the stage records
**session evidence** so the underlying agent transcript can be interrogated
later. It uses the same shape as gate-reviewer seat session evidence (see
[Gate reviewer records](gates.md)) and renders under the stage:

```
stage: plan
  produced_by: agent-run
  harness:     claude-code 2.1.195
  model:       claude-opus-4-8
  skill:       plan@1.0.0 (github.com/example/skills)
  output: role=plan path=artifacts/sha256/def456... size=2048 storage=content media-type=text/markdown; charset=utf-8
    session_id: 019f1723-d148-7703-8118-ce9438fce210
    retrieval: imported
    redaction: passed
    transcript_artifact: artifacts/sha256/ab/ab12...
```

- `session_id` / `transcript_uri` — the harness session identity (at least one is present).
- `retrieval` — `imported` (transcript read and stored), `failed` (could not read), or `not_applicable`.
- `redaction` — `passed` (secret scan clean), `failed` (secrets detected), or `not_applicable`.
- `transcript_artifact` — the stored transcript (content-addressed), present only when the transcript was imported.

Capture is **best-effort**: a stage runner that reports no session, or whose
transcript cannot be read, never fails the stage — the evidence is simply
omitted or marked `failed`. Transcripts are secret-scanned before storage; a
failed scan is recorded as `redaction: failed` rather than storing the bytes.

When a run carries review-gate attempts, they are printed after the stages —
see [Gate reviewer records](gates.md) for the gate output format and how to
capture gates with `etude capture-gate`.

### Machine-readable output

```bash
etude run show <run-id> --json
```

`--json` emits the run's verbatim on-disk manifest — the canonical snake_case
schema (the same bytes stored as `manifest.json`, including any review-gate
attempts), so the output is re-ingestible by tooling. The manifest is validated
before output, so a corrupt manifest still errors rather than printing partial
JSON.

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

## Live run

`etude run <workflow>` executes a workflow's stage graph live, capturing each
stage incrementally as it completes.

```bash
etude run <workflow> --task <task-file> [flags]
```

Example:

```bash
etude run dev-pipeline --task bead.md
etude run dev-pipeline --task bead.md --run-id my-run-001
etude run dev-pipeline --task bead.md --timeout 30m
```

### How it works

The engine reads `.etude/workflow.yaml` and `.etude/registry.yaml`, resolves
each stage's runner (from the registry or inline), and walks the stage graph
in dependency order. All stages share a single evolving worktree checked out
at the run's git SHA, so mutations from earlier stages are visible to later
ones.

After each stage completes its runner, the engine chains the stage's output
artifact into the next stage's inputs by role (matching `capture-run`
semantics), then captures the stage to `refs/etude/runs/<id>` via an
atomic compare-and-swap append. Every intermediate commit is a valid,
parseable run manifest, so `etude run show <id>` works mid-run and a crash
leaves a valid partial run.

Per stage the command prints:

```
captured <commit>
```

On completion:

```
ref refs/etude/runs/<id>
```

`etude run list` and `etude run show <id>` work immediately after the first
stage completes, before the run finishes.

### Flags

| Flag | Description |
|------|-------------|
| `--workflow <name>` | Select the named workflow `.etude/workflows/<name>.yaml`. Omitted, the repo default `.etude/workflow.yaml` is used. See [Named workflows](#named-workflows). |
| `--task <file>` | Path to the task input file. Seeds the special `task` role. Required for a fresh run. |
| `--run-id <id>` | Explicit run id (auto-generated as `<workflow>-<timestamp>-<rand>` if omitted). Must be unique; errors on collision. |
| `--git-sha <sha>` | Git commit SHA for the worktree (defaults to `HEAD`). |
| `--runner <command>` | Runner command override applied to all stages. |
| `--timeout <duration>` | Per-stage runner timeout (default `10m`; `0` disables). |
| `--resume <id>` | Resume a partial run. See [Resume](#resume). |

### Run id

When `--run-id` is omitted, the engine generates a sortable id of the form
`<workflow>-<UTC:yyyymmddThhmmssZ>-<8-hex>` (4 random bytes). Explicit ids are validated before
any git call and must not contain `/`, `..`, leading or trailing `.`, or
characters other than letters, digits, `-`, `_`, and `.`.

Reserved workflow names `show` and `list` are rejected with a clear error
because they are shadowed by the `run` subcommands.

### Named workflows

A repo can host more than one workflow. `--workflow <name>` selects
`.etude/workflows/<name>.yaml`; without the flag the engine reads the repo
default `.etude/workflow.yaml`.

```bash
etude run dev --task bead.md                        # .etude/workflow.yaml
etude run gate --workflow gate --task packet.md     # .etude/workflows/gate.yaml
```

This lets a full development pipeline and a narrow gate-only workflow live side
by side in the repo they act on, sharing one run store — `etude run list` and
`etude run show` cover both, since they key off the run manifest.

The positional argument keeps its existing meaning: it asserts that the loaded
workflow's `name:` is the one you meant, and a mismatch is an error naming the
file that was actually read. There is deliberately **no fallback** between the
two locations — a missing `.etude/workflows/<name>.yaml` errors on that path
rather than silently reading the default.

`--workflow` takes a name, not a path; values containing a path separator are
rejected. The run manifest records the workflow name, so a name re-resolves to
the same file on any machine while a path would not.

`etude init` scaffolds `.etude/workflow.yaml` only. Create `.etude/workflows/`
yourself when you want a second workflow.

Two commands do not yet take `--workflow` and always read
`.etude/workflow.yaml`: `etude replay` (which is given a run id, not a workflow
name) and `etude retro nudge` (which is repo-scoped).

### Gate execution

When a workflow stage carries a `gate` block, the engine runs it after the
stage completes, before advancing to the next stage.

A gate has two seat kinds:

- **checks** — deterministic runners (scripts, lint, tests). The runner is
  invoked through the external-runner contract (`ETUDE_INPUTS_DIR`,
  `ETUDE_OUTPUT_FILE`). Exit 0 passes; any nonzero exit, launch failure, or
  timeout is a hard BLOCK — no threshold applies, and even a unanimous seat
  `go` cannot override a failing check.
- **seats** — model/variant runners that write a verdict JSON envelope to
  `ETUDE_OUTPUT_FILE`:

  ```json
  {"verdict":"go","required":[],"optional":[]}
  ```

  `verdict` is `"go"` or `"block"`. `required` lists required changes on a
  block; `optional` lists suggestions on a go. Seats that cannot launch, time
  out, produce no output, or produce non-JSON output are recorded as
  `failed`/`empty`/`malfunction` and excluded from the vote count.

**Synthesis** is fail-closed:

1. Any failing check → not a pass (skip seat vote).
2. Checks-only gate (no seats), all checks pass → **pass**.
3. Usable seat count (valid envelope received) < min(2, configured seats) →
   **escalated** (insufficient usable seats; escalation_reason recorded).
4. `goCount / usableCount >= pass_threshold` (default 1.0) → **pass**;
   otherwise not a pass.
5. Not-pass with rounds remaining → **rerun**; rounds exhausted → **escalated**.

On **rerun**: the engine builds a `gate-feedback` artifact (failed check
details + blocking seats' `required[]`) and re-runs the guarded stage with
that artifact appended to its inputs (role `gate-feedback`). The re-run stage
is captured as `<stage>.r<round>` (round ≥ 2); the gate is re-evaluated
against its output.

On **escalated**: the engine advances the tier ladder toward L1 (strongest)
and re-runs the gate against the latest stage output at the stronger tier. If
no stronger tier exists (already at L1, or the gate used inline seats with no
tier ladder), the run halts with a `GateEscalationError` — the partial run is
valid and inspectable via `etude run show`. A terminal escalation caused only
by unusable model seats is resumable; substantive reviewer decisions remain
terminal.

Each gate attempt is recorded automatically as a `GateAttempt` in the run
manifest (`manifest_version` 3); gate attempts appear after stages in
`etude run show`. No separate `etude capture-gate` call is required for live
runs.

### Env passthrough

By default, each stage runner receives a hermetic environment containing only
`PATH`, `ETUDE_INPUTS_DIR`, and `ETUDE_OUTPUT_FILE`. To pass additional env
vars (such as API keys) to live runners, declare an allowlist of names in
`.etude/workflow.yaml`:

```yaml
env_allowlist: [ANTHROPIC_API_KEY, OPENAI_API_KEY]
```

At run time, etude reads each listed name's value from the orchestrator's own
process env and passes it to each stage runner and gate model seat. Values are
never stored in the manifest or artifact files; only the names are recorded.

**Scope.** The allowlist applies to live stage runners and gate model seats.
Deterministic gate checks are always hermetic (they receive no allowlist entries
regardless of configuration).

**Validation.** Each entry must be a valid POSIX env var name
(`[A-Za-z_][A-Za-z0-9_]*`). Duplicates and the three reserved names (`PATH`,
`ETUDE_INPUTS_DIR`, `ETUDE_OUTPUT_FILE`) are rejected at workflow load time.

**Audit.** The configured names (never values) appear in `etude run show` as an
`env allowlist:` header line and in the run manifest (`env_allowlist` field).
The manifest records the configured names as intent/policy: a name is only
passed to the child process when it is present in the orchestrator's env —
"listed" does not mean "value delivered". Secret values never appear in the
manifest or `etude run show` output. If a runner echoes its own secret into its
output artifact, that is operator responsibility; etude's guarantee is that
values cross only into the child process environment.

**Replay.** By default, `etude replay` stays hermetic (no passthrough). Pass
`--allow-env` to load the workflow's `env_allowlist` and apply it during replay:

```bash
etude replay <run-id> --allow-env
etude replay <run-id> <stage> --allow-env
```

`--allow-env` and `--record` cannot be combined (rejected with a clear error).

### Forward replay

Once a live run is captured, `etude replay <run-id>` (one argument, no stage)
re-executes all its stages forward in a single evolving worktree, using the
recorded (content-addressed) inputs for each stage. This is the inverse of
a live run: it replays what was captured, in order.

Single-stage flags (`--record`, `--output`, producer overrides) are not valid
in forward-replay mode and return an error if supplied.

## Resume

When a stage runner exits non-zero or times out, `etude run` stops, leaves
the partial run (all previously-completed stages remain durably captured),
prints the failure to stderr, and exits non-zero:

```
stage "<name>" failed: <error>
resume with: etude run <workflow> --resume <id>
```

To continue from where the run stopped:

```bash
etude run <workflow> --resume <id>
```

`--resume` loads the partial run manifest, reseeds all previously captured
artifact blobs (including the task input) from the run commit, and continues
CAS-appending from the current run ref head. It normally resumes at the first
stage whose output role has not been produced yet.

A run whose stages are all captured can also resume a gate that stopped only
because every model seat was unusable (`failed`, `empty`, `malfunction`, or
`disregarded`). The latest attempt must carry Etude's insufficient-usable-seat
classification, and the phase's complete gate history must contain no model
`go` or `block`. Mixed decisions, substantive prior verdicts, passed gates,
missing gate records, and gate-less legacy runs remain complete and are not
retried.

Gate recovery reviews the latest recorded output artifact for the workflow
role without rerunning its producer. Artifact bytes come from the stored run;
checks and model seats come from the current workflow and registry. A missing
or mismatched stored artifact fails closed. The worktree used by checks is a
fresh checkout at the git SHA recorded in the run manifest, not current
`HEAD`, so repository drift cannot change what the recovered gate checks.
Infrastructure-only attempts advance the append-only gate round history but do
not consume substantive round or tier budget. A tier gate restarts at its
current configured tier, and an inline gate uses its current inline seats.

`--task` and `--run-id` are ignored when `--resume` is set (both come from
the existing partial run). The resumed run is the same run ref: it gains
additional commits, one per newly-completed stage.

Failed-stage status is not yet durably recorded in the run manifest (tracked
in etude-dp7). `etude run show` on a partial run shows the successfully-
completed stages only; the frontier stage has not been captured.

## Current limits

- Runs are listed by walking `refs/etude/runs/*` directly. There is no query
  index; performance degrades with a large number of runs.
