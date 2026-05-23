# Auto Mode

Autonomous development with the configured phase gate.

## Overview

Auto mode advances through ready beads until:

1. All selected beads complete successfully, or
2. A phase gate blocks, fails, or needs human input.

In repos with a project-specific gate, that gate is authoritative. In the
`etude` dogfood repo, use the three-reviewer gate for every phase. In repos
without a project gate, Codex can evaluate phase artifacts with the prompts in
`approval-prompts.md`.

## Command Interface

```bash
/dev auto                    # Auto-mode on all ready beads
/dev <bead-id> auto          # Auto-mode on specific bead
/dev auto --confidence=0.8   # Codex fallback threshold
```

Default Codex fallback confidence threshold: **0.7**

## Flow Comparison

Manual conversational mode:

```text
Plan -> [gate] -> Implement -> [gate] -> Verify -> [gate] -> Docs -> [gate] -> Final Review -> [gate] -> Done
```

Auto mode:

```text
Plan -> [configured gate] -> Implement -> [configured gate] -> Verify -> [configured gate] -> Docs -> [configured gate] -> Final Review -> [configured gate] -> Done
           |                        |                         |                      |                              |
           v                        v                         v                      v                              v
       escalate on BLOCK, reviewer/tool failure, missing input, or low Codex confidence
```

## Gate Process

At each gate, provide:

1. Bead identity and current phase.
2. Exact phase artifact contents or exact changed excerpts.
3. Prior approved artifacts needed to judge the phase.
4. Current git status and diff references.
5. Structured expected output.

For the `etude` dogfood repo, follow `docs/plans/dogfood/review-gate-runbook.md`.

### Codex Fallback Decision Logic

```python
response = codex_evaluate(phase, bead_context)

if response.approved and response.confidence >= threshold:
    proceed_to_next_phase()
else:
    escalate_to_human(
        bead=bead_id,
        phase=current_phase,
        reason=response.reason,
        concerns=response.concerns,
        confidence=response.confidence
    )
```

## Escalation Triggers

Auto mode escalates when:

1. A configured reviewer returns `BLOCK`.
2. A reviewer invocation fails because of auth, quota, model access, allowance,
   timeout, or tooling.
3. Verify returns `blocked` and needs external input.
4. The same phase gate blocks through the configured rerun limit.
5. Codex fallback confidence is below threshold.
6. Codex fallback explicitly rejects the phase.
7. Security or architecture concerns require human judgment.
8. Requirements are ambiguous and cannot be resolved from local context.

## State Tracking

### Labels

- `auto:enabled` - Auto mode is active on this bead.
- `auto:escalated` - Human intervention was requested.

### Dev Notes Addition

```markdown
## Auto Mode
- Mode: auto
- Gate: project-configured
- Escalations: 0

### Gate Log
| Phase | Gate result | Notes |
|---|---|---|
| Plan | GO | Clear requirements |
| Implement | GO | Diff matches plan |
| Verify | pending | Not run yet |
| Docs | pending | Not run yet |
| Final Review | pending | Not run yet |
```

## Multi-Bead Processing

When running `auto` on multiple ready beads:

```text
for each ready_bead:
    label bead with auto:enabled
    completed = false

    while not completed:
        run current phase for bead

        if current phase is Plan:
            gate_result = evaluate("plan", bead)
            if gate_passed(gate_result): current phase = Implement
            else: escalate and stop
            continue

        if current phase is Implement:
            gate_result = evaluate("implement", bead)
            if gate_passed(gate_result): current phase = Verify
            else: escalate and stop
            continue

        if current phase is Verify:
            gate_result = evaluate("verify", bead)
            if not gate_passed(gate_result): escalate and stop
            if verify_status == "pass": current phase = Docs
            if verify_status == "fail": current phase = Implement
            if verify_status == "blocked": escalate and stop
            continue

        if current phase is Docs:
            gate_result = evaluate("docs", bead)
            if gate_passed(gate_result): current phase = Final Review
            else: escalate and stop
            continue

        if current phase is Final Review:
            gate_result = evaluate("final-review", bead)
            if not gate_passed(gate_result): escalate and stop
            if final_review_recommendation == "return to Plan": current phase = Plan
            if final_review_recommendation == "return to Implement": current phase = Implement
            if final_review_recommendation == "return to Verify": current phase = Verify
            if final_review_recommendation == "return to Docs": current phase = Docs
            if final_review_recommends_close:
                commit and close bead
                remove phase:review
                add phase:complete
                remove auto:enabled
                push git and bead storage
                completed = true
            continue

report_summary(completed, remaining, escalation_reason)
```

## Escalation Report

When escalating, report:

```markdown
## Auto Mode Escalation

**Stopped at**: <bead-id> / <phase>
**Reason**: <reason>
**Gate result**: <BLOCK | failed | blocked | low confidence>

**Concerns**:
- <concern 1>
- <concern 2>

**Progress**:
- Completed: <N> beads
- Remaining: <M> beads

**What's needed**: <human decision/input required>
```

## Resuming After Escalation

After the issue is resolved:

```bash
/dev <bead-id> <phase>    # Resume specific phase manually
/dev <bead-id> auto       # Resume auto mode from current state
/dev auto                 # Resume auto on remaining ready beads
```

## Codex Fallback Settings

```bash
codex exec \
  -c model_reasoning_effort=high \
  --sandbox read-only \
  --output-schema <schema-path>
```

## Safety Considerations

1. The evaluator receives exact artifacts, not summaries only.
2. Reviewers/evaluators do not modify files while evaluating.
3. Structured results are recorded in bead notes.
4. Optional improvements are implemented or deferred to named beads.
5. Gate failures are process blockers, not approvals.
6. Every phase leaves a capture trail for later `etude` import.
