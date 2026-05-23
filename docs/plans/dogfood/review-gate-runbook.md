# Review Gate Runbook

Status: planning note. This is the operational checklist for running the
four-reviewer dogfood gate defined in [Review Gate Process](review-gate-process.md).

## Purpose

The review gate process defines the policy. This runbook defines how to execute
it consistently.

Use this runbook for every phase gate while dogfooding `etude`.

## Gate Weight

The four-reviewer gate remains the default authority for product code,
architecture, workflow contracts, storage formats, command behavior, and any
change that could affect users or future compatibility.

For low-risk docs/process maintenance, use a lightweight gate artifact while
keeping the same reviewer authority. A lightweight gate does not remove a
reviewer seat and does not allow the workflow to advance on partial approval.
It only narrows the prompt and evidence to the actual changed docs.

Use the lightweight form only when all of these are true:

- the change touches docs or planning notes only;
- no shipped CLI behavior, schema, storage format, or Go API changed;
- the artifact includes the full changed files or exact diffs;
- the phase owner explicitly states why product tests or manual tests are not
  relevant.

Do not use the lightweight form for:

- product code;
- public CLI behavior;
- manifest, artifact, ref, workflow, or eval schema changes;
- docs that claim new shipped behavior;
- any change after a reviewer asks for broader evidence.

Lightweight gates should still record reviewer results with the normal gate
attempt note format.

## Gate Inputs

Before launching reviewers, collect exact current artifacts:

- bead ID, title, status, labels, and design/notes
- phase name and gate attempt number
- files changed in the phase
- exact contents of reviewable docs or source files
- git status and relevant commit/diff references
- prior reviewer results from earlier attempts of the same gate, if rerunning

Reviewer prompts may include a short orientation summary, but the exact current
artifact contents or exact changed excerpts must be included. Do not rely on a
summary as the sole source of truth.

## Invocation

Run the four reviewers in parallel:

- Gemini Pro
- Claude Opus
- a fresh GPT-5.5 xhigh agent (codex)
- pi/pilms (local qwen via LM Studio)

Each reviewer should run as a non-interactive prompt invocation that receives
only the gate prompt and repository files. They must not rely on hidden
implementation context.

The GPT-5.5 reviewer (codex) must be fresh: start a new isolated agent session
that receives only the gate prompt and artifacts needed for review, not
conversational history from the current bead.

### In-harness Claude rule

When the gate orchestrator is Claude Code (i.e. you are running inside a Claude
session), the Claude Opus reviewer seat MUST be run as a fresh in-harness Task
sub-agent, NOT the external `claude -p` CLI:

```python
Task(subagent_type="general-purpose", model="opus", prompt="<only the gate prompt>")
```

The sub-agent is given ONLY the gate prompt as context, so it is genuinely fresh
and isolated, and it is authenticated through the host session. It is
functionally equivalent to a fresh `claude --model opus -p` seat without the
auth failure. It must still receive the seat-only framing: it is one reviewer
seat, must not orchestrate the panel, and must return only its own verdict.

Only use the external `claude --model opus -p` CLI for the Claude seat when the
orchestrator is NOT Claude (for example when codex or gemini is driving the
gate). Rationale: a nested `claude` CLI spawned from inside a Claude session
returns `401 Invalid authentication credentials` because there is no
`ANTHROPIC_API_KEY` in the environment and the host session's credentials are
not exposed to the subprocess. This is deterministic and recurs every time the
orchestrator is Claude Code.

### Example launch pattern

```text
Gemini Pro:     gemini -m gemini-3.1-pro-preview -p "<gate prompt>"
Claude Opus:    in-harness Task(subagent_type="general-purpose", model="opus",
                prompt="<gate prompt>") when Claude orchestrates;
                otherwise claude --model opus -p "<gate prompt>"
GPT-5.5 xhigh:  spawn a new fresh GPT-5.5 (codex) agent with reasoning_effort=xhigh
pi/pilms:       pilms -p --thinking high "<gate prompt>"
```

`pilms` is the canonical invocation for the pi seat: it is a shell function that
pins the local provider and model
(`pilms () { pi --provider lmstudio --model qwen/qwen3.6-35b-a3b "$@" }`), so it
runs `pi` against the local LM Studio qwen3.6-35b-a3b model with no API auth. The
prompt can be passed inline or piped via stdin like the other seats
(`echo "<gate prompt>" | pilms -p --thinking high`).

Do not advance until all four reviewers return.

## Reviewer Prompt Template

Each prompt should request the same structured result:

```text
Gate review for <bead-id>, <phase> gate, attempt <n>.

You are only the <reviewer-name> reviewer seat, one of four reviewer seats. Do
not act as the orchestrator. Do not invoke other reviewers, judge whether other
reviewer seats ran, or escalate because another reviewer is unavailable. Return
only your reviewer-seat verdict.

Process:
- no human approval gates
- gate passes only if Gemini Pro, Claude Opus, fresh GPT-5.5 xhigh, and pi/pilms
  all return clear GO
- any BLOCK requires incorporating required feedback and rerunning the full
  gate
- reviewer auth/quota/model/tool failure escalates to the user and cannot be
  skipped
- optional improvements from GO reviewers must be implemented before advancing
  or explicitly deferred to a named follow-up bead

Review artifacts:
<exact artifact contents or exact changed excerpts>

Return exactly:
1. GO or BLOCK
2. required changes if BLOCK
3. optional improvements if GO

Be strict. Give GO only if this artifact can advance to the next phase.
```

For Claude Opus in particular, keep the seat-only instruction near the top of
the prompt. Prior gate attempts showed that Claude can otherwise interpret the
shared gate process as an instruction to orchestrate the whole panel. When Claude
Code is the orchestrator, run this seat as the in-harness Task sub-agent
described in the In-harness Claude rule above, not the external `claude -p` CLI.

## Waiting And Status

While reviewers are running:

- report which reviewers have returned
- report which reviewers are still pending
- do not infer failure from silence while a process is still running
- if a reviewer exits with auth, quota, model access, allowance, timeout, or
  tooling failure, stop and escalate to the user

A failed invocation is not a `GO`. Any seat's failure to run still escalates.
Because pi/pilms runs against a local LM Studio model, a pi/pilms failure usually
means LM Studio is not running; start it and rerun the seat rather than skipping
it.

Default wait heuristic: poll quietly for at least 10 minutes before treating a
silent reviewer as suspect. If the process is still alive after that, inspect
the process state and escalate to the user rather than killing or skipping it.

## Result Classification

After all four reviewers return:

- all four `GO`: gate passes after optional improvements are handled
- any `BLOCK`: gate fails; incorporate all required changes and rerun the full
  gate
- any reviewer failure: gate is incomplete; escalate to the user

Optional improvements are not blockers, but they are not ignored. Before
advancing, either:

- implement the optional improvement, or
- create or reference a named follow-up bead and record the deferral

Optional improvements do not require a gate rerun. If an optional improvement
reveals a required design change, record that explicitly and treat it as a new
required-change rerun.

After a gate passes and optional improvements are implemented or explicitly
deferred, continue immediately to the next workflow step. Do not wait for a
separate user prompt unless the process is blocked, reviewer execution failed,
or the next step requires missing user input.

## Reruns

Every rerun is a full re-examination by all four reviewers. Prior `GO` results
do not carry over.

Prior reviewer results are context only on rerun. They explain why the artifact
changed, but they never count toward the new gate.

For rerun counting, the same gate means one phase attempt for one bead. The
counter resets when the phase gate passes.

If the same gate receives `BLOCK` results through attempt 4 (the initial run
plus three reruns), escalate to the user with:

- all reviewer results
- required changes already attempted
- remaining disagreement or blocker
- proposed resolution

The user can provide direction, but the gate still needs a clean
four-reviewer `GO` before advancing.

## Recording Results

Record gate results in bead notes:

```text
<Phase> gate attempt <n>:
- Gemini Pro: GO | BLOCK | failed (<reason>)
- Claude Opus: GO | BLOCK | failed (<reason>)
- fresh GPT-5.5 xhigh: GO | BLOCK | failed (<reason>)
- pi/pilms: GO | BLOCK | failed (<reason>)
- required changes incorporated: <summary or none>
- optional improvements handled: <summary or deferred bead>
- result: pass | rerun required | escalated
```

Example safe append:

```bash
bd update <id> --append-notes "$(cat <<'EOF'
Implement gate attempt 2:
- Gemini Pro: GO
- Claude Opus: GO
- fresh GPT-5.5 xhigh: GO
- pi/pilms: GO
- required changes incorporated: none
- optional improvements handled: clarified runbook examples
- result: pass
EOF
)"
```

If the phase artifact has its own review-gate section, append reviewer results
after review completes. Do not edit the original artifact body just to insert
post-review data.

## Safe Bead Updates

Use stdin or files for long Markdown updates.

Prefer:

```bash
bd update <id> --design-file -
bd update <id> --body-file -
bd update <id> --append-notes "short plain text"
bd update <id> --remove-label phase:implement --add-label phase:verify
```

Avoid inline shell arguments containing Markdown backticks, code fences, quotes,
or multi-line text. Shell interpolation can corrupt the update before `bd`
receives it.

Every bead close carries a one-line rationale: what landed and the commit SHA,
e.g. `bd close <id> --reason "implemented manifest reader, gate passed, f17af3a"`.
A bare `"Closed"` is not sufficient — the reason is the durable record of why
the bead is done once the chat is gone.

## Approval Surface

The approval surface is informational. Use it to show:

- current gate artifact
- reviewer status while waiting
- final reviewer results
- next workflow action

For example, one local setup may use tmux pane `.2`, but that is a transient
session choice. The reusable gate authority remains the reviewer panel.
