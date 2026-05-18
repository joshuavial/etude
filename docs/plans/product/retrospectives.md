# Retrospectives

Status: planning note. This sketches how retrospectives should become a
first-class `etude` capability rather than only an ad hoc agent behavior.

## Product Thesis

`etude` should not only capture workflow stages and evaluate skill versions. It
should also help teams learn why a run went well or poorly.

Retros are the bridge between raw captured artifacts and better future
workflow design. They turn failure patterns, review friction, blocked gates,
tooling problems, and surprisingly good choices into structured evidence that
can improve skills, gates, and workflow configuration.

## When Retros Should Run

Retros should be event-driven, not attached to every minor step by default.
Running one after every phase would create noise and make retros feel like
ceremony. The useful triggers are:

- **End-of-run retro:** after a bead/run closes, summarize what changed, what
  gates found, what took extra cycles, and what should improve next time.
- **Repeated gate-block retro:** after the same phase gate receives repeated
  `BLOCK` results, analyze why the artifact keeps failing review.
- **Blocked-state retro:** after a run is blocked by missing context, auth,
  quota, tool access, or human input, record the blocker and prevention path.
- **Failed Verify retro:** when Verify returns `fail`, capture whether the
  failure came from implementation quality, test inadequacy, plan defects, or
  missing workflow rules.
- **Post-bench retro:** after `etude bench`, explain why a skill version won or
  lost across a cohort, using examples from the artifacts.
- **Manual retro:** allow a user to request a retro for a run, phase, cohort,
  skill version, or gate sequence.

Default behavior should be lightweight: end-of-run retros plus triggered
retros for repeated blockers. More aggressive retro policies should be
configurable per repo.

## What A Retro Captures

A retro is a structured artifact. It should be stored like other `etude`
artifacts and linked from the run manifest.

Suggested fields:

- `scope`: run, phase, gate, cohort, skill version, or workflow
- `trigger`: close, repeated-block, blocked-state, failed-verify, bench, manual
- `inputs`: run manifest, phase artifacts, reviewer results, test output,
  command logs, git state, and linked issues
- `summary`: concise narrative of what happened
- `failure_modes`: concrete failure categories observed
- `root_causes`: underlying process, skill, tool, or context causes
- `worked_well`: practices worth preserving
- `recommendations`: proposed changes with target artifact paths
- `follow_up_refs`: beads, PRs, skill files, docs, or workflow config entries
- `decision`: accepted, deferred, superseded, or informational

Retros should never be unstructured memory. If a lesson is worth preserving, it
should point to a durable artifact: a skill change, workflow config change,
bead, formula, runbook, or docs update.

## Workflow Integration

Retros should be modeled as a workflow stage type, but usually not as a gate
that blocks normal product work.

Recommended stage shape:

```yaml
stages:
  - name: retro
    produces: retro
    inputs: [run-manifest, gate-results, phase-artifacts]
    trigger: on-run-close
    optional: true
```

Triggered retros should be expressed separately from linear workflow phases:

```yaml
retros:
  on_run_close: true
  on_repeated_gate_block:
    enabled: true
    threshold: 3
  on_failed_verify: true
  on_blocked_state: true
```

This keeps `Plan -> Implement -> Verify -> Docs -> Final Review` simple while
still letting retros run when they are useful.

## CLI Shape

Possible commands:

```text
etude retro run <run-id>
etude retro phase <run-id> --stage verify
etude retro gate <run-id> --stage implement --attempt 2
etude retro bench <bench-id>
etude retro list --run <run-id>
etude retro show <retro-id>
```

The first implementation can be simpler:

```text
etude retro run <run-id> --out retro.md
etude capture retro --run <run-id> --file retro.md
```

That keeps generation and capture separate until `etude` has a reliable
runner/evaluator interface.

## Evaluation Use

Retros should feed future evals in two ways:

1. **As training/evaluation context:** a planning skill can be judged partly on
   whether it avoids failure modes identified in prior retros.
2. **As cohort analysis:** `etude bench` can group wins/losses by retro-coded
   root cause, such as "insufficient tests", "unclear plan", "tool failure",
   or "review prompt omitted exact artifacts".

Retros should not become the source of truth for whether a stage passed. That
remains the gate, test, or eval result. Retros explain why.

## Capture Rules

- Retros are append-only artifacts once captured.
- A later retro can supersede an earlier retro, but should not mutate it.
- Retros should link to exact artifacts rather than paste large transcripts.
- Retros should include enough provenance to reconstruct their inputs.
- Retros can propose follow-up beads, but should not silently create broad
  work without user or workflow approval.

## Open Design Questions

- Should `etude retro` generate retros itself, invoke a configured retro
  skill, or only capture externally generated retros at first?
- Should repeated-block retros be mandatory in the default workflow or opt-in?
- Should retros have an evaluator, or are they primarily explanatory artifacts?
- How should retros avoid contaminating future replay inputs? A replay should
  use only the artifacts that existed at the original stage boundary unless the
  workflow explicitly includes prior retros as context.
- Should retros be indexed by failure mode for cross-run trend analysis?

## Recommendation

Bake retros into `etude` as optional, triggerable artifacts from the start of
the manifest design, but defer automated retro generation until after manual
capture works.

Phase 0 should reserve the artifact type and manifest links. The dogfood
capture protocol should define how to capture manual retros. Later phases can
add `etude retro` generation and bench-level retrospective analysis.
