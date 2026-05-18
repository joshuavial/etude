# Dogfood Process Retro

Status: planning note. This retro covers the early dogfood workflow while
working on `etude-agent-workflow-audit` and `etude-consolidate-test-qa`.

## Retro Summary

The work established a stronger dogfood workflow: phase gates, planning docs,
external reviewer gates, and a concrete Verify phase design. It went well in
that process defects were caught quickly and encoded into repo docs instead of
remaining conversational rules.

The biggest issue was process drift. The workflow changed from human approval
gates to a three-reviewer gate while active artifacts still described human
approval. That forced repeated review reruns.

The strongest pattern was treating reviewer feedback as design input. Gemini,
Claude Opus, and GPT-5.5 caught real contradictions in the artifact contract,
and the resulting docs are clearer.

## What Went Wrong

- Gate authority was changed in conversation before it was encoded in the repo.
- The active Verify design kept stale human-gate language after the process had
  changed.
- Optional reviewer improvements were initially treated as optional in the
  everyday sense, even though they were useful low-cost quality improvements.
- The reviewer prompt summarized artifacts instead of always including exact
  current file contents. That was efficient, but it made reviewer confidence
  depend on the orchestrator's summary.
- Long-running external reviewer calls created uncertainty about whether the
  process was parallel or stalled.
- The first `bd update` attempt used shell-interpreted backticks in inline
  text, which produced noisy shell errors before the design was corrected.
- Claude Opus twice interpreted a reviewer-seat prompt as an orchestration
  prompt and returned an invalid "gate incomplete" result instead of a
  Claude-seat `GO` or `BLOCK`.

## Root Causes

The main root cause was that the dogfood workflow itself was still being
designed while being used. Process rules existed in chat before they existed in
repo docs, so artifacts could contradict the latest intent.

A second cause was missing gate-run mechanics. The docs now say every gate
needs three reviewers, but the process still lacks a small execution checklist
for how to assemble prompts, capture exact artifacts, track reviewer sessions,
record results, and decide whether a rerun is needed.

A third cause was brittle command hygiene around long Markdown payloads. Inline
shell strings are too easy to corrupt when they contain backticks, quotes, or
paths. Gate and bead updates should prefer files or stdin heredocs.

A fourth cause was ambiguous reviewer prompt role framing. The shared process
language describes the whole three-reviewer gate, but each external model is
only one reviewer seat. Without an explicit seat-only instruction, Claude Opus
treated the panel process as its own responsibility.

## What Worked Well

- Phase gates made the work inspectable and reversible.
- Keeping planning material under `docs/plans/` avoided documenting planned
  behavior as shipped behavior.
- The three-reviewer gate found contradictions that a single reviewer missed.
- Requiring all reviewers to finish prevented accidental partial approval.
- Treating reviewer tool failures as escalation conditions is the right rule.
- Recording decisions in bead notes preserved the evolving rationale.

## Recommended Changes

1. Add a gate execution checklist.

   Create a small runbook that defines how to run a gate: gather exact file
   contents, launch the three reviewers in parallel, wait for all results,
   classify `GO`/`BLOCK`/tool failure, apply required changes, implement or
   defer optional improvements, record results in the bead, and rerun when
   required.

   Artifact: `docs/plans/dogfood/review-gate-runbook.md`.

   Leverage: high. It turns the current policy into repeatable mechanics.

2. Prefer file/stdin updates for long bead notes and designs.

   Any `bd update` carrying Markdown, backticks, or multi-line text should use
   `--design-file -`, `--body-file -`, or a temporary reviewed file instead of
   inline shell arguments.

   Artifact: add this rule to `docs/plans/dogfood/review-gate-runbook.md` and
   later to the workflow skill.

   Leverage: high. It prevents noisy shell interpolation failures.

3. Use exact artifact payloads for gate reviews.

   Reviewer prompts should include exact current file contents or an explicit
   digest plus changed excerpts. Summaries are allowed only as orientation, not
   as the sole source of truth.

   Artifact: `docs/plans/dogfood/review-gate-runbook.md`.

   Leverage: high. It reduces review drift.

4. Keep optional improvements mandatory-before-advance.

   The current `review-gate-process.md` change is correct: optional
   improvements from `GO` reviewers do not require a rerun, but they must be
   implemented or explicitly deferred to a named follow-up bead before
   advancing.

   Artifact: already added to `docs/plans/dogfood/review-gate-process.md`.

   Leverage: medium. It preserves reviewer value without creating unnecessary
   reruns.

5. Surface reviewer run status while waiting.

   During long waits, report which reviewers are still pending and which have
   returned. Do not infer failure from silence unless the process has a defined
   timeout or the tool exits with an error.

   Artifact: `docs/plans/dogfood/review-gate-runbook.md`.

   Leverage: medium. It reduces operator uncertainty.

6. Make reviewer-seat prompts explicit.

   Every reviewer prompt should state near the top that the model is only one
   reviewer seat, must not invoke other reviewers, and must not escalate because
   another reviewer is unavailable. It should return only its own `GO`/`BLOCK`
   verdict.

   Artifact: already added to `docs/plans/dogfood/review-gate-runbook.md`.

   Leverage: high. It prevents invalid reviewer outputs and expensive reruns.

## Highest-Leverage Next Step

Create `docs/plans/dogfood/review-gate-runbook.md` before the next gate. It
should turn the three-reviewer gate policy into an operational checklist with
prompt assembly, parallel invocation, result capture, optional-improvement
handling, rerun rules, and safe `bd update` mechanics.
