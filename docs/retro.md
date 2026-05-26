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
```

The retro id is auto-assigned from the scope, primary subject, and timestamp.
Prints `captured <commit-sha>` and `ref refs/etude/retros/<id>` on success.

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
RETRO ID           SCOPE  TRIGGER  SUBJECTS  CREATED
run-abc-20260523   run    manual   run-1     2026-05-23T10:00:00Z
```

Columns:

| Column | Content |
|--------|---------|
| `RETRO ID` | The retro identifier |
| `SCOPE` | Scope value recorded at capture time |
| `TRIGGER` | Trigger value (e.g. `manual`, `scheduled`) |
| `SUBJECTS` | Comma-joined subject run ids and bead ids |
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

### Retro id validation

The retro id is validated before any git call. Invalid ids produce
`invalid retro id "<id>"` and a non-zero exit code. An unknown retro id prints
`retro "<id>" not found` to stderr and exits non-zero.

## Current limits

- Retros are listed by walking `refs/etude/retros/*` directly. There is no
  query index; performance degrades with a large number of retros.
- There is no `--json` or machine-readable output flag yet.
