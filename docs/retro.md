# Retros

`etude retro capture`, `etude retro generate`, `etude retro list`, and
`etude retro show` manage retro artifacts stored under `refs/etude/retros/*`.

## Capture a retro

```bash
etude retro capture <scope> --file retro.md --subject-run <run-id>
```

Stores a markdown retro body into a content-addressed artifact and writes a
`refs/etude/retros/<retro-id>` ref. `<scope>` is one of: `run`, `phase`,
`gate`, `cohort`, `bench`, `workflow`.

`--subject-run` is required for every scope except `workflow`. It may be
repeated. `--bead` can be supplied in addition to, or instead of,
`--subject-run` to record bead ids as subjects.

Useful flags:

```bash
--file <path>|-               # retro body (required)
--subject-run <run-id>        # subject run (repeatable)
--bead <bead-id>              # subject bead (repeatable)
--decision accepted|deferred|superseded|informational
--supersedes <retro-id>
--trigger manual              # default: manual
--gate <stage>@<attempt>      # for phase/gate scopes
--bench <bench-id>            # for bench scope
--eval <eval-id>              # for bench scope
--git-sha <sha>               # defaults to HEAD
--harness <name>
--harness-version <version>
--model <model-id>
--skill-id <id>               # default: retro
--message <text>              # git commit message override
--occurred-at <RFC3339>        # original event time (optional; see below)
--meta-file <path>|-          # optional retro-meta JSON sidecar
```

The retro id is auto-assigned from the scope, primary subject, and timestamp.
Prints `captured <commit-sha>` and `ref refs/etude/retros/<id>` on success.

### Original event time

`--occurred-at <RFC3339>` records the time the retro's events actually happened,
distinct from the capture timestamp (`created`). It is optional: if omitted,
`retro show` and `etude log` fall back to showing capture time.

```bash
etude retro capture run \
  --file retro.md \
  --subject-run run-abc \
  --occurred-at 2026-03-15T10:30:00Z
```

When set:
- `retro show` prints an `occurred:` line immediately after `created:`.
- `etude log` shows the event time in the `EVENT` column and sorts the retro
  chronologically by event time (so a backfilled retro lands among the runs it
  retrospects, not at migration time). The `TIMESTAMP` column always shows the
  capture time.

The value must be a valid RFC3339 timestamp (e.g. `2026-03-15T10:30:00Z` or
`2026-03-15T10:30:00+09:00`). A malformed value is rejected with a clear error
before any ref is written.

Both `retro capture` and `retro generate` accept `--occurred-at`.

#### Relationship to the sidecar `original_event_date`

The cadence retro-meta sidecar (see below) has an `original_event_date`
(`YYYY-MM-DD`) field that serves a similar purpose as a human-readable date
for downstream analysis. The two fields are independent:

- `occurred_at` (manifest field): canonical RFC3339 instant, used by
  `retro show`, `etude log` EVENT column, and effective-time sort. This is the
  machine-readable source of truth for etude-native tooling.
- `original_event_date` (sidecar): calendar date string, used by downstream
  cross-retro analysis tools and checked by the dogfood completeness audit.
  Etude core never parses the sidecar — it stores it verbatim.

When both are set they SHOULD agree on the date (i.e. `occurred_at`'s date
portion should match `original_event_date`). They do not auto-sync; the
backfill bead (etude-8hq.5) should set both — `--occurred-at` for the core
field and `--meta-file` for the sidecar — so they stay coherent.

### Retro-meta sidecar

`--meta-file <path>|-` attaches an optional JSON sidecar (read from a file or
stdin) alongside the retro body. The body is stored as the first stage
(`stage: retro`, `text/markdown`); when a sidecar is supplied it is stored as a
second `retro-meta` stage (`application/json`) in the same manifest. Omitting
`--meta-file` produces the usual single-stage manifest, so existing captures are
unchanged.

The sidecar must be well-formed JSON: malformed input is rejected before any ref
is written, with `retro meta file "<path>" is not valid JSON` and a non-zero
exit code. The schema of the JSON is not interpreted by etude — it is stored
verbatim for downstream tooling (e.g. failure-mode indexing). `retro show`
renders the sidecar in a `--- retro meta ---` section after the body (see
[Show a retro](#show-a-retro)), and `retro list` flags its presence in the
`META` column.

For cadence retros that follow the 7-key convention below, `make retro-index`
runs `scripts/retro-meta-index.sh` — a read-only dogfood-layer aggregation that
emits a cross-retro index of failure modes, root causes, follow-up beads, and a
durable-changes timeline across all current (non-superseded) cadence-retro refs.

#### Cadence retro-meta convention (dogfood)

The schema of the JSON is not interpreted by etude — it is stored verbatim for
downstream tooling (e.g. failure-mode indexing). However, this project's
**cadence retros** follow a specific 7-key convention that is documented here
and enforced by `scripts/dogfood-completeness-audit.sh` check (f). This is a
**dogfood process convention**, not an etude-core schema constraint: `etude
retro capture` validates only that the sidecar is well-formed JSON (`json.Valid`)
and stores the bytes verbatim. The 7-key requirement is checked by the dogfood
audit script reading the retro manifest from git.

A cadence retro-meta sidecar is a JSON object with these required keys
(presence + type checked; values are never constrained; arrays may be empty):

| Key | Type | Meaning |
|-----|------|---------|
| `retro_type` | string | marks this as a cadence retro sidecar; use `"cadence"` |
| `original_event_date` | string | date the retro's events actually occurred (`YYYY-MM-DD`); distinct from the capture timestamp |
| `failure_modes` | array of strings | distinct failure modes identified (may be `[]` for a clean cohort) |
| `root_causes` | array of strings | underlying process/skill/tool/context causes (may be `[]`) |
| `follow_up_beads` | array of strings | bead ids spun off by the retro (may be `[]`) |
| `decisions` | array of strings | decisions or rule-changes the retro landed (may be `[]`) |
| `durable_changes` | array of strings | concrete skill/formula/doc/script edits that landed as a result (may be `[]`); captures what actually changed, distinct from decisions (intent) and follow-up beads (future work) |

All seven keys must be present and of the correct type. Additional keys are
allowed and ignored. A canonical example is committed at
`scripts/retro-meta-cadence.example.json`. See
`docs/plans/dogfood/retro-ledger.md` for the cadence capture rule.

```json
{
  "retro_type": "cadence",
  "original_event_date": "2026-05-27",
  "failure_modes": ["audit-check-missing"],
  "root_causes": ["convention existed only in prose, not machine-checked"],
  "follow_up_beads": ["etude-8hq.5"],
  "decisions": ["7-key sidecar required for all cadence retros from 2026-05-27"],
  "durable_changes": ["dogfood-completeness-audit.sh check (f) added"]
}
```

**Enforcement:** `scripts/dogfood-completeness-audit.sh` check (f) (`cadence-sidecar`)
runs in batch mode (`--last`/`--since`/default). For each `trigger==cadence-retro`
retro ref:
- **Superseded PRE-cutoff refs are skipped:** if a newer retro names a
  *pre-cutoff* ref in its `refs.supersedes` field, that old ref is excluded from
  check (f). This is the canonical way to clear a ref from the backfill worklist —
  re-capture the retro with `etude retro capture --supersedes <old-id>
  --meta-file <sidecar.json>`, which creates a new ref (post-convention, with a
  valid sidecar) and marks the old ref superseded. The new ref passes check (f);
  the old ref is skipped. **POST-cutoff cadence refs are NEVER skipped by
  supersession** — they are always validated, so a missing-sidecar hard gap can
  never be hidden by superseding a post-cutoff retro with a non-validating ref.
- **POST-convention** (captured on or after `2026-05-27T00:00:00Z`): missing or
  malformed sidecar is a **hard gap** (exit 1).
- **PRE-convention** (captured before the cutoff): missing or malformed sidecar
  is a **WARN** (exit 0) — these are the backfill worklist for etude-8hq.5.

Checks (c) and (g) also skip superseded refs by the same mechanism.

Check (f) is not run in `--bead` mode (which is per-bead, not per-retro).

## Generate a retro

```bash
etude retro generate <scope> \
  --subject-run <run-id> [--subject-run <run-id>...] \
  --generator <script> \
  [--stage <name>] \
  [--trigger <name>]
```

Invokes an external generator script over the materialized artifacts of the
named subject runs, then stores the produced markdown body exactly as `retro
capture` does. `<scope>` is one of: `run`, `phase`, `gate`, `cohort`, `bench`,
`workflow`.

`--subject-run` is required for every scope except `workflow` and may be
repeated to cover multiple runs in one retro. `--bead` and the other producer
flags accepted by `retro capture` are also accepted here.

### Generator script contract

The script is run headlessly with a strict environment:

| Variable | Content |
|---|---|
| `ETUDE_INPUTS_DIR` | Directory containing the subject runs' materialized artifacts. Each subject contributes `<NN>-<runid>-output` (the stage output) and `<NN>-<runid>-input-<role>` files (stage inputs), ordered by the position the `--subject-run` flag was given. |
| `ETUDE_OUTPUT_FILE` | Path the script **must** write the retro markdown body to before exiting. |

Only `PATH`, `ETUDE_INPUTS_DIR`, and `ETUDE_OUTPUT_FILE` are present in the
environment — no other parent-process variables are forwarded. The working
directory is a fresh temp directory.

A non-zero exit code is treated as `ErrGeneratorFailed` and the error is
reported to stderr with the trimmed stderr output of the script. The command
aborts if the output file is missing or is not a regular file after the script
exits.

The generator can be set via `--generator <spec>` (e.g. `--generator
./scripts/retro.sh`) or via git config:

```bash
git config etude.retroGenerator ./scripts/retro.sh
```

`--generator` takes precedence over git config. An error is returned when
neither is set.

### Generator timeout and output limits

`--timeout <duration>` bounds each generator invocation (default `10m`; `0`
disables). The generator process is killed when the timeout elapses and the
command returns a "timed out" error; a small grace period bounds cleanup even if
the generator backgrounds a child that holds its output pipe open. The
generator's output file is read through a hard size cap (default 64 MiB) — an
output exceeding the cap is rejected rather than read into memory unbounded.

### Stage selection

When a subject run has exactly one stage, that stage is selected automatically.
When a subject run has multiple stages, `--stage <name>` is required:

```bash
# Single-stage run: stage is auto-selected
etude retro generate run --subject-run my-run-abc --generator ./retro.sh

# Multi-stage run: --stage required
etude retro generate run --subject-run my-run-xyz --stage implement \
  --generator ./retro.sh
```

Omitting `--stage` for a multi-stage run produces an error that lists the
available stage names.

### Provenance

Generated retros record two extra metadata keys visible in `retro show`:

```
metadata:
  generator: ./scripts/retro.sh
  produced_via: generate
```

`produced_via=generate` distinguishes generated retros from captured retros
(`retro capture` does not set either key). `generator` records the spec that
was used.

### Output

On success the command prints:

```
generated <commit-sha>
ref refs/etude/retros/<retro-id>
```

Generated retros are stored under the same `refs/etude/retros/*` namespace as
captured retros, so `retro list` and `retro show` treat them uniformly.

### `--trigger` flag

`--trigger` is a free-form label recorded in the manifest (default: `manual`).
Setting a trigger value does not automate anything — config-driven automated
triggers are not yet implemented.

## List retros

```bash
etude retro list
```

Prints a tab-aligned table of all stored retros ordered lexically by retro id:

```
RETRO ID           SCOPE  TRIGGER  SUBJECTS  META  CREATED
run-abc-20260523   run    manual   run-1     N     2026-05-23T10:00:00Z
```

Columns:

| Column | Content |
|--------|---------|
| `RETRO ID` | The retro identifier |
| `SCOPE` | Scope value recorded at capture time |
| `TRIGGER` | Trigger value (e.g. `manual`, `scheduled`) |
| `SUBJECTS` | Comma-joined subject run ids and bead ids |
| `META` | `Y` when the retro carries a `retro-meta` JSON sidecar, else `N` |
| `CREATED` | Creation timestamp in RFC3339 UTC |

When there are no retros, the command prints `no retros found` and exits 0.

`retro list` takes no arguments or flags.

If any retro's `manifest.json` is missing or cannot be parsed, the command
fails with a non-zero exit code, names the offending retro id in an error
written to stderr, and prints nothing to stdout.

## Show a retro

```bash
etude retro show <retro-id>
```

Prints the full detail of one retro, including metadata and the inline retro
body. Example:

```
retro id:  run-abc-20260523
scope:     run
trigger:   manual
decision:  accepted
created:   2026-05-23T10:00:00Z
  subject: run-1
producer:  dev-workflow 1.2.0 claude-sonnet-4-6 retro@manual
--- retro body ---
<markdown content>
```

When `decision` or `supersedes` are absent from the retro, those lines are
omitted. The `producer:` line is omitted for retros that did not record
harness, model, or skill.

When the retro carries extra metadata keys (beyond scope, trigger, decision,
supersedes, and subject fields), they are printed in a `metadata:` block in
sorted key order.

### Retro-meta sidecar rendering

When the retro carries a `retro-meta` JSON sidecar (captured with
`--meta-file`), `retro show` prints it in a `--- retro meta ---` section after
the body, pretty-printed with two-space indentation:

```
--- retro body ---
<markdown content>
--- retro meta ---
{
  "failure_modes": [
    "flaky-gate"
  ],
  "root_causes": [
    "missing newline guard"
  ]
}
```

The section is omitted entirely for retros with no sidecar, so existing retros
render exactly as before. The stage is located by its `retro-meta` artifact
role, not by position. If the stored JSON cannot be re-indented it is printed
verbatim.

### Retro id validation

The retro id is validated before any git call. Invalid ids produce
`invalid retro id "<id>"` and a non-zero exit code. An unknown retro id prints
`retro "<id>" not found` to stderr and exits non-zero.

## Retro nudge

`etude retro nudge` is an opt-in reminder that surfaces when the number of
captured runs since the most recent retro reaches a configured threshold.
The reminder is a single line written to **stderr** by any `etude` invocation
when it is overdue, so it stays out of machine-readable stdout (including
`etude run show --json`).

### Enable the nudge

The nudge is off by default. Enable it in `.etude/workflow.yaml` under the
existing `retros:` block:

```yaml
retros:
  on_run_close: false
  nudge:
    enabled: true     # default: false
    threshold: 3      # default: 3 runs since the most recent retro
```

`threshold` is optional; the default is **3**. When `enabled: true` and
`threshold` is set explicitly, it must be `>= 1` or the workflow fails
validation. With no `nudge:` block at all, etude behaves exactly as it did
before this feature: zero ref I/O, zero stderr.

### When the nudge fires

The trigger counts runs under `refs/etude/runs/*` whose `Created` time is
strictly after the most recent retro's `Created` time (any scope). With zero
retros the count is the total run count. When that count is `>= threshold`,
the reminder appears on stderr after the subcommand finishes:

```
etude: retro nudge: 4 bead(s) since last retro (threshold 3); run `etude retro generate workflow` or `etude retro nudge dismiss` to silence for now
```

The nudge fires regardless of whether the subcommand succeeded or failed,
but is suppressed for `--help`, `-h`, `--version`, `etude help`, the bare
`etude` invocation (no subcommand), `etude completion …`, the hidden
`etude __complete …` path used by shell tab-completion, and the entire
`etude retro nudge` subtree (so `status` JSON output is never crowded by a
stderr line).

The nudge is a **best-effort side channel**: a missing or malformed
`workflow.yaml`, a missing snooze file, a corrupted snooze file, a refstore
read failure, or a cwd outside a git repository all silently disable the
nudge for that invocation — they never alter the parent command's exit
status or output.

### Snooze (dismiss for the next N beads)

When a nudge appears at an inconvenient moment, dismiss it:

```bash
etude retro nudge dismiss          # snooze for the next 1 bead
etude retro nudge dismiss --for 3  # snooze for the next 3 beads
```

`dismiss` records the current run count and the snooze depth into
`.git/etude/retro-nudge-snooze.json` (local-only, not committed). The nudge
stays silent while `runs_since_last_retro - runs_at_snooze < snooze_for`,
i.e. until enough new beads have accumulated to land beyond the snooze
window.

The snooze auto-invalidates when a new retro lands: the recorded
`last_retro_id_at_snooze` no longer matches the current latest retro id,
so the next over-threshold event fires fresh.

> **Linked-worktree limitation (v1):** the snooze file lives at
> `<repo>/.git/etude/retro-nudge-snooze.json`. In a linked git worktree
> (where `<worktree>/.git` is a file, not a directory) the write will fail
> and the snooze will not persist. The nudge itself still works in linked
> worktrees — only the dismiss-snooze does not. A follow-up bead can resolve
> the real git directory via `git rev-parse --git-common-dir`.

### Inspect the current decision

```bash
etude retro nudge status
```

Prints a JSON object to stdout:

```json
{
  "enabled": true,
  "threshold": 3,
  "runs_since_last_retro": 4,
  "last_retro_id": "retro-run-bead-x-20260530T100000Z",
  "overdue": true,
  "snoozed_until_runs": 0,
  "would_emit": true
}
```

`snoozed_at` (RFC3339) appears as an additional field only when a snooze is
currently active. `status` exits 0 in the happy path; a refstore failure or
a corrupted snooze file surfaces as a non-zero exit so the operator notices.
It prints `enabled: false` and `would_emit: false` when there is no `nudge:`
block (or no `workflow.yaml` at all).

The status subcommand reads the same workflow and refs the root-command
emitter uses, so it is the authoritative way to ask "why didn't I see a
nudge?" or "why am I seeing one?".

## Current limits

- Retros are listed by walking `refs/etude/retros/*` directly. There is no
  query index; performance degrades with a large number of retros.
- There is no `--json` or machine-readable output flag yet for `retro show`
  or `retro list` (the nudge `status` subcommand is the only JSON output in
  this area today).
