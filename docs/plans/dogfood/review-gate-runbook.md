# Review Gate Runbook

Status: planning note. This is the operational checklist for running the
four-reviewer dogfood gate defined in [Review Gate Process](review-gate-process.md).

## Purpose

The review gate process defines the policy. This runbook defines how to execute
it consistently.

Use this runbook for every phase gate while dogfooding `etude`.

## Gate Weight

Match the gate weight to the bead's risk. Pick the tier from the highest-risk
surface the bead touches; when unsure, go heavier. Every tier still requires a
UNANIMOUS pass of its seats — no tier advances on partial approval — and every
seat failure (auth/quota/tool/empty) still escalates per Waiting And Status.

**Tier 1 — Full four-seat gate** (Gemini Pro + in-harness Claude Opus + fresh
GPT-5.5 xhigh + pi/pilms). The default and required tier for anything that can
affect users, data, or future compatibility:

- product code and public CLI behavior;
- manifest, artifact, ref, workflow, or eval schema/format/storage changes;
- anything that reads or writes the `refs/etude/*` namespace or git plumbing;
- any change that could lose or corrupt data, or break backward compatibility;
- docs that claim NEW shipped behavior (reviewers must verify docs against code).

Examples this tier caught real bugs on: `etude sync`, refstore hardening.

**Tier 2 — Lightened two-seat gate** (in-harness Claude Opus + fresh GPT-5.5
xhigh — the two highest-signal seats; drop the slower Gemini and pi/pilms seats).
Use for LOW-RISK code changes that touch none of the Tier-1 surfaces: small
polish, localized refactors, validation tightening, or test strengthening on an
existing, already-gated component. Example: tightening `capture --git-sha`
validation + adding a table test.

**Tier 3 — Single-seat gate** (in-harness Claude Opus). Use only for changes with
NO shipping-code change: test-only additions (e.g. godoc `Example` functions) or
docs/planning-notes-only changes. Examples: internal API examples, a deferred-
decisions planning note.

Escalation is mandatory and overrides the tier: if a bead picked for Tier 2/3
turns out to touch a Tier-1 surface, or ANY reviewer (or the orchestrator) finds
it changes shipped behavior/schema/storage or could lose data, STOP and rerun at
Tier 1. Tier choice is recorded in the gate attempt note (e.g. "gate: Tier 2
(Opus + Codex) — low-risk capture polish, no Tier-1 surface").

**Lightweight artifact (composes with any tier):** for docs/planning-only work,
narrow the gate prompt and evidence to the actual changed files/diffs and have
the phase owner state why product/manual tests are not relevant. This is about
the prompt scope, independent of how many seats the tier uses.

All tiers record reviewer results with the normal gate attempt note format.

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

**Reviewer seats MUST NOT mutate the working tree.** A pre-commit gate reviews
UNCOMMITTED work, so any `git checkout`/`git restore`/`git stash`/`git reset` a
seat runs to "revert" a mutation test silently discards the implementation under
review. (This happened once: a seat's mutation-test revert wiped the producer
wiring out of `internal/cli/capture.go` mid-gate, and later seats then BLOCKed on
"unknown flag".) The orchestrator MUST snapshot the changed files to a `/tmp`
path BEFORE dispatching any seat, pass each seat an explicit read-only
instruction, and after each reviewer batch verify the changed files still match
that snapshot before committing.

Per-seat sandbox constraints (learned from real spirals):

- **codex**: its sandbox BLOCKS writes outside the project dir, so do NOT tell it
  to copy the repo to `/tmp` or mutation-test — it will spiral retrying rejected
  copy/patch commands until killed. Instruct codex to **review from the diff
  ONLY** and trust the provided green test results; never tell it to reconstruct
  a build env. **Dispatch codex diff-only from the FIRST attempt — do NOT let it
  run `go build`/`go test`/`go vet`.** When codex runs the suite it reliably
  HANGS after the test output, before emitting its GO/BLOCK line (observed on
  phase2.4, replay-command, and phase2.5 final gates — each required killing it
  and re-dispatching diff-only). Embedding "do NOT run go build/go test; the
  green results are provided and trustworthy" in the first prompt avoids the
  kill-and-re-dispatch cycle entirely.
- **in-harness Opus / other seats** with normal filesystem access MAY mutation-test
  by copying to `/tmp` and mutating the copy, never the repo file.
- **gemini**: when `ripgrep` is unavailable in gemini's environment it falls back
  to a GrepTool that BLEEDS matches across files, and has reproducibly
  misattributed string literals from one file (e.g. a planning doc path) to an
  unrelated test file — producing a confident BLOCK on a phantom assertion that
  grep proves does not exist. ALWAYS ground-truth-check a gemini BLOCK that cites
  a specific string in a test file (grep the real file + run the test) before
  acting; a gemini verdict contradicted by grep + passing tests is a tool
  artifact, not a defect. (Tracked for a durable fix.)

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
pi/pilms:       pilms --tools read,grep,find,ls,bash -p "<gate prompt>"
```

`pilms` is the canonical invocation for the pi seat: it is a shell function that
pins the local provider and model
(`pilms () { pi --provider lmstudio --model qwen/qwen3.6-35b-a3b "$@" }`), so it
runs `pi` against the local LM Studio qwen3.6-35b-a3b model with no API auth.

**The pi seat MUST run with a read-only tool allowlist — never `--no-tools`.**
`pi` is an agentic CLI; a reviewer is only useful if it can independently read
the actual files and run `git diff` / `go test` rather than trust the
orchestrator's summary. Use `--tools read,grep,find,ls,bash` (read + inspect +
shell, no edit/write). Two failure modes to avoid:

- Leaving ALL tools enabled (the default) can hang `pi` in `-p` mode. Restrict
  to the read-only allowlist above.
- `--no-tools` makes the seat blind, so it rubber-stamps the prompt summary and
  catches nothing. Do not use it.

The gate prompt for this seat should tell it to USE its tools to read the
changed files and the diff (it cannot review what it cannot see). The prompt may
be passed inline as the final arg (shown above) or piped via stdin.

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

The pi/pilms model `qwen/qwen3.6-35b-a3b` is reasoning-first: it emits a large
`reasoning_content` chain-of-thought (often 2,500+ tokens) BEFORE any answer
`content`. In `pi --mode text` nothing prints until the reasoning finishes, so a
pi/pilms seat legitimately sits at zero output for a long time and looks hung
when it is not. With `--tools` enabled each agentic round pays that reasoning
cost again, so a single seat can take many minutes. Do NOT kill it on a short
(1-5 minute) timeout or assume an empty output file means failure — give it a
generous budget (~15 minutes) and let it finish. The reasoning cannot be turned
off here: `pi --thinking off`, the qwen `/no_think` token, and
`chat_template_kwargs.enable_thinking=false` were all verified NOT to suppress it
on this LM Studio build. An occasional truly-empty completion is a model glitch;
rerun the seat (LM Studio is up) rather than treating one empty run as a verdict.

Debug a recurring seat flake on its SECOND occurrence, not its fifth. If a seat
hangs, empties, or errors twice in a session, stop blind rerun/kill cycles and
root-cause it (probe the underlying service directly — e.g. `curl` the model
endpoint, run the seat's CLI with a trivial prompt, check the process state)
before any further reruns. Repeatedly re-launching a flaky seat without
diagnosing it burns gate rounds; one focused investigation usually yields a
durable fix (and a note in this runbook). This applies the standing rule: when
you hit recurring friction, investigate the root cause instead of improvising
around it.

## Result Classification

After all four reviewers return:

- all four `GO`: gate passes after optional improvements are handled
- any `BLOCK`: gate fails; incorporate all required changes and rerun the full
  gate
- any reviewer failure: gate is incomplete; escalate to the user

**Autonomous-loop tooling-outage fallback.** In an autonomous `/loop` there is no
user to escalate to in real time, and blocking forever on one flaky seat stalls
the loop. So: if a SINGLE seat has a REPRODUCIBLE tooling outage — empty
completion or pre-verdict hang that recurs after ≥2 rerolls AND is root-caused to
a known tooling artifact (e.g. pilms/LM Studio empty completion, codex go-test
hang), NOT a substantive dissent — AND the other THREE seats (the substantive,
high-signal ones) are unanimous `GO` after thorough review, treat the gate as
passed and record the outage explicitly in the bead notes (which seat, the
artifact, how many rerolls). This is a deliberate, documented exception, not a
license to skip a seat that raises real findings: a seat that returns an actual
BLOCK is never bypassed. Outside an autonomous loop, still escalate to the user.

When a `BLOCK` rests on a disputed factual claim about tool behavior (e.g. "this
git command exits 0", "the CLI prints X"), or two reviewers disagree on such a
fact, the orchestrator REPRODUCES the behavior empirically before reworking — do
not change code or docs to satisfy a BLOCK that may be wrong. If the claim is
confirmed, rework and rerun. If it is disproven, do not apply the change; rerun
the gate with the empirical evidence embedded in the prompt so the panel
converges on the verified behavior, and record the resolution in the gate note.
A reviewer's confident assertion is not authoritative over a reproduced result.

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
