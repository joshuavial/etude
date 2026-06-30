# Research workflow example

This example proves that the etude live-run engine has **no dev-specific assumptions**: a genuinely non-dev 5-stage workflow runs end-to-end via `etude run`, capturing by construction, resolving a gate seat from the shared registry, and forward-replaying deterministically. No real LLM or network access is required — all runners and the gate seat are deterministic shell stubs.

## Workflow shape

```
research  (in: task)
    └─► fact-check  (in: task, findings)
            └─► draft  (in: checked)
                    └─► review  (in: draft)  ← GATE: tier L1 + check
                            └─► tone-police  (in: reviewed)
```

| Stage | Produces | Inputs |
|---|---|---|
| research | findings | task |
| fact-check | checked | task, findings |
| draft | draft | checked |
| review | reviewed | draft |
| tone-police | toned | reviewed |

The `review` stage carries a gate with two components:
- A **deterministic check** (`gate-check.sh`): exits 0 when the reviewed draft is non-empty (always passes here).
- A **registry tier** (`L1`): resolves the `approver` seat from `.etude/registry.yaml`, which writes `{"verdict":"go"}`.

The gate is designed to **pass on round 1** (single seat, threshold 1.0, one "go" vote). See the invariant below.

## Registry reuse

The registry mechanism is workflow-agnostic. The same `ResolveStageRunner`, `ResolveGateSeat`, and `ResolveTiers` functions that power the dev workflow also power this research workflow. The example ships its own registry (written at walkthrough runtime with ABSOLUTE paths) to stay hermetic.

## How to run

```bash
bash examples/research/walkthrough.sh
```

Or supply a pre-built binary to skip the build step:

```bash
ETUDE_BIN=bin/etude bash examples/research/walkthrough.sh
```

The script:
1. Builds etude from source (or uses `ETUDE_BIN`).
2. Git-inits a throwaway repo.
3. Commits the sample task document (`inputs/topic.txt`).
4. Writes `.etude/workflow.yaml` and `.etude/registry.yaml` with ABSOLUTE script paths interpolated.
5. Commits the etude config.
6. Runs `etude run research --task inputs/topic.txt`.
7. Runs `etude run show <id>` to inspect the captured run.
8. Runs `etude replay <id>` (forward) to re-execute all 5 stages.

## Expected output

```
==> Step 1: etude run research (5 stages + review gate)
captured <sha>
captured <sha>
captured <sha>
captured <sha>
captured gate review.r1 status=pass
captured <sha>
ref refs/etude/runs/research-<timestamp>-<hex>

==> Step 2: etude run show <id>
...
stage: research
  produced_by: original
...
stage: fact-check
...
stage: draft
...
stage: review
...
gate: review.r1
  status:   pass
  ...
  seat: approver
    verdict:  go
...
stage: tone-police
...

==> Step 3: etude replay <id> (forward — all 5 stages)
...
==> Walkthrough complete.
```

## No engine change

This bead (etude-2pc.3) adds **zero non-test Go under `internal/`**. The proof is structural: the engine's stage graph walker (`engine.go`) uses user-defined stage names; the gate resolver (`gate.go`, `resolve.go`) keys off registry names, not phase names. There are no hardcoded phase names in the live-run engine.

To verify: `git diff --name-only <pre-bead-commit> -- ':!**/*_test.go' | grep '^internal/'` must return empty for this bead's diff.

## Round-1-pass invariant

The `review` gate is **single-seat (`approver`) + threshold 1.0** by design. The `approver` stub always votes "go", so the gate passes on round 1 and no rerun stage is emitted.

**This invariant must be preserved.** Adding a second seat or a blocking seat would cause the gate to rerun (`review.r2`, `review.r3`, …), and the forward replay (`etude replay <id>`) cannot resolve those stage names from the workflow unless a `--runner` override is supplied. If you need to test a multi-round gate scenario, pass `--runner /path/to/stage-runner.sh` on `etude replay` to supply a single fallback runner for all stage names.

## Files

| File | Purpose |
|---|---|
| `stage-runner.sh` | Deterministic runner: concatenates sorted inputs → output |
| `approve-seat.sh` | Stub gate seat: always writes `{"verdict":"go"}` |
| `gate-check.sh` | Deterministic check: exits 0 when input is non-empty |
| `inputs/topic.txt` | Sample task document |
| `walkthrough.sh` | End-to-end demo script |
