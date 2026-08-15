# Review Gate Process

Status: planning note. This defines the gate policy to use while building
`etude`.

## Decision

Do not use human approval as the workflow gate.

Every phase gate must pass the panel its stage declares. The stage's
`gate.tier` in `.etude/workflow.yaml` names a tier; `tiers.<L>.seats` in
`.etude/registry.yaml` names that tier's seats. **This document deliberately does
not restate the seat list** — a second copy of it drifts from the config, and the
config is what runs.

A seat is a MODEL identity that reviews the shared gate prompt and votes
GO/BLOCK. Seats are never role personas with different briefs: the redundancy
comes from model diversity, so every seat at a gate gets the identical prompt.

Independent means the seat evaluates the supplied prompt and artifacts without
relying on hidden implementation context. Each seat is a non-interactive
invocation receiving only that prompt, and must be fresh — no carry-over
conversation context from earlier work on the bead.

The gate passes only on a UNANIMOUS `GO` from the tier's seats (registry
`quorum: unanimous`).

If any seat gives `BLOCK`, the blocking feedback must be incorporated and the
same gate run again with all of the tier's seats. Do not advance on partial
approval, and never drop a seat or lower a tier to get past a BLOCK.

**Seat invocation exception.** The `opus` seat's registry `invoke` is
`claude -p --model opus`, which fails to authenticate when the orchestrator is
itself a Claude Code session — the host session's credentials are not exposed to
a subprocess. In that case the seat is run as a fresh in-harness sub-agent
(`subagent_type` general-purpose, `model: opus`) given only the gate prompt, or
via the substitute named in the registry. This exception currently lives in a
registry comment rather than in code, so a machine running the gate cannot honour
it and will fail the seat closed; bead `etude-pqv` makes registry exceptions
machine-honored. See
[Supervisor runbook — Degraded gates and stop-the-line](supervisor-runbook.md#degraded-gates-and-stop-the-line).

If any seat cannot complete because of auth, quota, model access, allowance,
timeout, or tooling failure, stop and escalate. A failed seat invocation is not a
`GO` and must not be skipped.

**Degraded gate (bounded exception).** The strict rule above is the default. The
one exception: a SINGLE seat with a reproducible, root-caused TOOLING outage
(not substantive dissent) may be `disregarded` and the gate may pass on the
remaining unanimous substantive `GO`s — under the explicit conditions, reroll bar,
authorization (orchestrator inside an autonomous `/loop`, else escalate to the
user), and structured recording defined in the runbook's **Degraded Gate
Policy**. A substantive `BLOCK` is never bypassed.

If the same gate receives `BLOCK` results through attempt 4 (the initial run
plus three reruns), escalate to the user with the seat feedback and a proposed
resolution. The user can provide direction, but the gate still requires a clean
unanimous `GO` from the tier's seats before the workflow advances.

## Gate Semantics

**Review lenses.** Every seat applies the same four review lenses
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

The supervisor must:

- wait for all of the tier's seats to finish
- treat any missing seat result as a process blocker
- incorporate all required changes from every `BLOCK`
- rerun the full gate, with all of the tier's seats, after changes
- after a clean unanimous `GO`, incorporate optional improvements or
  explicitly defer them to a named follow-up bead
- record the seat identities, results, and change summary on the run ref
- count reruns so repeated blocks can be escalated with context

Every rerun is a full re-examination of the updated artifact by all of the
tier's seats. Prior `GO` results do not carry over after any required-change
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
- rerun the gate

## Approval Surface

The approval surface is where review artifacts and reviewer results are shown.
It can be a tmux pane, chat message, PR comment, local file, or another
configured surface.

The approval surface is informational. It does not replace the gate.
