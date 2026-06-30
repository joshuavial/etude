# Live execution (`etude run`) — design

Status: mixed. §1 (live workflow orchestration) is **implemented** as of bead
etude-xin. §2 (live gate execution) is **implemented** as of bead etude-04i.
§3 (scoped secret passthrough) remains **planned**. Durable failed-stage
status capture (decision 6 below) is deferred to bead etude-dp7.

Today etude supports both capture-and-replay and live orchestration: it records
runs, `etude replay <run-id> <stage>` re-runs a single recorded stage, and
`etude replay <run-id>` re-runs all stages forward. This note retains the
original planning context; implemented behavior is documented in
[docs/run.md](../run.md).

It is the etude-side companion to the xenota "dev projection" plan, which wants
to use etude as the live runner **and** tracker so that capture can never drift
from execution. That consumer context is non-normative; everything below is an
etude feature in its own right.

What ships today vs. what is planned is marked per section. "Implemented" refers
to code under `internal/` on `main`; everything attributed to `etude run` (live)
is planned.

## Summary of decisions

1. **One new live mode, not a rewrite.** `etude run <workflow>` (live) reuses the
   existing external-runner contract verbatim — env `ETUDE_INPUTS_DIR` /
   `ETUDE_OUTPUT_FILE`. A live run walks the workflow's stage graph in dependency
   order, invokes that contract per stage, chains each stage's output artifact
   into the next stage's inputs, and captures each step **by construction**, so a
   live run *is* a captured run.
2. **Everything is a runner; workflows + gates are arbitrary.** A stage `runner`
   is any command behind the contract — an LLM (`claude -p …`) *or* a
   deterministic script (`make test`, `git checkout -b …`). Stage names and edges
   are user-defined: the dev pipeline (`plan → implement → verify → docs →
   review`) and a research pipeline (`research → fact-check → draft → review →
   tone-police`) are both just stage graphs. There are no hardcoded phase names
   anywhere in the engine.
3. **Config lives on the stage; the seat/runner library is shared.** Each stage
   carries its own `runner` and optional `gate` block. How to *invoke* a named
   runner/seat (e.g. `opus`, `codex`) lives in a single shared **registry** file
   (`.etude/registry.yaml` — the renamed former `gates.yaml`; it is a
   runner/seat registry, not gate bindings) referenced by name from stage
   runners, gate seats, and `etude-review`'s ephemeral panels alike. Tier
   presets (`L1–L4`) remain as optional named seat-groups; inline seat lists are
   allowed for one-offs.
4. **Gates execute, with hard checks and soft seats.** Live mode adds gate
   *execution*. A gate has two seat kinds: **checks** (deterministic; any failure
   is a hard BLOCK, no threshold) and **seats** (model votes; weighted
   `pass_threshold`). It synthesizes a fail-closed verdict (`pass | rerun |
   escalated`) and drives the loop — `rerun` re-runs the guarded stage with
   feedback and bumps the round, `escalated` climbs the tier ladder. The executed
   outcome is written through the existing `capture-gate` path automatically.
5. **One evolving working tree; forward-replay.** A live run uses a single
   mutable worktree per run that stages share (so `git checkout`/branch steps and
   later stages that read earlier mutations Just Work). This replaces the
   per-stage pristine-checkout model: `etude replay` re-executes the run
   **forward** from the captured artifacts (stages 1..N in order), not as
   random-access per-stage. Hermeticity rests on the recorded stage outputs that
   capture already stores (LLM stages are not deterministic).
6. **Stop-and-capture on failure; resumable.** A stage runner exiting non-zero or
   timing out (outside a gate) halts the run and captures the failed stage's
   status, leaving a valid partial run. `etude run --resume <id>` continues from
   the last good stage. Plain stages do not auto-retry; retry is a gate concept.
7. **Hermetic-by-default stays the default.** Live runners that call LLMs need
   credentials, but replay must stay hermetic. So secrets reach a runner only
   through an explicit, auditable allowlist; with no allowlist configured,
   behavior is unchanged (env stripped to `PATH`, `ETUDE_INPUTS_DIR`,
   `ETUDE_OUTPUT_FILE`).

**v1 scope:** build the general engine above and prove it by converting the
existing dev workflow to it and driving it live end-to-end. Generality is proven
by construction (the schema has no dev-specific anything); a second
research-style workflow lands later as an explicit generality test.

## 1. Live workflow orchestration

**Implemented (etude-xin).** `etude run <workflow>` walks an arbitrary stage
graph live, capturing each stage incrementally, and `etude replay <run-id>`
(no stage arg) re-runs all stages forward. See [docs/run.md](../run.md) for
user-facing documentation.

What is implemented:

- Reads the workflow's (arbitrary) stage graph from `.etude/workflow.yaml` and
  executes stages in dependency order. Stage names/edges are user-defined; no
  phase names are hardcoded.
- Resolves each stage's `runner` (a name into the shared registry from
  `.etude/registry.yaml`, or inline) and invokes the external-runner contract
  per stage (`ETUDE_INPUTS_DIR`, `ETUDE_OUTPUT_FILE`). A runner may be an LLM
  or a deterministic script.
- Runs all stages in a **single evolving worktree** per run, so git-lifecycle
  stages (checkout/branch/commit) and later stages that read earlier mutations
  work without artifact round-tripping.
- Auto-generates a `run_id` (timestamp + short random, sortable); `--run-id`
  overrides. Create-only: an explicit collision errors.
- Chains output role → next input role, matching `capture-run` semantics.
- Captures each stage **incrementally** as it completes (compare-and-swap on the
  run ref), so `etude run show` works mid-run and a crash leaves a valid partial
  run.
- On a stage failure (non-zero/timeout) outside a gate: stop and leave the
  partial run. Resume later with `etude run --resume <id>` from the frontier
  (first stage not yet captured). Gate blocks in workflow stages are executed
  during the run (see §2).
- Forward replay: `etude replay <run-id>` (1 arg) re-runs all stages in order
  in a single evolving worktree using recorded (content-addressed) inputs.
  `etude replay <run-id> <stage>` (2 args) retains the original single-stage
  behavior unchanged.

**Note:** Durable failed-stage status capture — recording which stage failed
and surfacing it in `etude run show` — is deferred to bead **etude-dp7**
(depends on etude-xin). A partial run shows only successfully-completed
stages; failure is surfaced via nonzero exit and stderr only.

### Acceptance criteria (met)

- `etude run <workflow>` executes all stages of an arbitrary stage graph live and
  produces a captured run inspectable via `etude run show` (including mid-run).
- The produced run replays **forward** with `etude replay` (stages 1..N in order)
  from the captured artifacts.
- A failed run is resumable via `etude run --resume <id>`.
- Stage chaining matches `capture-run` semantics.

## 2. Live gate execution

**Implemented (etude-04i).** Gate blocks in workflow stages are now executed
during live runs. See [docs/run.md](../run.md#gate-execution) for user-facing
documentation.

What is implemented:

- A gate is attached to the stage it guards (`stage.gate`), with two seat kinds:
  - **checks** — deterministic runners (tests/build/lint); any failure is a hard
    BLOCK, exempt from the vote threshold;
  - **seats** — model/variant runners that vote, decided by a weighted
    `pass_threshold`.
- Fans the stage's output artifact at the gate's checks + seats, each invoked
  through the generic runner contract.
- Synthesizes a verdict — `pass | rerun | escalated` — fail-closed, reusing the
  existing etude-loop synthesis semantics: any failed check blocks; fewer than 2
  usable seats fails closed; errored seats are recorded and skipped; the weighted
  `pass_threshold` decides the rest.
- Drives the loop: `rerun` re-runs the guarded stage with seat feedback and
  increments the round (`<stage>.r<round>`); `escalated` advances the tier
  (stronger / more seats, or human).
- Records each gate attempt automatically through the existing `capture-gate`
  path (no CLI subprocess; the engine writes directly to the manifest).

Gate config lives on the stage (`checks`, `seats`/`tier`, `pass_threshold`,
`max_rounds`, `abstraction`); seat/tier *definitions* live in the shared
registry (`.etude/registry.yaml` — the renamed former `gates.yaml`).

### Acceptance criteria (met)

- A workflow stage with a gate runs its checks + seats live and records a gate
  attempt with the synthesized verdict.
- A failing deterministic check hard-blocks regardless of seat votes.
- `rerun` re-executes the guarded stage with seat feedback and increments the
  round; `escalated` advances the tier.
- Fail-closed behavior matches the existing etude-loop guarantees.

## 3. Scoped secret / env passthrough

**Planned (etude-3a2).** Secret passthrough is not yet built. Both live runs
and replay strip runner env to `PATH`, `ETUDE_INPUTS_DIR`, `ETUDE_OUTPUT_FILE`
— hermetic by default, unchanged from before etude-xin.

**Planned:** a configurable allowlist / secret-injection mechanism for runners
during live runs (opt-in for replay), e.g. `etude.runner.env-allowlist` or a
secrets file referenced from config. Keep hermetic-by-default; make the
passthrough explicit and auditable.

### Acceptance criteria

- An operator declares exactly which env vars / secrets reach the runner.
- Default behavior remains hermetic (nothing beyond the current three vars).
- The passthrough is visible in run metadata for auditability.

## Sequencing

The **schema + registry foundation** landed first (etude-2pc.1): per-stage
`runner` and `gate` blocks, run-level default runner, and the shared seat/runner
registry (`.etude/registry.yaml` — the renamed historical `gates.yaml`) with tier
presets. Orchestration (§1) is the spine and builds on the schema. Gate execution
(§2) builds on orchestration and is the largest piece. Secret passthrough (§3) is
small and independent — it depends only on the schema, and is only *exercised*
once a live LLM runner exists.

The **proof path has landed (etude-2pc.2)**: the existing dev workflow was
converted to the new schema — historical `gates.yaml` seats/tiers/quorum migrated
to `.etude/registry.yaml`; `phase_gates` migrated to per-stage `gate` blocks in
`.etude/workflow.yaml`; the `etude-review` skill now reads the new files; the
historical `gates.yaml` is deleted. The deterministic-check gate (verify stage)
and orchestration walk were proven live via `etude run` with a throwaway repo.

**Real-LLM end-to-end run is deferred to bead etude-s6z** (depends on
etude-2pc.2 + etude-3a2 secret passthrough): running the real `dev` stage runner
(`claude -p`) and model-seat gates (opus/codex/gemini) live requires secret
passthrough (§3), which is not yet built.

A second, research-style workflow lands later as an explicit generality test
(etude-2pc.3).
