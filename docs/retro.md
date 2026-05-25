# Retros

`etude retro capture`, `etude retro list`, and `etude retro show` manage retro
artifacts stored under `refs/etude/retros/*`.

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
