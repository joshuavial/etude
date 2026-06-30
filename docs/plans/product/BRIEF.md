# etude

> Empirically test AI coding-agent skills by replaying them against your real past work.

*Status: design brief — this document is the plan; implementation has not started.*

For the current shipped scope and deferred items, see [V1-SCOPE.md](V1-SCOPE.md).

## 1. Problem

Improving the skills that drive a coding agent — planning, implementation,
test design, code review, docs — is currently guesswork. You edit a skill,
it *feels* better, and you ship it. There is no empirical signal that an
upgraded planning skill actually produces better plans, or that a new review
skill catches more than the old one.

The reason is that the **inputs and intermediate artifacts of past work are
not preserved**. A merged PR shows where the code started and where it ended,
but the first-draft plan, the test plan, the initial implementation, and the
original review are gone. You cannot re-run a skill against historical work
because the historical *input* to that skill no longer exists in a recoverable
form.

`etude` is a CLI primitive that fixes this: it captures the artifacts
produced at each stage of a development workflow as immutable, versioned
records, and lets you **replay** any stage with a different skill version and
**evaluate** whether the result improved.

## 2. Goals and non-goals

### Goals

- Make skill improvement **empirical**: "I upgraded my planning skill — here
  is its win rate against the last 10 plans" instead of a vibe.
- Preserve the **first-draft input** to every workflow stage, immutably, so
  any stage can be replayed against its original input.
- Be a **reusable, git-native primitive** — works in any git repo, no service
  to run, not coupled to any one issue tracker.
- Be **config-driven**: a repo declares its own workflow stages and eval
  methods; nothing is hardcoded.
- Be **open source** and modular — capture, replay, and eval are independent
  layers a user can adopt one at a time.

### Non-goals

- Not a task tracker. Beads / GitHub Issues keep that job; `etude`
  *references* their IDs but does not replace them.
- Not a CI system.
- Not an agent runtime. It *invokes* a skill to replay it; it does not host
  or schedule agents.
- Not a replacement for human PR review. It measures review skills; it does
  not gate merges.

## 3. Core concepts

| Concept | Definition |
|---|---|
| **Workflow** | An ordered set of stages, declared in `workflow.yaml`. The pipeline itself — not the runtime that executes it. |
| **Stage** | A unit of work modelled as `(input artifacts) → (output artifact)`. Has a name, declared inputs, an associated skill, and an optional eval config. |
| **Artifact** | An immutable, content-addressed blob: a plan, a diff, a test plan, a test diff, a review, a docs diff. |
| **Run** | One pass of a workflow for one unit of work (≈ one PR / bead). A run has a manifest tying each stage to its input artifacts, output artifact, and the git sha of the repo state at that stage. |
| **Replay** | Re-executing one stage of an existing run with a (possibly new) skill version, producing a new artifact from the *original* recorded inputs. |
| **Eval** | Scoring an artifact against a rubric, or comparing two artifacts pairwise, or running deterministic assertions. |
| **Cohort** | A selected set of runs to operate on as a batch (e.g. "last 10 runs with a `plan` artifact"). |
| **Bench** | Replay + eval one stage across a cohort to measure the effect of a skill change. The headline use case. |

### Producer identity

Each stage records four distinct provenance axes. Three live inside a `producer` block; the fourth (Workflow) is top-level manifest metadata:

| Axis | Key | Definition | Example |
|---|---|---|---|
| **Harness** | `producer.harness` | The agent runtime that executed the stage. | `claude-code 1.0` |
| **Model** | `producer.model` | The LLM the harness used. | `claude-opus-4-7` |
| **Skill** | `producer.skill` | The external skill identity: id, repo, and version (git sha or semver in the skill repo). | `{id: dev-planner, repo: codewithjv-agent-skills, version: a1b2c3d}` |
| **Workflow** | *(top-level)* | The pipeline definition: workflow name + version (hash of `workflow.yaml`). | `default @ <sha>` |

These are intentionally separate axes: the same skill can run under different harnesses or models; the same model can run different skills; a workflow version is independent of any individual skill version. Comparing two runs is unambiguous only when all four axes are recorded.

The single load-bearing abstraction is **stage = (inputs) → output**. Because
every stage explicitly declares its inputs, those exact inputs can be re-fed
to an upgraded skill. The dependency graph between stages *is* the workflow.

## 4. Architecture

### 4.1 Workflow config (lives on `main`)

The workflow definition and eval rubrics are things the team edits and
code-reviews, so they live in the normal working tree on `main`:

```
.etude/
├── workflow.yaml            # stage definitions
└── evals/
    ├── plan-rubric.md       # "what a good plan looks like for us"
    └── review-rubric.md
```

Example `workflow.yaml`:

> **Non-current.** This early six-stage `test-plan`/`test` listing (skills
> `dev-coder`/`dev-test-writer`) is a historical illustration and is NOT the
> shipped default. The canonical default that `etude init` scaffolds is now the
> five-stage Etude-native loop `plan → implement → verify → docs → review`
> (skills `dev-executor`/`dev-qa`) — see `workflow.Default()` and this repo's
> `.etude/workflow.yaml`. The block below is kept only to show the original
> design sketch.

```yaml
name: default
stages:
  - name: plan
    produces: plan
    inputs: [task]
    skill: dev-planner
    eval:
      method: rubric          # rubric | pairwise | assertion
      rubric: evals/plan-rubric.md
  - name: implement
    produces: diff
    inputs: [plan, repo-state]
    skill: dev-coder
  - name: test-plan
    produces: test-plan
    inputs: [plan, diff]
    skill: dev-test-writer
    eval:
      method: rubric
      rubric: evals/test-plan-rubric.md
  - name: test
    produces: test-diff
    inputs: [test-plan, diff]
    skill: dev-test-writer
  - name: review
    produces: review
    inputs: [diff, plan]
    skill: dev-pr-reviewer
    eval:
      method: pairwise
  - name: docs
    produces: docs-diff
    inputs: [diff]
    skill: dev-docs-writer
    optional: true
```

A different repo can drop `docs`, reorder stages, or add its own. xenota's
workflow, for example, adds `manual-test-plan` and `manual-test-execution`
stages between `test` and `review`.

### 4.2 Storage: a custom ref namespace

Captured data — immutable artifacts, run manifests, eval results — lives under
a dedicated git **ref namespace**, `refs/etude/*`. This follows DVC's
experiment storage (`refs/exps/*`) and the precedent of GitHub's `refs/pull/*`
and Gerrit's `refs/changes/*` — custom refs are the documented idiom for
"metadata that travels with a repo but is not part of its branch history."

This was chosen over both an orphan branch and the working tree:

- **vs. orphan branch:** an orphan branch shows up in `git branch -a`, invites
  accidental checkout or merge, and forces every writer to contend on a single
  branch tip. A ref namespace stays out of the branch/tag namespace entirely
  and lets each run write its own ref. DVC rejected branches for exactly these
  reasons.
- **vs. working tree (`.etude/` data committed to `main`):** that
  pollutes every PR diff and bloats `main`'s history permanently.

Layout — one commit per run, reachable only via its ref:

```
refs/etude/runs/<run-id>    -> commit; tree holds manifest.json + artifacts/<hash>
refs/etude/evals/<eval-id>  -> commit; tree holds the eval result
```

Each run is a standalone commit (no branch — reachable only through its custom
ref). Its tree carries `manifest.json` plus the content-addressed artifact
blobs for that run. Git deduplicates identical blobs at the object layer
automatically, so per-run trees cost nothing extra when artifacts repeat
across runs.

**Why this keeps every git-native advantage:**

- **Immutability is structural.** An artifact blob's content hash *is* its
  identity. A first-draft plan captured as a blob cannot be silently mutated.
- **Free input reconstruction.** Each stage records the `git_sha` of the repo
  at the time it ran. To replay, check out that sha in a throwaway worktree —
  the exact original repo state, no snapshotting needed.
- **Worktree-transparent.** Refs live in the shared `.git` dir, so an artifact
  captured in one workmux worktree is visible in all of them — matters for
  xenota's parallel swarm.
- **Per-run atomic writes.** Each run writes its own ref, and git ref updates
  are atomic per-ref. A parallel swarm capturing many runs at once never
  contends on a shared tip — strictly better than an orphan branch.

**The cost — and it is real — is that custom refs do not sync with
`git push`/`fetch` by default.** This is the same issue that kept `git notes`
obscure. The mitigation, following DVC, is to make sync a **first-class CLI
concern**: `etude init` writes the fetch refspec
(`+refs/etude/*:refs/etude/*`) into the repo's git config, and
`etude sync` does explicit push/fetch of the namespace. Discoverability
is the tool's job, never assumed of the user.

Cloudflare Artifacts (2025, closed beta) is sometimes cited as prior art, but
on inspection it is a *hosted, proprietary* service for provisioning
agent-scale git repos — not a local primitive, not self-hostable, and it does
no capture/replay/eval. It is adjacent inspiration only. The one validated
idea worth borrowing: it uses standard **git-notes** to attach harness
metadata (prompts, run IDs, model output) to commits without mutating them.
We can use the same trick for *discoverability* — a lightweight note on a PR's
merge commit pointing at its `refs/etude/runs/<id>`, so a run is visible
from plain `git log --notes` even to someone without the tool installed. That
is an optional enhancement, not a change to the storage model above (a run
bundles artifacts across multiple shas, which a single note cannot hold).

### 4.3 Artifacts

Each artifact is a content-addressed blob (sha256 of content). The blob is
**pure content** — the markdown of a plan, the unified diff, the review text.
Type and role metadata travel in the manifest, not in the blob, so identical
artifacts dedupe for free.

Large binary artifacts (manual-test screenshots, long transcripts) should be
stored **by reference**, not inlined — the run tree holds a pointer, the bytes
live in external object storage. Inlining binaries bloats `.git` permanently
because objects cannot be pruned while a ref reaches them.

### 4.4 Run manifest

`runs/<run-id>/manifest.json`:

```json
{
  "manifest_version": 2,
  "run_id": "20260517-xc-m6f8",
  "workflow": "default",
  "workflow_version": "<hash of workflow.yaml at capture time>",
  "created": "2026-05-17T09:12:00Z",
  "refs": {
    "pr": "469",
    "bead": "xc-m6f8",
    "branch": "harbor/xc-m6f8-reject-dirty-slash-buffers"
  },
  "stages": [
    {
      "stage": "plan",
      "produced_by": "original",
      "git_sha": "442755e5...",
      "producer": {
        "harness": {
          "name": "claude-code",
          "version": "1.0"
        },
        "model": "claude-opus-4-7",
        "skill": {
          "id": "dev-planner",
          "repo": "codewithjv-agent-skills",
          "version": "a1b2c3d"
        }
      },
      "inputs": [
        {
          "role": "task",
          "artifact": "abcdef...",
          "path": "artifacts/sha256/ab/abcdef...",
          "media_type": "text/markdown; charset=utf-8",
          "storage": "content",
          "size": 1234
        }
      ],
      "output": {
        "role": "plan",
        "artifact": "123456...",
        "path": "artifacts/sha256/12/123456...",
        "media_type": "text/markdown; charset=utf-8",
        "storage": "content",
        "size": 5678
      },
      "timestamp": "2026-05-17T09:12:00Z"
    }
  ]
}
```

Notes:

- `manifest_version` versions the on-disk document format. `0` (absent) is the
  legacy format with a top-level per-stage `skill` block; `2` is the current
  format with a nested `producer` block. (Version 1 is never emitted; the
  transition goes directly 0 → 2.)
- `produced_by` is `original` or `replay` — replayed stages are recorded the
  same way as captured ones, so a replay is just another stage entry (or
  another run linked to the parent).
- `producer` records all four provenance axes for the stage (see §3):
  `harness` is the agent runtime; `model` is the LLM; `skill` is the external
  skill identity. The top-level workflow + `workflow_version` are the fourth
  axis.
- `producer.skill.version` is an **external skill identity** — a sha or semver
  in the skill repo (`~/.claude/skills`, `codewithjv-agent-skills`), not a sha
  in this repo. This is a deliberate provenance design point.
- `refs` values are always strings (e.g. `"pr": "469"`, not `"pr": 469`).
- `repo-state` inputs are recorded as a `git_sha` on the stage, not an artifact
  hash — the repo is already perfectly versioned by git.
- Each artifact entry carries `role`, `artifact` (sha256 hex), `path`
  (content-addressed path in the run tree), `media_type`, `storage`
  (`"content"` or `"pointer"`), and `size`.
- Stages produced by replay carry an optional `replay_of` object:
  `{"run_id": "<source-run-id>", "stage": "<source-stage>", "commit": "<git-oid>"}`.
  `commit` is the immutable git commit of the source run ref, pinning the link
  durably. `produced_by: "replay"` and `replay_of` are bidirectionally required:
  each implies the other and the manifest validator rejects any stage that has
  one without the other.

### 4.5 Query index (rebuildable cache)

Walking every ref to answer "last 10 runs with a `plan` artifact" is fine for
hundreds of runs, slow for tens of thousands. A **SQLite file is kept as a
derived cache**, not a source of truth: `.git/etude-index.db` (inside
`.git`, so it is local and never in the working tree). The `refs/etude/*`
namespace is authoritative; `etude reindex` rebuilds the cache from it.
This avoids SQLite's poor git-merge behavior entirely.

### 4.6 Capture

Two capture paths:

1. **Live capture** — an adapter hooks into the workflow as it runs and
   snapshots each stage's artifact the moment it is produced. High fidelity.
2. **Backfill / import** — `etude import` reconstructs runs from
   historical PRs. Honest limitation: old PRs reliably yield the **final
   diff** and **PR body**, so they are good for `review` and `docs` eval
   cases, but first-draft plans and implementations were never saved, so old
   PRs are weak for `plan` and `implement` eval.

### 4.7 Replay

`etude replay <run-id> <stage>` re-executes one stage end-to-end:

1. Read the run manifest; find the named stage entry.
2. Check out the recorded `git_sha` in a throwaway git worktree.
3. Resolve the recorded input artifacts and feed them to the runner via the
   **skill-runner adapter** (`--runner` or `git config etude.runner`).
4. Emit the produced output to stdout or `--output <path>`.

Without `--record`, no data is persisted; the command is emit-only.

With `--record`, the output is also persisted as a **new linked run**:

- The new run id is `<source-run-id>-replay-<yyyymmddThhmmssZ>` (UTC), with a
  numeric suffix (`-2` through `-10`) on collision.
- The new run contains a single stage: same name as the source, `produced_by:
  "replay"`, the source stage's `git_sha`, inputs copied verbatim from the
  source, and a `replay_of` link (`{run_id, stage, commit}` — see §4.4).
- The source commit is pinned in `replay_of.commit` for durable linkage.
- The source run is never modified.
- Producer-override flags (`--skill-version`, `--skill-id`, `--skill-repo`,
  `--model`, `--harness`, `--harness-version`) let the caller record a
  different producer identity (skill, model, and/or harness) for bench; unset
  fields inherit from the source.

Bench (`etude bench`) has shipped, backed by the `internal/eval` evaluator
library. A standalone `etude eval` CLI remains future work.

### 4.8 Eval

One `Evaluator` interface — `(artifact, context) → score + structured
findings` — with three shipped implementations:

- **rubric** — an LLM scores an artifact against criteria *you* wrote in a
  versioned rubric file. The rubric file *is* "what a good plan looks like for
  your org" — the thing you evolve. The eval result records which rubric
  version was used, so a rubric edit does not silently break comparability.
- **pairwise** — an LLM is given two artifacts and picks the better one with
  reasons. No ground truth needed. Order is randomized (and optionally run
  twice with swapped order) to counter position bias.
- **assertion** — deterministic checks (a test plan exists; it mentions every
  changed file).

Default to **pairwise** for the skill-upgrade question — it directly answers
"is the new skill better" with no ground truth. Use **rubric** for tracking
absolute quality over time and surfacing *why*.

## 5. CLI surface

```
etude init      # scaffold workflow.yaml, register refs/etude/* refspec
etude capture <stage> --run <id> --input ... --output ...
etude run list [--cohort ...]
etude run show <run-id>
etude replay <run-id> --stage <s> [--skill-version <v>]
etude eval <run-id> --stage <s>      # rubric / assertion
etude eval --pairwise <run-a> <run-b> --stage <s>
etude bench --stage <s> --skill-version <v> --last 10   # headline command
etude import --from-github --repo <owner/name> --last 50
etude reindex   # rebuild the SQLite query cache
etude gc        # prune unreachable / oversized artifacts
etude sync      # push / fetch refs/etude/*
```

`bench` is the headline workflow: take the last N runs that have the named
stage's artifact, replay each with the new skill version, pairwise-eval new vs
old, and report a win rate.

## 6. Walkthroughs

### Capture a run (live)

As work progresses on bead `xc-m6f8`, the capture adapter snapshots the plan
when planning completes, the diff when the PR opens, the review when the
review completes. Each becomes an immutable blob; the manifest grows a stage
entry each time. At merge, `runs/20260517-xc-m6f8/manifest.json` is complete.

### Improve the planning skill

```
etude bench --stage plan --skill-version dev-planner@feature-x --last 10
```

For each of the last 10 runs with a `plan` artifact: check out the recorded
sha, re-run the new planning skill against the recorded task input, pairwise-
judge the new plan against the original. Output: `7/10 wins, 2 ties, 1 loss`
plus per-run reasoning. Now the skill change has a number.

### Backfill for review eval

```
etude import --from-github --repo xenota-collective/xenon --last 50
```

Creates 50 runs, each with a final-diff artifact and a PR-body artifact —
enough to bench the `review` and `docs` skills, not the `plan` skill.

## 7. First testbed: xenota

xenota is the first integration target. Its workflow has extra stages:
`plan → implement → test → manual-test-plan → manual-test-execution →
review → docs`.

- **Capture adapter** hooks into the XSM lifecycle: snapshot the bead
  `--design` field as the `plan` artifact when a polecat finishes planning;
  snapshot the diff when the PR opens; snapshot the review when a review bead
  closes. xenota's existing artifacts (bead design fields, PR bodies,
  `.xsm-local/log/*.jsonl`, manual-test result files) are the capture sources.
- **Backfill** via `etude import` over the xenon repo's closed PRs.
- Because xenota's swarm is parallel, capture must not contend on a shared
  write point — the per-run ref design (§4.2) handles this: each run writes
  its own `refs/etude/runs/<id>` atomically.

## 8. Risks and open questions

- **Skill-runner adapter (biggest unknown).** Replay requires invoking a skill
  headlessly. For Claude Code skills that likely means `claude -p` in
  print/headless mode or the Agent SDK. v1 needs a concrete, reliable adapter;
  this is the riskiest part of the design.
- **Concurrent writes — largely resolved.** The per-run ref design (§4.2)
  means a parallel swarm never contends on a shared tip; each run writes its
  own ref atomically. Residual: the SQLite query cache may lag behind the refs
  — treat it as eventually-consistent and rebuildable, never authoritative.
- **Replay non-determinism.** Running a skill twice yields different output.
  Bench may need multiple samples per run, or to accept and report variance.
- **Judge reliability.** Pairwise LLM judging has position bias; rubric
  scoring has scale drift. Randomize pairwise order; version rubrics.
- **Eval cost.** A `bench --last 10` is 10 replays + 10 judgments — real token
  cost. Make cohort size explicit and cache eval results.
- **Binary artifacts.** Screenshots/transcripts must be stored by reference,
  not inlined, to keep `.git` from bloating.
- **Skill identity across repos.** Skills live outside the artifact repo; the
  manifest's external skill identity scheme must be solid.
- **Ref sync adoption.** Custom refs do not sync by default (§4.2). The
  `init`/`sync` mitigation must be robust, and CI needs the refspec too.

## 9. Phasing

| Phase | Deliverable |
|---|---|
| **0** | `workflow.yaml` schema + `refs/etude/*` store + `init`, `capture`, `run show`, `sync`. Manual capture only. |
| **1** | Capture adapter for xenota (bead/PR snapshots) + `import` backfill. |
| **2** | `replay` — the skill-runner adapter, re-run one stage. |
| **3** | `eval` (rubric + pairwise) + the `bench` headline command. |
| **4** | OSS polish: docs, non-beads example repo, `gc`, `reindex`. |

Each phase is independently useful: Phase 0–1 alone gives you a versioned
artifact history; replay and eval build on top.

## 10. Prior art and positioning

A scan of adjacent tools (May 2026) found no single tool that does what this
one does — but every individual piece exists somewhere, and one part of our
design has direct prior art worth studying.

### Skill A/B testing — partially occupied

- **Anthropic Skill Creator 2.0** (~April 2026) ships built-in skill A/B
  testing: for each test prompt it spawns one subagent with the skill and one
  without, and a blind comparator judges which output is better. This is
  conceptually our "did the upgrade help" core — but it runs against
  **synthetic, hand-authored or auto-generated prompts**, not real work.
- The locally installed **`skill-evaluator`** skill is a weaker manual
  precursor: runs hand-written input/expected pairs headlessly and grades
  them. No replay, no baseline A/B, no trend tracking. This tool supersedes it.

### Eval platforms — strong on mechanics, wrong storage model

- **Inspect** (UK AISI) — closest *structural* match: its solver chains model
  multi-stage pipelines, exactly like our plan→implement→test→review→docs
  stages. Local file-based `.eval` logs.
- **promptfoo** — closest *eval-mechanics* match: `select-best` pairwise
  assertion, `llm-rubric` grading, YAML configs committed to git, local
  SQLite. **DeepEval** has a purpose-built pairwise judge (`ArenaGEval`).
- **Braintrust / LangSmith / Langfuse** — polished versioned-experiment
  regression UX, but cloud-locked with their own databases.
- **Common gap:** all version the *prompt or dataset*, not the *stage output*;
  all re-run a whole eval rather than replaying one stage with upstream stages
  held fixed; none are git-native for run *results*.

### PR-derived benchmarks — only the implement stage

- **SWE-bench** and its variants are the canonical PR-replay design: a merged
  PR becomes (base-commit repo snapshot + issue text + held-back test patch).
  Its **collection pipeline can be run on your own repo** to mint private
  instances — a primitive worth reusing for our `implement` stage and `import`
  backfill.
- **Gap:** scores only final-diff-vs-hidden-tests; covers only implementation;
  there is no PR-derived oracle for *planning* or *review*; the public
  benchmarks are fixed datasets. Plan-compliance is emerging research
  (trajectory analysis), not a product.

### Storage — custom ref namespace (decided)

- **DVC** deliberately chose a **custom ref namespace (`refs/exps`)** over
  branches, citing GitHub's `refs/pull/*` as precedent — explicitly to avoid
  branch-namespace pollution and accidental checkout/merge. **git-annex** uses
  a dedicated branch.
- **Cloudflare Artifacts** (2025, closed beta) was initially flagged as
  near-identical prior art; detailed research showed it is not. It is a
  hosted, proprietary service for provisioning agent-scale git repos (a custom
  Zig/Wasm git server on Durable Objects), with no self-hostable component and
  no capture/replay/eval. It is adjacent inspiration, not a competitor or
  backend. It does independently validate one idea: attaching harness metadata
  to git objects via **git-notes** is a sound, converged-upon pattern (see the
  discoverability note in §4.2).
- **Decision:** the brief follows DVC — captured data lives under
  `refs/etude/*`, not an orphan branch (see §4.2). This trades the
  orphan branch's accidental-checkout risk and shared-tip contention for a
  sync cost that the `init`/`sync` commands absorb.

### Positioning — what is genuinely unoccupied

The defensible space is the **intersection**: replaying **non-implementation
stages** (planning, review, docs) against a corpus of your **own real
historical work**, and measuring the **skill-version delta**. Implementation
replay is SWE-bench's turf; skill A/B on synthetic prompts is Skill Creator
2.0's; multi-stage pipelines are Inspect's; git-native storage is Cloudflare
Artifacts'. No tool combines real-work corpus + non-impl stage replay +
skill-delta scoring. That intersection is the product.
