# Bench

`etude bench` has two mutually exclusive modes:

- replay a stage across recorded runs and compare each replay with the original;
- evaluate gate-prompt variants against recorded stage artifacts and compare
  their verdicts with explicit or proxy labels.

Replay mode answers: "if I re-run this stage with a new skill version (or
runner), does it beat the original work?"

```bash
etude bench <stage> --last 10 \
  --runner /absolute/path/to/run.sh \
  --judge /absolute/path/to/judge.sh
```

For each qualifying run in the cohort, `etude bench`:

1. Replays `<stage>` at the run's recorded git SHA via `--runner`, and records
   the replay as a new linked run (so its output is content-addressed and
   commit-pinned, like `etude replay --record`).
2. Builds a **pairwise** evaluation with two targets — A = the original stage
   output, B = the replayed output — and invokes `--judge` to pick a winner.
3. Caches the judge verdict so an identical comparison is not re-judged (see
   [Caching](#caching)).

It then aggregates the per-run winners into a win rate and prints a report.

## Gate-prompt variants

Gate mode answers: "which gate prompt best catches known problems without
over-blocking clean work?"

```bash
etude bench --gate plan \
  --variant prompts/plan-a.md \
  --variant prompts/plan-b.md \
  --cohort run-refs \
  --labels labels.json \
  --judge /absolute/path/to/judge.sh
```

`--gate <phase>` switches the existing `bench` command into gate mode; the
phase is the workflow stage name and is the same value used by a label's
`stage` field. Gate
mode takes no positional `<stage>` argument and rejects replay-only flags such
as `--runner`, `--seed`, producer overrides, and `--no-cache`. Replay mode, in
turn, rejects `--variant`, `--cohort`, and `--labels` rather than silently
ignoring them.

Gate mode requires at least two `--variant` prompt files. Each path is made
absolute, cleaned, and symlink-resolved before distinctness is checked, so
different spellings or symlinks to one file do not count as separate variants.
Every prompt must be readable and contain non-whitespace text. The original
flag value remains the variant's display name in the report.

For every `(variant, fixture)` cell, bench invokes the configured judge through
the gate evaluator with one target artifact and exactly one `gate-prompt`
context input. The judge writes a gate result:

```json
{"passed": false, "findings": [{"severity": "error", "message": "missing rollback plan"}]}
```

`findings` is optional. Each finding requires a non-empty `message` and a
`severity` of `info`, `warning`, or `error` (there is no default), and may
include an optional `pointer` artifact locator.

`passed: true` maps to `go`; `false` maps to `block`. The command prints every
successful and failed cell, including the run id, stage, round, predicted and
expected verdicts, label source, verification status, confusion-matrix outcome,
common/excluded status, and first finding or error.

### Gate cohort

`--cohort run-refs` is the built-in selector. It reads the `--last N` most
recent replayable artifacts for the requested phase from `refs/etude/runs/*`,
in deterministic newest-first order (run id breaks timestamp ties). Gate bench
is read-only: "replayable" describes the recorded artifact's storage form, not
an action gate bench performs. Evaluating cells does not create replay runs,
read or write the replay evaluation cache, or create eval refs.

The run-refs source currently provides one final captured artifact per
qualifying run. Gate history supplies the fixture's final round and weak label
hint, but it does not reconstruct a different artifact body for every historical
round. Per-round artifact recovery requires a separate corpus adapter.

### Labels

`--labels` accepts strict JSON with this schema. `verified` is optional and
defaults to `false`; set it only when the expected verdict was independently
checked rather than inferred from gate progression:

```json
{
  "version": 1,
  "labels": [
    {
      "run_id": "run-123",
      "stage": "plan",
      "round": 2,
      "expected": "block",
      "verified": true,
      "note": "The plan omitted the required rollback path."
    }
  ]
}
```

The key is exactly `(run_id, stage, round)`, `round` must be positive, and
`note` is an optional human-readable annotation.
Unknown fields, duplicate keys, trailing JSON, unsupported versions, and verdicts
other than `go` or `block` are rejected. When a labels file is supplied, at
least one key must match the selected cohort; otherwise bench fails before
invoking the judge.

An explicit matching label takes precedence. A fixture without an explicit
match falls back to the run's gate-progression hint when one exists; fixtures
with neither remain unlabeled. Mixed cohorts are allowed, and each variant row
prints `EXPLICIT`, `PROXY`, and `UNLABELED` counts so the evidence composition
is visible.

> Progression labels are circular: a gate that historically over-blocked can
> score well against its own blocked rounds. Bench prints a loud warning whenever
> proxy labels contribute. Treat proxy results as exploratory, not calibration
> truth; use independently verified explicit labels for trustworthy comparisons.

### Comparable scores and cell failures

Variant rows report accuracy, catch rate, avoid-overblock rate, the confusion
matrix (`TP`, `FN`, `FP`, `TN`), and their denominators. Accuracy is the share
of scored labels predicted correctly. Catch rate is `TP / (TP + FN)`, and
avoid-overblock rate is `TN / (TN + FP)`. `block` is the positive class. When
the common fixture set has no labels, `SCORED` is zero, the confusion matrix is
empty, and the printed rates are `0.0%`; those rows describe execution coverage,
not measured prompt quality.

All variants use the same **common fixture set**: only fixtures for which every
variant returned a verdict contribute to aggregate rows. A judge failure in one
cell excludes that fixture from every variant's aggregate, while every success
and failure still appears in the cell table. The exclusions section names each
fixture key and the variants that failed, so a reduced denominator is
explainable. Complete but unlabeled fixtures count under `COMMON` and
`UNLABELED`, not `SCORED`.

If no fixture returns a verdict for every variant, bench prints all cell errors
and exclusions and exits non-zero instead of presenting incomparable rates.

## Replay cohort selection

The cohort is the `--last N` most-recent runs (by manifest creation time,
tie-broken by run id) that contain `<stage>` in a **replayable** form:

- the stage occurs exactly once in the run,
- its recorded git SHA is a syntactically valid OID,
- all of its inputs use inline content storage (pointer inputs are not
  replayable), and its output uses content storage (it is the eval target).

Runs whose stage was itself **produced by a replay** (`produced_by: "replay"`)
are excluded: a bench cohort is original work, not bench's own recorded
replays. Without this, each bench run would grow the cohort with its own output
and re-benchmark it recursively.

Non-qualifying runs are skipped and listed in the report with a reason
(`stage-missing`, `stage-ambiguous`, `no-git-sha`, `invalid-git-sha`,
`pointer-input`, `pointer-output`, `replay-run`); they are not errors. If no
run qualifies, the command fails with a non-zero exit code.

## Win rate

The headline number is the **replay (new skill) win rate**:

```
win_rate_B = (count(B wins) + 0.5 * count(ties)) / total
```

where A = original, B = replay, and `total` is the number of runs that produced
a successful evaluation. A **high** `win_rate_B` means the replayed/new skill is
beating the original. The report states the orientation explicitly and also
prints the raw A / B / tie counts.

> This is the complement of the `win_rate` defined in the evaluator contract
> (which is oriented toward A); bench reports the B-oriented rate because the
> question is whether the *new* skill wins.

The report is a human-readable table (source run → replay run, winner,
confidence, first finding, eval id), followed by the skipped runs and any runs
that failed mid-benchmark. A `CACHED` marker flags rows served from the cache.

## Flags

| Flag | Description |
|------|-------------|
| `--last <N>` | Number of most-recent qualifying runs to benchmark. Must be `> 0`. Default `10`. |
| `--gate <phase>` | Switch to gate-prompt benchmarking for the named phase. Takes no positional stage. |
| `--variant <file>` | Gate-prompt file. Repeat at least twice; paths must resolve to canonically distinct, readable, non-empty files. Gate mode only. |
| `--cohort run-refs` | Select the built-in run-ref corpus. Required in gate mode. |
| `--labels <file>` | Optional strict version-1 labels JSON. Explicit matches override progression proxies. Gate mode only. |
| `--runner <command>` | Runner command spec (whitespace-split into argv; no shell expansion). Falls back to `git config etude.runner`. Required (via flag or config). Replay mode only. |
| `--judge <command>` | Judge command spec (whitespace-split into argv). Falls back to `git config etude.judge`. Required (via flag or config). |
| `--judge-model <model>` | Model passed to the judge process as `ETUDE_MODEL`. Falls back to `git config etude.judgeModel`. Empty is allowed (the judge command may encode its own model). This is the **referee** model and is independent of `--model`. |
| `--seed <n>` | Seed for per-pair presentation randomisation (position-bias mitigation). Replay mode only. |
| `--timeout <duration>` | Per-invocation timeout applied to **both** the runner and the judge processes (default `10m`; `0` disables). Each process is killed when the timeout elapses. A small grace period bounds cleanup even if a process backgrounds a child that holds its output pipe open. Runner and judge output is also read through a hard size cap (default 64 MiB). |
| `--no-cache` | Force re-evaluation; skip the eval-result cache. Replay mode only. |
| `--model`, `--skill-id`, `--skill-repo`, `--skill-version`, `--harness`, `--harness-version` | Override the corresponding field in the **contestant** (replay) producer. These describe the new skill being benchmarked; they never affect the judge. Replay mode only. |

### External command contracts

The replay-only `--runner` and the judge used by either mode are external
commands invoked with a restricted environment and a working directory set to
a throwaway scratch area, mirroring
[`etude replay`](replay.md#runner-io-contract). The judge receives
`ETUDE_OUTPUT_FILE`, where it must write its JSON verdict, and `ETUDE_MODEL`,
the `--judge-model` value. In replay mode it additionally receives:

- `ETUDE_INPUTS_DIR` — a directory with the two presented targets as
  `00-target-left` / `01-target-right` (presentation order is randomised per
  pair to reduce position bias; the winner is mapped back to canonical A/B).

The replay judge writes `{"winner": "A"|"B"|"tie", ...}`. In gate mode,
`ETUDE_INPUTS_DIR` instead contains one target and one context file, and the
judge writes `{"passed": true|false, ...}`.

Because the presentation order is randomised, a judge must decide the winner
from the target **content**, not from position.

Command specs are whitespace-split into argv without shell expansion. Because
the process working directory is a throwaway scratch directory, use an absolute
executable path (or a command available on `PATH`); a repository-relative value
such as `./judge.sh` does not resolve against the caller's directory.
Prompt and labels paths are different: Etude reads `--variant` and `--labels`
itself before starting the judge, so their relative paths resolve from the
caller's working directory as usual.

## Replay caching

Each judge verdict is persisted as an eval result under
`refs/etude/evals/<eval-id>`. Before judging a pair, `etude bench` reuses an
existing verdict when one exists for an identical comparison. The cache key is:

- the method (`pairwise`),
- both targets' content artifact SHA-256 hashes (the content identity — equal
  hashes guarantee byte-identical judge inputs),
- the judge identity (a fingerprint of the judge command + judge model), and
- the seed (which fixes the per-pair presentation order).

Caching is only used for judges with a **known identity**: a judge whose
identity cannot be determined always re-evaluates, and its verdicts are never
reused by another judge. Pass `--no-cache` to force re-evaluation.

## Errors and exit codes

In replay mode, `etude bench` exits non-zero when: `--last <= 0`; no runner is
configured; no judge is configured; no run in the cohort qualifies; or every
run failed to evaluate. A run that fails mid-benchmark (replay, record, or judge
error) is reported under "failed runs" and does **not** abort the command — the
remaining runs are still benchmarked, and the command exits 0 as long as at
least one evaluation succeeded.

Gate mode also exits non-zero for invalid or incompatible arguments, prompt or
labels read/validation errors, an unknown or empty cohort, a supplied labels
file matching zero fixtures, no judge, or no fixture with successful verdicts
from every variant. Individual cell failures are reported and do not make the
command fail while at least one common fixture remains.

## Current limits

- Replay cohort selection is `--last N` only; gate mode currently supports only
  `--cohort run-refs` plus `--last N`. There is no run-id list or filter yet.
- The run-refs gate corpus exposes one final captured artifact per run, not a
  different artifact body for each historical gate round.
- There is no `--json` / machine-readable output flag.
- Cache lookup is a linear scan of `refs/etude/evals/*`; there is no index.
- For non-deterministic (e.g. LLM) judges, reusing a cached verdict is a
  sampling shortcut; use `--no-cache` when you need a fresh judgement.
