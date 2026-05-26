# Bench

`etude bench` benchmarks a stage by replaying it across a cohort of recorded
runs and judging each replay against the original, then reporting an aggregate
win rate plus per-run reasoning. It answers: "if I re-run this stage with a new
skill version (or runner), does it beat the original work?"

```bash
etude bench <stage> --last 10 --runner ./run.sh --judge ./judge.sh
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

## Cohort selection

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
| `--runner <command>` | Runner command spec (whitespace-split into argv; no shell expansion). Falls back to `git config etude.runner`. Required (via flag or config). |
| `--judge <command>` | Judge command spec (whitespace-split into argv). Falls back to `git config etude.judge`. Required (via flag or config). |
| `--judge-model <model>` | Model passed to the judge process as `ETUDE_MODEL`. Falls back to `git config etude.judgeModel`. Empty is allowed (the judge command may encode its own model). This is the **referee** model and is independent of `--model`. |
| `--seed <n>` | Seed for per-pair presentation randomisation (position-bias mitigation). |
| `--timeout <duration>` | Per-invocation timeout applied to **both** the runner and the judge processes (default `10m`; `0` disables). Each process is killed when the timeout elapses. A small grace period bounds cleanup even if a process backgrounds a child that holds its output pipe open. Runner and judge output is also read through a hard size cap (default 64 MiB). |
| `--no-cache` | Force re-evaluation; skip the eval-result cache. |
| `--model`, `--skill-id`, `--skill-repo`, `--skill-version`, `--harness`, `--harness-version` | Override the corresponding field in the **contestant** (replay) producer. These describe the new skill being benchmarked; they never affect the judge. |

### Runner and judge contracts

Both `--runner` and `--judge` are external commands invoked with a restricted
environment and a working directory set to a throwaway scratch area, mirroring
[`etude replay`](replay.md#runner-io-contract). The judge additionally receives:

- `ETUDE_INPUTS_DIR` — a directory with the two presented targets as
  `00-target-left` / `01-target-right` (presentation order is randomised per
  pair to reduce position bias; the winner is mapped back to canonical A/B),
- `ETUDE_OUTPUT_FILE` — the path the judge must write its JSON verdict to
  (`{"winner": "A"|"B"|"tie", ...}`),
- `ETUDE_MODEL` — the `--judge-model` value.

Because the presentation order is randomised, a judge must decide the winner
from the target **content**, not from position.

## Caching

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

`etude bench` exits non-zero when: `--last <= 0`; no runner is configured; no
judge is configured; no run in the cohort qualifies; or every run failed to
evaluate. A run that fails mid-benchmark (replay, record, or judge error) is
reported under "failed runs" and does **not** abort the command — the remaining
runs are still benchmarked, and the command exits 0 as long as at least one
evaluation succeeded.

## Current limits

- Cohort selection is `--last N` only; there is no run-id list or filter yet.
- There is no `--json` / machine-readable output flag.
- Cache lookup is a linear scan of `refs/etude/evals/*`; there is no index.
- For non-deterministic (e.g. LLM) judges, reusing a cached verdict is a
  sampling shortcut; use `--no-cache` when you need a fresh judgement.
