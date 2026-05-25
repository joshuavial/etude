# etude example: tracker-agnostic summarize workflow

This directory is a self-contained, runnable demonstration that etude is a
**reusable git-native primitive**.  It works with nothing beyond git, bash, and
the `etude` binary — no beads, no xenota, no issue tracker, no LLM, no network.

## Run it

```bash
# From the repo root — builds etude from source automatically:
bash examples/summarize/walkthrough.sh

# Or against a pre-built binary:
ETUDE_BIN=./bin/etude bash examples/summarize/walkthrough.sh
```

## What the walkthrough does

Each step is echoed with a `==>` header so you can follow along.

The scenario models a **skill upgrade**: a v1 summarizer that emits only the
first line of a document is being replaced by a v2 summarizer that adds a word
count.  The bench proves the new skill is strictly better (longer output →
picked by the judge).

### Step 1 — `etude init`

Scaffolds `.etude/workflow.yaml` and rubric placeholders in the throwaway
repository.  Because the throwaway repo has no remote `origin`, the refspec
step is skipped — that is normal and expected.

Expected output contains:
```
created .etude/workflow.yaml
```

### Step 2 — generate v1 summaries and capture three original runs

The walkthrough first generates a **v1 (original)** summary for each document:
just the first line — a deliberately terse older-skill output.

```bash
head -n 1 docs/alpha.txt > summaries/alpha-v1.txt
```

Three calls to `etude capture summarize` then store those v1 summaries as
content-addressed artifacts under `refs/etude/runs/doc-alpha`,
`refs/etude/runs/doc-beta`, and `refs/etude/runs/doc-gamma`.

Run ids are **plain strings** (`doc-alpha`, `doc-beta`, `doc-gamma`).  There is
no issue-tracker ID here.  Compare `scripts/dogfood-capture.sh` in the repo
root, which uses `bd show <bead-id>` and is tightly coupled to the bead tracker.

Each capture call looks like:
```bash
etude capture summarize \
  --run doc-alpha \
  --input  doc=docs/alpha.txt \
  --output summary=summaries/alpha-v1.txt \
  --workflow summarize \
  --produced-by original
```

### Step 3 — inspect runs

`etude run list` prints a table of all stored runs:

```
RUN ID      WORKFLOW    CREATED               STAGES
doc-alpha   summarize   2026-...              1
doc-beta    summarize   2026-...              1
doc-gamma   summarize   2026-...              1
```

`etude run show doc-alpha` prints the detail view including the content-addressed
artifact paths, stage name, and producer metadata.

### Step 4 — replay

`etude replay doc-alpha summarize --runner ./summarize-runner.sh` re-executes
the summarize stage using the **v2 runner** (`summarize-runner.sh`): it reads
the `*-doc` file from `$ETUDE_INPUTS_DIR` and writes a richer summary — first
line plus word count — to `$ETUDE_OUTPUT_FILE`.

```
The history of computing is a story of abstraction.
[94 words]
```

Because the runner is a pure function of its inputs, the replayed output is
deterministic across machines.  No LLM, no randomness.

### Step 5 — bench

`etude bench summarize --last 3 --runner ... --judge ...` benchmarks the
summarize stage across the three-run cohort.  For each run it:

1. Replays the stage with the v2 runner.
2. Invokes the judge to compare the replay against the v1 original.

The judge (`pick-longer-judge.sh`) decides the winner by byte length — longer
content wins, equal lengths are a tie.  The v2 output (first line + word count)
is always longer than the v1 output (first line only), so the new skill wins
all three comparisons.

**Key caveat:** etude randomises which candidate occupies the left/right
position per pair (to reduce position bias).  The judge therefore cannot key
off file position.  The files are named `00-target-left` and `01-target-right`
in `$ETUDE_INPUTS_DIR`; the judge must decide from **content**.  After the judge
returns a position-relative winner (`A`=left, `B`=right), etude maps it back to
the canonical A=original / B=replay orientation.

Expected bench headline:
```
bench summarize: replay (new skill) wins 100.0% vs original
(B=3 A=0 tie=0) over 3 evals; 0 skipped, 0 failed
```

### Step 6 — maintenance

`etude reindex` rebuilds the SQLite query index at `.git/etude-index.db`.
`etude gc` prints a storage report (logical artifact bytes, run and eval counts).

## Files

| File | Purpose |
|------|---------|
| `walkthrough.sh` | End-to-end driver — run this |
| `summarize-runner.sh` | Deterministic v2 runner: first line + word count |
| `pick-longer-judge.sh` | Content-aware pairwise judge: longer wins |
| `docs/alpha.txt` | Sample document A |
| `docs/beta.txt` | Sample document B |
| `docs/gamma.txt` | Sample document C |

## Contrast with the dogfood (beads) usage

`scripts/dogfood-capture.sh` (in the repo root) is the tracker-coupled
counterpart.  It requires:

- `bd` (bead tracker CLI) to read design fields and task descriptions.
- A live bead ID and a specific commit SHA.
- Four stage captures that mirror the dev-workflow phases (plan, implement,
  verify, review).

This example requires **none of that**.  The run ids are arbitrary strings, the
runner is a shell one-liner, and the judge is content-based arithmetic.  etude
is a storage and evaluation layer — the tracker (or absence thereof) is entirely
up to you.
