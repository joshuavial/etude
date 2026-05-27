# Log

`etude log` narrates captured work as a single chronological timeline across
runs (`refs/etude/runs/*`) and retros (`refs/etude/retros/*`). It is read-only:
it walks the refs, reads each `manifest.json`, and prints a table — it never
writes a ref or mutates the repository.

## Show the timeline

```bash
etude log
```

Prints a tab-aligned table of run and retro events, ordered **ascending by
effective time** (oldest first, so the narration reads forward):

```
TIMESTAMP             KIND   ID                               EVENT                 SUMMARY
2026-05-23T10:00:00Z  run    run-1                            -                     manual (2 stages)
2026-05-23T11:30:00Z  run    run-2                            -                     manual (1 stages); 3 gates
2026-05-23T12:00:00Z  retro  retro-run-run-1-20260523T120000  -                     scope=run trigger=manual subjects=run-1
2026-06-01T09:00:00Z  retro  retro-run-run-1-20260601T090000  2026-05-23T10:00:00Z  scope=run trigger=manual subjects=run-1
```

Columns:

| Column | Content |
|--------|---------|
| `TIMESTAMP` | Manifest `created` time (capture time) in RFC3339 UTC — always shown, never replaced |
| `KIND` | `run` or `retro` |
| `ID` | The run id or retro id |
| `EVENT` | The `occurred_at` event time in RFC3339 UTC when set (backfilled retros), or `-` when absent (all runs, un-backfilled retros) |
| `SUMMARY` | For a run: `<workflow> (<n> stages)`, plus `; <n> gates` when the run carries review-gate attempts. For a retro: `scope=<scope> trigger=<trigger> subjects=<comma-joined subjects>`, with any empty field omitted. |

Events are sorted by **effective time** ascending: `occurred_at` when present,
otherwise `created` (capture time). This places a backfilled retro
chronologically among the runs it retrospects rather than clustering at
migration time. Ties (equal effective times) are broken deterministically by
`(kind, id)` so the output is stable.

The `TIMESTAMP` column always shows capture time (`created`). The `EVENT`
column reveals when the events being retrospected actually occurred — distinct
from when the retro was written. For runs and retros without `occurred_at`,
`EVENT` is `-` and the effective sort time equals the capture time, so the
output is identical to the pre-`occurred_at` behavior (except for the added
`EVENT=-` column).

When there are no events (after any filters), the command prints
`no events found` and exits 0.

## Filters

```bash
etude log --kind run                       # only run events
etude log --kind retro                      # only retro events
etude log --subject run-1                   # events involving subject run-1
etude log --subject run-1 --subject bead-7  # union of either subject
etude log --limit 20                        # the 20 most-recent events
```

| Flag | Effect |
|------|--------|
| `--kind <run\|retro>` | Repeatable. Include only the named kinds. An unrecognized value is rejected (`invalid --kind "<v>"`) before any git call, with nothing written to stdout. |
| `--subject <id>` | Repeatable. Include only events whose subject set contains `<id>`. A **run**'s subject set is its own run id; a **retro**'s subject set is its `subject_run.N`/`bead.N` values (not its own retro id). An event matches if it contains any of the given subjects. |
| `--limit <n>` | Keep only the most-recent `n` events — i.e. the **tail** of the ascending-sorted list, so the newest events are printed last (nearest the prompt). `0` (the default) means unlimited. A negative value is rejected. |

## Errors

If any run or retro `manifest.json` is missing or cannot be parsed, the command
fails with a non-zero exit code, names the offending ref id in an error written
to stderr (e.g. `run "run-x": ...`), and prints nothing to stdout. The full
event set is assembled before any row is written, so a malformed manifest never
produces partial output.

`etude log` takes no positional arguments.

## Current limits

- **Gate attempts are not first-class timeline rows.** Review-gate attempts are
  stored as a field on the run manifest (`gates`), not as their own refs, so
  they have no independent identity to narrate. The run event's summary notes
  the gate count (`; <n> gates`). A dedicated gate-attempt event kind is
  deferred; it would require either a gate ref namespace or a documented
  secondary sort over each gate's timestamp.
- **Evals are not included.** The timeline covers runs and retros only;
  `refs/etude/evals/*` are not narrated. Adding them later is additive.
- **No time-range or workflow-type filters yet** (`--since`/`--until`,
  workflow filter) and **no `--json`** machine-readable output. These are
  deferred to follow-ups.
- The timeline is built by walking `refs/etude/runs/*` and
  `refs/etude/retros/*` directly; there is no query index, so performance
  degrades with a very large number of refs.
