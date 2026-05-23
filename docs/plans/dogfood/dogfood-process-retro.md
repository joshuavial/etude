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

A second cause was missing gate-run mechanics. The docs said every gate needed
three reviewers before there was a small execution checklist for how to
assemble prompts, capture exact artifacts, track reviewer sessions, record
results, and decide whether a rerun is needed. The review gate runbook now
fills that gap.

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

All recommendations below were implemented during this iteration. Only carrying
the safe bead-update rule into the future workflow skill remains follow-up
work.

1. Add a gate execution checklist.

   Already added to [Review gate runbook](review-gate-runbook.md). The runbook
   defines how to run a gate: gather exact file contents, launch the three
   reviewers in parallel, wait for all results, classify `GO`/`BLOCK`/tool
   failure, apply required changes, implement or defer optional improvements,
   record results in the bead, and rerun when required.

   Artifact: `docs/plans/dogfood/review-gate-runbook.md`.

   Leverage: high. It turned the current policy into repeatable mechanics.

2. Prefer file/stdin updates for long bead notes and designs.

   Any `bd update` carrying Markdown, backticks, or multi-line text should use
   `--design-file -`, `--body-file -`, or a temporary reviewed file instead of
   inline shell arguments.

   Artifact: already added to `docs/plans/dogfood/review-gate-runbook.md` in
   the Safe Bead Updates section. It should also be carried into the workflow
   skill later.

   Leverage: high. It prevents noisy shell interpolation failures.

3. Use exact artifact payloads for gate reviews.

   Reviewer prompts should include exact current file contents or an explicit
   digest plus changed excerpts. Summaries are allowed only as orientation, not
   as the sole source of truth.

   Artifact: already added to `docs/plans/dogfood/review-gate-runbook.md`.

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

   Artifact: already added to `docs/plans/dogfood/review-gate-runbook.md`.

   Leverage: medium. It reduces operator uncertainty.

6. Make reviewer-seat prompts explicit.

   Every reviewer prompt should state near the top that the model is only one
   reviewer seat, must not invoke other reviewers, and must not escalate because
   another reviewer is unavailable. It should return only its own `GO`/`BLOCK`
   verdict.

   Artifact: already added to `docs/plans/dogfood/review-gate-runbook.md`.

   Leverage: high. It prevents invalid reviewer outputs and expensive reruns.

## Highest-Leverage Step Taken

Created `docs/plans/dogfood/review-gate-runbook.md` before advancing beyond the
current gate. It turns the three-reviewer gate policy into an operational
checklist with prompt assembly, parallel invocation, result capture,
optional-improvement handling, rerun rules, and safe `bd update` mechanics.

## Retro: gate reviewer auth failure and four-seat expansion (2026-05-23)

### Retro Summary

While running the dogfood reviewer gate for bead `etude-workflow-schema`, the
Claude Opus seat failed to authenticate when invoked as a nested `claude` CLI
from inside a Claude Code session. The failure is deterministic, not transient.
The recovery (run the Claude seat as a fresh in-harness Task sub-agent) worked
and is now an encoded rule. The same session also showed why reviewer diversity
matters: the codex seat caught a real `BLOCK` that two other seats missed,
motivating a 4th independent seat. The gate is now a four-reviewer panel.

### What Went Wrong

- The Claude Opus seat invoked as `claude --model opus -p` failed with
  `401 Invalid authentication credentials`. There is no `ANTHROPIC_API_KEY` in
  the environment, and a nested `claude` CLI spawned from inside a Claude Code
  session cannot authenticate headlessly: the host session's credentials are
  not exposed to the subprocess. This recurs every time the orchestrator is
  Claude Code.

### Root Causes

- The runbook assumed the Claude seat could always run as the external
  `claude -p` CLI, regardless of which agent was orchestrating the gate. That
  assumption holds when codex or gemini drives the gate, but not when Claude
  Code is the orchestrator, where the nested CLI has no credentials.
- A three-seat panel left less margin: one seat caught a real defect the other
  two missed (see below), so reviewer diversity is load-bearing, not redundant.

### What Worked Well

- The user-approved fix substituted the Claude seat with a fresh in-harness
  sub-agent: `Task(subagent_type="general-purpose", model="opus", prompt=<only
  the gate prompt>)`. It is authenticated through the host session, genuinely
  fresh and isolated (only the gate prompt as context), and returned a valid GO
  verdict with useful optional improvements. It is functionally equivalent to a
  fresh `claude --model opus -p` seat without the auth problem.
- On the Implement gate, the codex (GPT-5.5 xhigh) seat returned a real `BLOCK`
  that both Gemini and the in-harness Claude seat missed: the new `ParseYAML`
  did not reject trailing YAML documents, unlike the sibling
  `runmanifest.ParseJSON` it mirrors. This reinforced the value of diverse,
  independent reviewer seats and motivated adding a 4th.

### Recommended Changes (all implemented this iteration)

1. Encode the in-harness Claude rule. When the orchestrator is Claude Code, the
   Claude Opus seat must run as a fresh in-harness Task sub-agent
   (`subagent_type` general-purpose or equivalent, `model: opus`, given only the
   gate prompt), not the external `claude -p` CLI. The external CLI is used for
   the Claude seat only when the orchestrator is not Claude.

   Artifacts: `docs/plans/dogfood/review-gate-runbook.md` (Invocation, In-harness
   Claude rule) and `docs/plans/dogfood/review-gate-process.md` (Decision).

   Leverage: high. It removes a deterministic gate failure.

2. Add `pi`/`pilms` as a 4th independent reviewer seat. `pilms` is a shell
   function (`pilms () { pi --provider lmstudio --model qwen/qwen3.6-35b-a3b
   "$@" }`) that runs the local `pi` CLI against a local LM Studio model
   (qwen3.6-35b-a3b), free and with no API auth. Canonical invocation:
   `pilms --tools read,grep,find,ls,bash -p "<gate prompt>"`. The gate now
   passes only if Gemini Pro, Claude Opus, fresh GPT-5.5 xhigh, and pi/pilms all
   return clear GO. A pi/pilms failure usually means LM Studio is not running.

   Artifacts: `docs/plans/dogfood/review-gate-runbook.md`,
   `docs/plans/dogfood/review-gate-process.md`,
   `docs/plans/dogfood/verify-phase-design.md`,
   `docs/plans/dogfood/capture-protocol.md`, and the `dev-workflow` skill at
   `~/.claude/skills/dev-workflow/SKILL.md`.

   Leverage: medium, and contingent on tooling. The real cross-model value in
   this run came from the GPT-5.5 (codex) seat, which caught a trailing-document
   parse bug on Implement attempt 1 — before pi/pilms was added, while still a
   three-seat panel. The local pi seat is free and adds a fourth independent
   read, but only earns its place if it can actually inspect the artifacts.

3. pi/pilms must run with a read-only tool allowlist, never tool-less. On its
   first use the seat was run with all tools (hung in `-p` mode) and then with
   `--no-tools` (returned `GO`/none on every gate — a blind rubber stamp that
   judged only the orchestrator's prompt summary). The fix is
   `--tools read,grep,find,ls,bash`: the seat then reads the changed files and
   runs `git diff`/`go test` itself. Lesson: a reviewer seat with no tools adds
   a vote but no signal; gate prompts must let each seat see the real artifacts.

   Artifact: `docs/plans/dogfood/review-gate-runbook.md` (Invocation, pi seat).

   Leverage: high. It is the difference between a real fourth reviewer and a
   rubber stamp.
