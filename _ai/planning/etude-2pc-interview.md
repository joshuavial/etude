# etude-2pc — Live execution: epic planning interview

Epic: `etude-2pc` ([EPIC] Live execution: drive and gate workflows, not just replay)
Design doc: `docs/plans/product/live-execution.md`
Purpose: turn the broad "live execution" epic into an implementation-ready plan and
right-sized beads before coding.

Status legend: ✅ decided · ❓ open · ⏸ deferred · ✂ rejected

## Existing constraints (read from code)
- `internal/replay` `ExecRunner`: runs a configured external command headlessly;
  strict env (PATH, ETUDE_INPUTS_DIR, ETUDE_OUTPUT_FILE), cwd = WorktreeDir,
  materializes inputs to `<scratch>/inputs/NN-role`, reads `<scratch>/output`.
  Has Timeout + MaxOutputBytes guards.
- `internal/cli/capture_run.go`: builds a manifest from already-produced artifact
  files on disk and writes the run ref **create-only, atomic at end** to
  `refs/etude/runs/<run_id>`. Requires run_id in the spec. Does NOT execute.
- `internal/workflow/workflow.go`: parses `.etude/workflow.yaml` — stages with
  inputs/produces/skill/eval(rubric|pairwise|assertion)/optional + retros block.
  specialRoles = {task, repo-state}. NO runner command, NO gate schema today.
- Gates currently live in a SEPARATE `.etude/gates.yaml` consumed by skills
  (dev-workflow gate + etude-review), NOT by Go.
- `internal/worktree` package exists.

## Children (current)
- `etude-xin` (P1, feature): live orchestration `etude run` — §1. Blocks 04i.
- `etude-04i` (P1, feature): live gate execution — §2. Depends on xin.
- `etude-3a2` (P2, feature): scoped secret/env passthrough — §3.

## Answers And Decisions
- Q1 ✅ Per-stage `runner:` field in workflow.yaml (option A). Workflow file fully
  describes execution; any command behind the ETUDE_* contract works. Add an
  optional run-level default runner so trivial workflows can omit per-stage.
  - Derived: `internal/workflow` Stage gains a `Runner` field; `etude run`
    resolves stage.Runner → ExecRunner.Command. No separate runner-config file.

- Q2 ✅ Incremental capture: update the run ref after each stage completes
  (option A). Live ref is the source `etude run show` reads; crash leaves a valid
  partial run, replayable up to last good stage.
  - Derived: new incremental-write path alongside capture-run's atomic writer;
    share manifest/tree-building code. Each stage = a compare-and-swap on the run ref.
  - Derived: `etude run show` reads the live `refs/etude/runs/<id>` directly.

- Q3 ✅ Auto-generate run_id by default (timestamp + short random, sortable),
  `--run-id` override (option A). Still create-only: explicit id collision errors.

- Q4 ✅ RESOLVED by the arbitrary-workflows requirement (Q5). Gate *binding*
  moves onto the stage (a fixed phase→tier map cannot work once stage names are
  arbitrary). Seats + tier presets + quorum → shared registry. Not the full
  merge, not status quo: the responsibility split.
- Q5 ✅ Workflows + gates must be ARBITRARILY configurable stage graphs, not the
  fixed plan/implement/verify/docs/review pipeline (e.g. research → fact-check →
  draft → review → tone-police). workflow.yaml stage schema is already generic
  (role-chained DAG); only phase_gates + the .etude/ instance were dev-hardcoded.
  - Model: (1) a workflow = arbitrary stage graph, each stage = runner (doer) +
    optional gate (reviewers + loop rules); (2) a shared seat/runner registry
    (how to invoke opus/codex/… ) referenced by name by stage runners, gate
    seats, AND etude-review's ephemeral panels.
  - Tier presets (L1–L4) kept as optional sugar over inline seat lists.
  - Dev-specific prose (per-phase abstraction, dev escalation) moves onto that
    workflow's stage gate.abstraction — not a global file.
  - Rename gates.yaml → registry/seats file (it's no longer "gates").
- Q6 ✅ Deterministic (non-LLM) steps must be expressible: e.g. checkout clean
  main, make feature branch, run tests/build/lint. Unifies cleanly: EVERYTHING is
  a runner behind the ETUDE_* contract — a runner can be an LLM or a script; a
  gate seat can be a model vote OR a deterministic check (exit code = verdict).
  - Open sub-decisions carried to questions below.

- Q7 ✅ Gate has two seat kinds: CHECKS (deterministic; any failure = hard BLOCK,
  no threshold) and SEATS (model votes; weighted pass_threshold). Modeled distinctly.
- Q8 ✅ Git lifecycle = ordinary deterministic stages (checkout/branch first;
  commit/PR last), NOT built-in git machinery. Keeps etude runner-agnostic; a
  research/blog workflow can omit git entirely.
- Q9 ✅ Option A: ONE evolving working tree per run. Stages share a mutable
  worktree. Replay is REDEFINED as forward-replay (replay 1..N in order), not
  random-access per-stage. True hermeticity relies on recorded stage outputs
  (capture already stores them) since LLM stages aren't deterministic.
  - Derived: revise design doc §1 AC "replayable with no special-casing" →
    "replay re-executes the run forward from the captured artifacts."
  - Derived: worktree lifecycle = one per run (resolves earlier worktree Q).
  - Implication: per-stage replay random-access is lost; document the tradeoff.

## Questions
- Q10 ✅ Option A: build the GENERAL arbitrary-workflow engine, convert+prove on
  the existing dev workflow, leave "second-workflow generality test" as a tracked
  follow-up bead. No dev-specific assumptions baked in.
- Q11 ✅ One shared seat/runner registry file (renamed gates.yaml) referenced by
  name; inline definitions allowed for one-offs. Shared with etude-review panels.
- Q12 ✅ Stop-and-capture on stage failure: halt, capture failed-stage status,
  valid partial run. `etude run --resume <id>` continues from last good stage
  (evolving worktree + incremental capture make resume cheap). No auto-retry for
  plain stages; retry is a gate concept only.

## Interview complete ✅
Proof path: convert the existing dev workflow to the general schema and drive it
live end-to-end; generality proven by construction (no dev-specific schema) +
a later research-style workflow.
Non-goals (v1): authoring a second full pipeline now; per-stage random-access
replay (replaced by forward-replay); baking git into etude (lifecycle = stages).

## Final plan — revised epic children
- A. Schema + registry foundation (NEW, P1) — workflow stage `runner` + `gate`
  block (checks[] hard-veto + seats[] weighted + tier ref + pass_threshold +
  max_rounds + abstraction), run-level default runner; shared seat/runner registry
  file w/ Go parser + tier presets (L1–L4) + quorum. Blocks B, C, D.
- B. etude-xin (REVISE, P1) — live `etude run`: arbitrary stage-graph walk,
  registry-resolved runners (LLM or script), ONE evolving worktree per run,
  incremental capture, auto run_id (+--run-id), `etude run show`, stop-and-capture
  + `--resume`, forward-replay. Depends A.
- C. etude-04i (REVISE, P1) — live gate execution: hard checks (veto) + model
  seats (weighted), synth pass|rerun|escalated fail-closed, rerun w/ feedback +
  round bump, escalate tier, auto capture-gate. Depends A, B.
- D. etude-3a2 (KEEP, P2) — scoped secret/env passthrough; hermetic default. Depends A.
- E. Migrate dev workflow + retire gates.yaml (NEW, P1) — port .etude config to
  new schema; gates.yaml seats/tiers → registry, phase_gates → per-stage gate
  blocks; update dev-workflow + etude-review skills; delete gates.yaml. Proof
  path. Depends A, B, C.
- F. Generality test: second workflow (NEW, P3, follow-up). Depends E.

## Newly Discovered Questions
- Atomic-at-end (capture-run) vs incremental capture as stages complete (needed
  for `etude run show` mid-run + crash-partial inspectability).
- run_id generation for live `etude run <workflow>` (auto vs required).
- Gate schema home: design says workflow.yaml; repo keeps gates in gates.yaml.
- Stage-execution failure semantics (stop / resume / retry) — distinct from gate rerun.
- Worktree lifecycle: one per run vs one per stage; who creates/cleans.
- First proof path / first real consumer (xenota dev-projection vs test workflow).
- v1 scope: land §1 alone as MVP vs all three together.

## Created Child Issues
- etude-2pc.1 (NEW, P1) Schema + registry foundation — the only ready leaf; blocks all.
- etude-xin (REVISED, P1) Live orchestration — depends on .1.
- etude-04i (REVISED, P1) Live gate execution — depends on .1, etude-xin.
- etude-3a2 (KEPT, P2) Secret passthrough — depends on .1.
- etude-2pc.2 (NEW, P1) Migrate dev workflow + retire gates.yaml — depends on .1, xin, 04i.
- etude-2pc.3 (NEW, P3) Generality test (2nd workflow) — depends on .2.

Docs updated: docs/plans/product/live-execution.md (Summary 1-7, §1, §2, Sequencing).
Graph verified: no cycles; bd ready → etude-2pc.1.
