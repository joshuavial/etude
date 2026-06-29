# Review Gate Process

Status: planning note. This defines the dogfood gate process to use while
building `etude`.

## Decision

Do not use human approval as the workflow gate.

Every phase gate must pass a three-reviewer panel:

- Gemini Pro
- Claude Opus
- a fresh GPT-5.5 xhigh agent (codex)

Independent means the reviewer evaluates the supplied gate prompt and artifacts
without relying on hidden implementation context. Each reviewer uses a
non-interactive prompt invocation that receives only the gate prompt and
repository files. The GPT-5.5 seat must be fresh: start a new isolated agent
with no carry-over conversation context from earlier work on the bead.

When the gate orchestrator is Claude Code (i.e. you are running inside a Claude
session), the Claude Opus seat MUST be run as a fresh in-harness Task sub-agent
(`subagent_type` general-purpose or equivalent, `model: opus`, given only the
gate prompt as context), NOT the external `claude -p` CLI. A nested `claude` CLI
spawned from inside a Claude session fails auth (`401 Invalid authentication
credentials`); the in-harness sub-agent is authenticated and equivalent. Only
use the external `claude -p` CLI for the Claude seat when the orchestrator is
not Claude. See [Review Gate Runbook](review-gate-runbook.md) for the exact
invocation and the canonical three-seat command lines.

The gate passes only when all three reviewers give a clear `GO`.

If any reviewer gives `BLOCK`, the blocking feedback must be incorporated and
the same gate must be run again with all three reviewers. Do not advance the
workflow on partial approval.

If any reviewer cannot complete because of auth, quota, model access,
allowance, timeout, or tooling failure, stop and escalate to the user. A failed
reviewer invocation is not a `GO` and must not be skipped.

**Degraded gate (bounded exception).** The strict rule above is the default. The
one exception: a SINGLE reviewer with a reproducible, root-caused TOOLING outage
(not substantive dissent) may be `disregarded` and the gate may pass on the other
three unanimous substantive `GO`s — under the explicit conditions, reroll bar,
authorization (orchestrator inside an autonomous `/loop`, else escalate to the
user), and structured recording defined in the runbook's **Degraded Gate
Policy**. A substantive `BLOCK` is never bypassed.

If the same gate receives `BLOCK` results through attempt 4 (the initial run
plus three reruns), escalate to the user with the reviewer feedback and a
proposed resolution. The user can provide direction, but the gate still
requires a clean three-reviewer `GO` before the workflow advances.

## Gate Semantics

**Review lenses.** Every reviewer seat applies the same four review lenses
(Spec Adversary, Runtime Verifier, Docs/Reality Checker, Security/Data-Integrity
Checker). Lenses are the shared checklist each seat runs; seats are the redundancy.
See [Reviewer Roles (review lenses)](review-gate-runbook.md#reviewer-roles-review-lenses)
in the runbook for the full lens definitions, seat-to-lens mapping, and the lens
block for the Reviewer Prompt Template.

Each reviewer must return:

- `GO` when the phase artifact can advance as-is
- `BLOCK` when required changes are needed before advancing
- required changes when blocking
- optional improvements when giving `GO`

Optional improvements are not blockers and do not require rerunning the review
gate. They must still be implemented before advancing to the next phase unless
they are explicitly recorded as deferred to a named follow-up bead.

The orchestrating agent must:

- wait for all three reviewers to finish
- treat any missing reviewer result as a process blocker
- incorporate all required changes from every `BLOCK`
- rerun the full three-reviewer gate after changes
- after a clean three-reviewer `GO`, incorporate optional improvements or
  explicitly defer them to a named follow-up bead
- record the reviewer identities, results, and change summary in the bead
- count reruns so repeated blocks can be escalated with context

Every rerun is a full re-examination of the updated artifact by all three
reviewers. Prior `GO` results do not carry over after any required-change
rerun.

For rerun counting, the same gate means one phase attempt for one bead. The
initial gate run is attempt 1, and the counter resets when that phase gate
passes.

## Human Input

Humans can still provide missing inputs, decisions, credentials, or manual test
results. That input is not the gate authority.

When a phase is blocked on human input:

- record what input is missing
- request the input from the user
- incorporate the supplied input into the artifact or workflow state
- rerun the three-reviewer gate

## Approval Surface

The approval surface is where review artifacts and reviewer results are shown.
It can be a tmux pane, chat message, PR comment, local file, or another
configured surface.

The approval surface is informational. It does not replace the three-reviewer
gate.
