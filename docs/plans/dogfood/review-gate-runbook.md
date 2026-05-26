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

**Derive "ground-truth" facts in the prompt from the SOURCE, not from memory.**
When a reviewer prompt asserts ground-truth a seat will check the artifact against
(a schema rule, an existing convention, a CLI behavior), quote or derive it from
the actual source (the runbook/spec/code), not a paraphrase from the orchestrator's
recollection. An INCOMPLETE paraphrase makes a seat correctly BLOCK on a "violation"
that is not a defect, costing a disputed-claim verify-and-rerun cycle. Observed:
a gate prompt stated the disregard reroll bar as ">=2 rerolls" but omitted the
runbook's documented single-confirming-reroll shortcut for an already-known
artifact, so codex BLOCK'd the plan for "loosening" a bar it was actually faithful
to — resolved only after re-reading the runbook and rerunning with the full rule.
If unsure a paraphrase is complete, tell the seat to verify it against the named
source rather than treating the paraphrase as authoritative.

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
that snapshot before committing. **The snapshot must PRESERVE directory
structure** (snapshot `internal/gc/gc.go` to `<tmp>/internal/gc/gc.go`, not a
flat `<tmp>/gc.go`). Flattening collides files that share a basename — e.g.
`internal/gc/gc.go` and `internal/cli/gc.go` both land at `<tmp>/gc.go`, the
second `cp` overwrites the first, and the clobber-check then reports a phantom
DIFF on one and could silently MASK a real mutation of the other. Use
`cp --parents` (or `rsync -R`, or per-file `mkdir -p`) so each snapshot path is
unique.

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
  kill-and-re-dispatch cycle entirely. **Keep codex's inline input SMALL — review
  the DIFF / the changed production files only, never a full-file + full-test
  dump.** On large inline inputs (observed at ~1000+ lines: the assertion-eval and
  bench-cohort impl gates) codex reliably emits its preamble and then TRUNCATES
  without ever printing a VERDICT line (exit 0, no verdict — looks like an empty
  completion). The identical gate with a focused input (≈600 lines of production
  code + a one-line test summary, or a small delta on a rerun) completes cleanly.
  So: inline only the changed production code + the diff, summarize the tests in
  prose (don't paste them), and on a rerun send just the delta. If codex returns
  no VERDICT, treat it as a truncation glitch (reroll with a smaller input), not a
  silent GO. As a rough budget, keep the codex prompt under ~700 lines.
  **On a DESIGN/DOC gate (reviewing a planning note, not code), inline the whole
  artifact + the line citations it makes, and tell codex to "reason ONLY from the
  inlined note; do NOT read repo files."** When a doc gate says codex "MAY read"
  the repo, it has no diff to anchor on and spiders the entire tree — observed
  ballooning to 200 KB+ of output on the bench-retro design gate before finishing.
  A doc review needs the artifact text + the few cited facts, not a repo crawl.
- **in-harness Opus / other seats** with normal filesystem access MAY mutation-test
  by copying to `/tmp` and mutating the copy, never the repo file.
- **gemini**: when `ripgrep` is unavailable in gemini's environment it falls back
  to a GrepTool that BLEEDS matches across files, and has reproducibly
  misattributed string literals from one file (e.g. a planning doc path) to an
  unrelated test file — producing a confident BLOCK on a phantom assertion that
  grep proves does not exist.

  **Root cause:** gemini-cli's `getRipgrepPath()` looks ONLY for a bundled binary
  at `<bundle>/vendor/ripgrep/rg-<platform>-<arch>[.exe]` — it never consults
  system `rg` on PATH. When that vendor path is absent (it is not shipped in
  current gemini-cli builds), `ensureRgPath()` throws and gemini registers the
  bleeding GrepTool, logging "Ripgrep is not available. Falling back to GrepTool."

  **Durable fix:** run `scripts/provision-gemini-ripgrep.sh` once per machine.
  The script creates the expected vendor path as a symlink to the system `rg` so
  gemini finds and registers RipGrepTool on startup. Re-run after any
  gemini-cli reinstall or upgrade — upgrading wipes the `vendor/` dir and its
  symlinks. The script is idempotent; re-running when already provisioned is a
  safe no-op.

  **Defense-in-depth backstop** (covers machines where the symlink is missing):
  ALWAYS ground-truth-check a gemini BLOCK that cites a specific string in a test
  file (grep the real file + run the test) before acting; a gemini verdict
  contradicted by grep + passing tests is a tool artifact, not a defect.
  **Dispatch gemini with the changed files' content INLINED in the prompt from
  the FIRST attempt** (same as codex's diff-only discipline), and tell it to
  reason only from the inlined code without calling tools. This avoids two
  observed cycle-wasters at once: gemini trying `run_shell_command` (which is NOT
  in its toolset — it errors `Tool "run_shell_command" not found` and burns an
  attempt before recovering via GrepTool), and the GrepTool cross-file
  misattribution above (no file reading is needed when the code is already in the
  prompt).

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

**Third failure mode — `pi` client 0-CPU hang with a healthy backend.** Distinct
from slow-reasoning (above) and LM-Studio-down. Symptom: the `pi` process stays
alive but emits ZERO output indefinitely, and `ps` shows it at `0:00.00` CPU.
Diagnose it cheaply rather than waiting the full 15-minute reasoning budget:
after the seat has been silent ~2 minutes, check BOTH (a) `ps aux | grep "[p]i --provider lmstudio"`
for `0:00.00` CPU and (b) `curl -s -m5 localhost:1234/v1/models` for a 200 with
the model listed. If the process is at 0 CPU AND the backend is healthy, the
client is hung (NOT reasoning, NOT a down backend) and will never produce a
verdict — kill it immediately; do not wait out the 15-minute budget. This was
reproducible across an entire session (rubric-eval gate ×2, pairwise-eval plan +
impl gates) in both tool-mode and inline `-p` mode while LM Studio served other
clients fine. Because it is a known, root-caused client artifact, ONE 0-CPU hang
(after a single reroll confirming it recurs) satisfies the "reproducible tooling
outage" bar for the Degraded Gate Policy below — do not burn four 15-minute
waits re-confirming a hang you have already diagnosed this session.

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

**Reviewer failure / tooling outage.** A reviewer failure (auth/quota/empty/hang/
tool error) makes the gate INCOMPLETE — escalate, never treat as `GO`. The single
bounded exception — when one outage seat may be `disregarded` and a degraded gate
may still pass on the other three substantive `GO`s — is the **Degraded Gate
Policy** below.

When a `BLOCK` rests on a disputed factual claim about tool behavior (e.g. "this
git command exits 0", "the CLI prints X"), or two reviewers disagree on such a
fact, the orchestrator REPRODUCES the behavior empirically before reworking — do
not change code or docs to satisfy a BLOCK that may be wrong. If the claim is
confirmed, rework and rerun. If it is disproven, do not apply the change; rerun
the gate with the empirical evidence embedded in the prompt so the panel
converges on the verified behavior, and record the resolution in the gate note.
A reviewer's confident assertion is not authoritative over a reproduced result.

**Seats split on an approach/risk choice (not a fact, not a correctness bug).**
Sometimes seats disagree on a design CHOICE where no one is factually wrong — one
seat finds an approach acceptable or even preferable while others `BLOCK` it as
too risky or too complex (observed: codex+gemini BLOCK'd a cobra
default-subcommand shim as fragile/regression-prone while Opus empirically proved
the shim *works* and preferred it). Resolve by choosing the option that ALL seats
find acceptable — the consensus-safe option — especially when it is also the
lower-complexity choice. A lone `GO` defending the riskier approach does NOT
override two `BLOCK`s prescribing a safer one: empirical proof that the riskier
approach *functions* informs the decision but does not by itself outweigh
maintainability/regression objections ("it works" is not "it is the right
surface"). Record the chosen option and why in the gate note. Corollary for PLAN
gates: a gated plan must COMMIT to a single approach for any decision the gate
will scrutinize. Leaving "approach A, or fall back to B if A proves hard" is
itself a BLOCKable defect — it defers a reviewed decision to un-reviewed
implementation time; the plan must pick one before the gate passes.

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

## Degraded Gate Policy

The DEFAULT is strict: a gate passes only on a UNANIMOUS substantive `GO` from its
tier's seats, and a seat returning an actual `BLOCK` (substantive dissent) is
NEVER bypassed. This section makes the bounded exceptions explicit so the written
process matches real practice; it does not weaken the default.

**1. Block vs. recoverable retry.** A seat that exits with
auth/quota/model-access/timeout/tool-invocation failure, or returns empty/no
verdict, is FIRST a recoverable retry: reroll and root-cause per "Waiting And
Status" (the second-occurrence debug rule, the pi/pilms reasoning budget, the
0-CPU-hang diagnosis under "Third failure mode"). Until it is resolved or meets
the disregard bar below, the gate is INCOMPLETE (verdict `failed`/`empty`) — an
unresolved or undiagnosed failure is never a `GO`.

**2. Disregarding a seat (the bounded exception).** A SINGLE seat may be
`disregarded` ONLY when ALL hold: (a) it is a reproducible TOOLING outage
(empty/hang/auth/quota), NOT substantive dissent; (b) root-caused to a known
tooling artifact (e.g. the pi/pilms 0-CPU client hang, codex go-test hang, pilms
empty completion); (c) REROLL BAR — `>=2` rerolls to ESTABLISH a NOVEL outage as
reproducible, or, for an outage ALREADY documented here as a known root-caused
artifact (e.g. the pi/pilms 0-CPU hang under "Third failure mode"), a single
confirming reroll — this shortcut is bounded to already-known/root-caused
artifacts, NOT a general 1-reroll allowance; (d) the OTHER THREE seats are
unanimous substantive `GO` after thorough review. A DISPROVEN `BLOCK`
(ground-truth contradicts a factual claim) is NOT a disregard — it is handled by
the disputed-factual-claim rule under Result Classification (verify empirically,
do not apply the change, rerun with the evidence embedded). A substantive `BLOCK`
is never disregarded.

**3. Degraded 3-seat gate — allowed, authorized, recorded.** When rule 2 holds,
the gate MAY pass on the three substantive `GO`s. WHO authorizes: inside an
autonomous `/loop` there is no real-time user, so the ORCHESTRATOR authorizes
under exactly these conditions; OUTSIDE an autonomous loop, escalate to the user
instead. It is always RECORDED: which seat, the artifact/diagnosis, and the
reroll evidence.

**4. Structured recording (shipped schema).** Capture the degraded gate as a
normal `GateAttempt` (status `pass`) via `etude capture-gate`: the outage seat
carries verdict `malfunction` (or `failed`/`empty`) with a `failure_note`; the
disregarded seat carries verdict `disregarded` + `failure_note`; and
`decision.degraded_reason` records which seat, the evidence, and the reroll count.
No new schema field is needed (see docs/gates.md and
docs/plans/product/gate-reviewer-record-schema.md).

## Reruns

Every rerun is a full re-examination by all four reviewers. Prior `GO` results
do not carry over.

Prior reviewer results are context only on rerun. They explain why the artifact
changed, but they never count toward the new gate.

For rerun counting, the same gate means one phase attempt for one bead. The
counter resets when the phase gate passes.

**Incorporating a PLAN-gate BLOCK: distinguish a missing detail from a
conceptual contradiction.** A missing-detail BLOCK ("add validation X",
"specify the seed") bounces back to the planner cleanly — it appends the detail.
But when the BLOCK exposes a CONCEPTUAL contradiction in the design (the plan's
own model is internally inconsistent — e.g. gc-command defined "prune the
unreachable runs" while also stating leaf runs are kept work, leaving "what does
--prune delete?" undefined), bouncing it back with an open-ended "resolve the
contradiction" tends to make the planner re-derive the SAME flawed framing
(observed: two wasted gc-command planner round-trips). For a conceptual
contradiction, either hand the planner the PRESCRIPTIVE resolved model to write
up, or author the corrected design directly (the planner's exploration —
file refs, structure, tests — is still reused). Don't round-trip a contradiction
open-ended.

**Recurring avoidable plan-gate blocks — preempt them in the plan.** Across the
gate-reviewer-visibility epic, most plan-gate BLOCKs were the same two shapes;
plans that handle these up front avoid a rerun round:

- **Cover ALL of a schema type's fields, including optional ones.** When a change
  renders, documents, captures, or tests a struct, address every field of that
  struct (or explicitly justify each omission). Partial coverage drew repeat
  BLOCKs (omitting `SeatResult.Skill` from `run show`; leaving `optional`/
  `raw_output`/`escalation`/`deferred` rendering untested; omitting `skill` from
  the docs field listing). "I covered the common/required fields" is not "I
  covered the type."
- **Read acceptance criteria literally.** Satisfy the words as written, not a
  convenient reinterpretation (e.g. "a NEW dogfood run demonstrates X" means a
  new run, not an append/backfill of an existing one — backfill was a separate
  bead). When a criterion is ambiguous, state the chosen reading in the plan.
- **Verify before any irreversible step.** A script/process that mutates then
  pushes must verify the local result BEFORE the push, and be exercised in a
  throwaway repo before first real use (see Scope Discipline / isolation rule).

Also: a BLOCK that rests on a factual claim about behavior (e.g. "this example is
invalid") is verified empirically before reworking (see Result Classification);
do not edit to satisfy a disproven claim — rerun with the evidence.

If the same gate receives `BLOCK` results through attempt 4 (the initial run
plus three reruns), escalate to the user with:

- all reviewer results
- required changes already attempted
- remaining disagreement or blocker
- proposed resolution

The user can provide direction, but the gate still needs a clean
four-reviewer `GO` before advancing.

## Scope Discipline (implement → gate)

The bead's commit must contain ONLY this bead's change (1 bead = 1 commit). The
implementer (and any implementing sub-agent) touches ONLY the files in the
approved plan's **Files** list, plus their tests and any file the plan's change
mechanically forces (e.g. a regenerated reference). Do NOT, on your own
initiative, fix unrelated drift, refactor adjacent code, update docs the plan did
not name, edit process docs, or write a retro — even when you spot a real problem.
Drift or cleanup you discover is filed as a SEPARATE bead, never folded in.

Before the gate, the orchestrator runs a scope-check: `git status` and diff the
working tree against the plan's **Files** list. Any file changed that the plan did
not name is out-of-scope — investigate it, then revert it (preserving it elsewhere
if it has independent value, e.g. under the bead it actually belongs to) so the
commit stays scoped. Sub-agents report what they *intended*; verify what they
*did*. (Observed: an implementing sub-agent silently fixed unrelated README/BRIEF
doc drift and wrote a retro doc while implementing a schema bead; the scope-check
caught it.)

The orchestrator owns the commit — sub-agents never run `git commit`. Before the
wrap-up commit, check `git log -1`/`git status`: if the change is already
committed (concurrent/autonomous settings), verify its scope instead of creating
a duplicate.

**When implementation reveals the gated PLAN is wrong: deviate correctly, then
FLAG the deviation to the implement gate.** Scope discipline ("follow the plan")
must not become "follow a plan that turned out factually wrong." If implementing
exposes a wrong assumption in the gated plan (observed: a plan required matching
each command as `etude <cmd>` in BOTH README and docs/README, but docs/README is a
link INDEX — the predicate false-positived every command), do NOT silently follow
the broken plan, and do NOT silently deviate. Implement the CORRECT thing, then
state the deviation explicitly in the implement-gate prompt ("the plan said X;
implementation does Y because the plan's assumption Z is wrong") so the panel
validates the deviation rather than rubber-stamping. This is distinct from
out-of-scope drift (which is reverted): a deviation stays IN scope (same goal,
corrected approach) and is surfaced for review, not hidden.

## Recurring Defect Classes (implement gate)

Defect classes the gate caught repeatedly across the etude-14r feature
(q87/8t4/n0t), the misc-backlog sweep (0rt/712/4o0), and the Phase-C extras
(egg/2ku/qih/aqt), cheap to catch up front. Both the implementer and the gate
should check them.

**1. Reserve every command-generated `Refs`/manifest key against `--ref` (or any
passthrough) override.** When a command writes keys into a map that a passthrough
flag (`--ref key=value`, `--meta`, …) later MERGES, any generated/validated key the
passthrough can also write is silently overwritable — letting a user bypass
validation or falsify provenance.
- **Why:** this recurred. q87 first shipped with `--ref subject_run.1=`/`scope=`
  able to overwrite the `IsValidRunID`-validated subjects + authoritative scope
  (caught at the implement gate). n0t then REINTRODUCED the same class: it added a
  `generator`/`produced_via` provenance key but only reserved `produced_via`, so
  `--ref generator=hack` could spoof which generator produced a retro (caught
  again). The fix pattern (a reserved-exact-keys + reserved-prefixes guard that
  rejects colliding `--ref` keys) existed already; the second time it just wasn't
  extended to the NEW key.
- **How to apply:** whenever you add a command-generated key (flat or indexed) to
  a manifest/Refs map that a passthrough flag also merges, add that key (or its
  prefix) to the reserved-key guard in the SAME change. Gate check: enumerate every
  key the command itself writes and confirm each is reserved against the
  passthrough. A new provenance/identity key with no matching reserved entry is a
  BLOCK.

**2. The in-harness (repo-aware) reviewer seat must do ADVERSARIAL + spec-
completeness review, not just "does it work as the implementer intended."** The
repo-aware seat runs tests and mutation-tests and is excellent at confirming the
happy path and the implementer's intent — but across q87/8t4/n0t it GO'd four
times on changes that the spec-focused inlined seats (codex/gemini) correctly
BLOCKED: the two `--ref` override holes above, `retro show` silently dropping
`gate/bench/eval`/custom metadata, and `resolveSubjectStage` silently picking one
arbitrary stage of a multi-stage run.
- **The pattern continued into the Phase-C extras (qih/aqt), and notably it is
  often codex — not the repo-aware Opus seat — that catches these even at the
  IMPLEMENT gate where the built binary is in hand:** on `etude log` (qih) the
  `--subject` filter matched a retro by its OWN retro id (the spec says retros
  match only by their `subject_run`/`bead` subjects) — both the in-harness Opus
  seat AND gemini GO'd, gemini explicitly RATIONALIZING the line as correct; codex
  BLOCKED on reading the match-set. On the retro-meta sidecar rendering (aqt) the
  `--- retro meta ---` divider would glue onto a body printed with `Fprint` that
  lacked a trailing newline — Opus said "no divergence", gemini "defensively
  sound"; codex BLOCKED at the PLAN gate before a line was written. In both the
  defect was a spec/output invariant the tests did not cover, so "tests pass"
  proved nothing.
- **Why:** the blind spot is systematic. "Run it, it works" and "the implementer's
  tests pass" do not surface (a) fields the acceptance requires that are silently
  dropped, (b) inputs the spec ALLOWS that bypass validation/spoof/inject, or
  (c) heuristics that silently select the wrong thing, or (d) a match-set/output
  invariant stated in PROSE that no test enforces. The inlined seats, judging
  against the spec/precedent rather than the running code, catch these. This is
  the concrete evidence that the multi-seat gate is load-bearing — do NOT collapse
  a Tier-1/Tier-2 gate to the single repo-aware seat, and do NOT let two GO seats
  outweigh one source-cited BLOCK: every BLOCK this run that two seats missed was
  verified TRUE against source.
- **How to apply:** the in-harness seat's brief must explicitly demand, beyond
  "run the tests": (a) enumerate every field/key the acceptance requires and verify
  each is rendered/stored/handled, not silently dropped; (b) try adversarial inputs
  that bypass validation (override a generated key, spoof provenance, inject via an
  unvalidated value); (c) for any selection heuristic, construct the input where it
  picks wrong and confirm it errors rather than silently proceeding.

**3. A negative/failure-mode test must exercise the claimed failure path for the
RIGHT reason — verify it fails on the right injected fault, not a neighbouring
one.** A test named for fault X that actually trips on fault Y gives false
confidence: the guard for X is unproven.
- **Why:** caught at etude-712's PLAN gate (a test-only/dev-tooling bead — the
  rigor applies there too, not just product code). The drift guard derives its
  expected set from the GENERATED dir; the plan's "delete a generated file →
  proves missing-committed" sub-test actually only proved ORPHAN detection (the
  committed copy becomes the orphan), and there was no byte-stale case at all. So
  two of the three real fault paths (missing-committed, byte-stale) were unproven
  while the test looked thorough. Fixing it required mutating a temp COMMITTED copy
  (generated left whole) for "missing", a separate stray file for "orphan", and a
  byte change for "stale" — three distinct injections.
- **How to apply:** for each negative/guard test, name the exact fault it injects
  and confirm the guard fails *because of that fault* — inject ONE fault at a time,
  on the correct side of the comparison, and assert the error names that specific
  victim. A helper that takes the inputs as parameters (so a test can feed it
  crafted faulty inputs) is the enabler. Gate check: does each failure-mode test
  prove a DISTINCT path, or do several collapse onto the same one?

**4. An "X appears in rendered output" assertion must match X at its exact
rendered SLOT (whole token + position), never via substring/`Contains` — names
that PREFIX or NEST inside other names will silently satisfy a loose check.**
- **Why:** etude-7no's `etude prime` drift guard (assert every registered command
  appears in the primer's command list) took FOUR implement rounds because the
  membership check was too loose, with a fresh collision class surfacing each time:
  (r2) `strings.Contains(primer, "capture")` passed even with the `capture` line
  deleted, because "capture" is a substring of the prose/other lines; (r3, after
  switching to first-field match) `fields[0]=="capture"` still false-matched
  nothing — but the SIBLING prefix `capture` vs `capture-gate` and then (r4) the
  PARENT/child `run` vs `run list` both slipped through, because `run list`'s first
  field is also `run`. Only INDENT-anchored, whole-token matching
  (`"  run "` 2-space top-level vs `"    run list "` 4-space subcommand, trailing
  space to exclude `capture-gate`) closed all classes. Each loose check looked
  fine until the specific colliding name existed. (Same theme as etude-712's
  gen-docs guard — output-membership guards are a recurring trap.)
- **How to apply:** when a guard asserts a derived/registered set appears in
  rendered text, anchor each item to its exact rendered position — line start +
  indent + the item as a whole token followed by a delimiter — NOT `Contains` and
  NOT a bare first-field match. ENUMERATE the collision classes up front: does any
  name prefix another (`capture`/`capture-gate`)? Does a parent share its first
  token with its children (`run`/`run list`)? Write the matcher to distinguish all
  of them in round one, and prove it by reasoning "if I drop the `run` line while
  keeping `run list`, does this fail?" before shipping.

**5. An OPTIONAL config/struct block must preserve the absent / present-null /
present-empty distinction — a plain `*T` pointer field conflates absent with
present-null, and synthesizing defaults on parse destroys the presence bit and
breaks round-trip.** When adding an optional nested block (e.g. a new
`workflow.yaml` section, an optional manifest field), the three states absent vs
present-but-null (`block:` / `block: null`) vs present-empty (`block: {}`) are
distinct and often need distinct behavior.
- **Why:** etude-egg (the `retros:` block) BLOCKED twice on exactly this. Plan
  round 1: the design SYNTHESIZED the block (`Retros = &{defaults}`) when absent —
  which destroyed the presence bit (`Validate` couldn't gate "generator required"
  on the block being present vs defaulted) AND broke round-trip (a legacy file
  with no block re-encoded WITH a spurious block, since `omitempty` only drops
  nil). Implement round 1: decoding the block as `Retros *T` made a present-null
  `retros:`/`retros: null` decode to `nil` — indistinguishable from absent — so it
  silently skipped validation. Both were caught by the spec-focused seats (codex/
  gemini reasoning about the STATE MODEL), not the test-running seat.
- **How to apply:** (a) keep the field NIL for a genuinely-absent block (never
  synthesize on parse) so `nil ⇔ absent` and `omitempty` keeps legacy round-trips
  byte-stable; (b) compute effective defaults via ACCESSOR methods (read-time), not
  by mutating the struct; (c) to distinguish absent from present-null, decode via
  `yaml.Node` (Kind==0 absent; `!!null` scalar present-null; mapping present) — a
  plain `*T` cannot; (d) when re-marshalling a captured node, re-impose
  `KnownFields(true)` (node `.Decode` does not inherit it); (e) gate any
  presence-conditional validation on the field being non-nil. Test all three
  states explicitly (absent / `block:` / `block: {}`) plus a legacy byte-stable
  round-trip.

**6. A change that touches GENERATED artifacts has a blast radius beyond its own
file — the plan's file-scope must enumerate EVERY generated output the change
regenerates, not just the obvious one.** Adding, renaming, or removing a command
or flag does not regenerate only that command's own page; it also rewrites the
root/index pages that list or cross-reference it.
- **Why:** etude-qih's PLAN gate BLOCKED on exactly this. The plan added a new
  top-level `etude log` command and listed only `docs/cli/etude_log.md` in scope —
  but `make docs` ALSO rewrites `docs/cli/etude.md`, whose generated `SEE ALSO`
  section lists every top-level command alphabetically. So the new command inserts
  a line into the root page too; committing only the new page leaves
  `docs/cli/etude.md` stale and `make docs-check`/`gen-docs TestDriftGuard` red.
  The plan looked complete because it named *a* generated file — just not all of
  them. (Same family as the gen-docs guard traps in #3/#4: generated-output
  reasoning is a recurring blind spot.)
- **How to apply:** before finalizing scope, RUN the generator (`make docs`) and
  `git status` to see the true set of changed files, OR reason explicitly about
  the blast radius: a new/renamed/removed command → its own `cli/etude_<cmd>.md`
  PLUS the root `cli/etude.md` `SEE ALSO` PLUS any hand-maintained index
  (`docs/README.md`) PLUS README usage that `docs-reality` checks; a new/renamed
  flag → that command's generated page. Gate check: does the file-scope match what
  the generator actually emits? A scope that lists the new page but omits the
  regenerated root/index page is a BLOCK.

## Plan-Phase Discipline

Two plan-phase practices the gate enforces BEFORE design is accepted. Unlike the
implement-gate defect classes above, these are about the plan itself.

**P1. Verify the verification — when a bead's acceptance rests on an equivalence /
escape / property proof, the PLAN must specify a proof that actually RUNS, uses
commands/flags that EXIST, normalizes exactly the volatile fields, and covers the
full fidelity/threat surface. The gate vets the proof method, not just the change.**
- **Why:** etude-21z's PLAN gate was BLOCKED by ALL THREE seats — not on the
  rewrite (a faithful 4-`capture`→1-`capture-run` swap) but on its VERIFICATION,
  which was broken three independent ways: it diffed `etude run show --json` (no
  such flag exists — run.go:60 defines none); it used throwaway ids `eq-old`/`eq-new`
  that embed into `refs.bead` but normalized only run_id/created/timestamps (the
  diff would falsely fail); and comparing `run show` TEXT would have missed
  artifact content-hashes + media_type entirely. A "passing" equivalence check that
  cannot run, or that is blind to the fields that matter, proves nothing. (Compare
  etude-094, where the proof was an EMPIRICAL adversarial escape probe + a planted
  secret leak-audit — that proof was load-bearing precisely because it actually
  exercised the threat.)
- **How to apply:** for any proof-backed bead, the plan states the EXACT proof
  commands; confirm each flag/command exists (grep/`--help`), enumerate every
  volatile field to normalize (run_id, created, per-stage timestamp, AND any
  id-derived ref like `refs.bead`), and confirm the comparison surface includes
  the load-bearing fields (for manifests: diff the raw `manifest.json` blob, which
  carries artifact hashes + media_type, NOT the human `run show` text). Gate check:
  trace the proof end-to-end — would it actually execute, and would it catch a real
  divergence? A proof that can't run or is field-blind is a BLOCK.

**P2. Premise-check before designing — confirm the bead's premise holds (the data
exists, the dependency is stable, the value is real) and recommend DEFER (with the
concrete prerequisite) rather than building speculative infra over a hypothetical.**
- **Why:** etude-9ey ("cross-retro failure-mode index") was correctly DEFERRED at
  plan time, not built: (a) ZERO retros carry a sidecar yet (`refs/etude/retros/*`
  is empty), so the aggregation had no input; (b) the retro-meta sidecar is
  schema-free BY DESIGN (etude-2ku stores it verbatim, json.Valid only) — an index
  would be the first place to bless a schema, inverting that posture; (c) the
  de-facto convention even CONTRADICTED itself (`root_cause` vs `root_causes`).
  Building would have produced speculative read-side code over an empty, unstable
  source — exactly "designing for hypothetical future requirements." The deferral
  instead spun off the real prerequisite (etude-sb4: pin + document the convention).
- **How to apply:** the planner's FIRST output for a feature bead is a
  BUILD-vs-DEFER call. If the premise fails (no data, unstable/contradictory
  dependency, no concrete consumer), recommend DEFER, name precisely what must
  exist first, file/point at that prerequisite bead, and `bd defer` — do NOT write
  speculative code just because a bead is open. Do NOT default to BUILD.

## Epic-Close Gate

Closing an epic is a gated action with a mandatory pass/fail check.

**The gate:** before `bd close <epic>` (or `bd epic close-eligible`), you MUST
run `make reconcile` and it MUST exit 0. This composes `make docs-reality` (whole-
surface CLI-inventory check) and `make docs-check` (generated-docs drift check).
See `docs/plans/dogfood/docs-checklist.md` "Epic-Close Reconciliation" for the
full procedure including the one human holistic-read step.

**Important:** `make reconcile` is a workflow-required command, NOT a `bd`
pre-close hook. `bd` emits no mechanical pre-close event and has no plugin hook
at epic close. Enforcement is documentary discipline — a required MUST + a
pass/fail target — the same mechanism as all other workflow gates documented here.

**Recording:** record the epic-close gate result in the epic bead's notes as a
one-line gate note:

```text
Epic-close gate: make reconcile exit 0, <commit SHA> — closing.
```

This is consistent with the normal gate attempt note format (phase, result,
commit reference). No four-seat reviewer panel is required for the epic-close
gate (it is a mechanical pass/fail command, not a design/code review); the gate
passes on `make reconcile` exit 0 + the holistic README/index read.

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

### Structured capture (`etude capture-gate`)

The prose block above is the human-readable mirror; the **structured record is
the durable source of truth**. After each gate ATTEMPT concludes (one panel
re-examination of one phase at one round), also record it as a `GateAttempt` on
the bead's etude run:

1. Author a gate-attempt JSON (the orchestrator already holds every seat's
   verdict, feedback, provider, and model at gate time).
2. Run `scripts/dogfood-gate-capture.sh <bead-id> <gate.json>`. It builds etude
   fresh, fetches the run ref, appends via `etude capture-gate`, VERIFIES the
   local manifest (`manifest_version` 3 + the gate present) BEFORE pushing, then
   pushes `refs/etude/runs/<bead-id>`.

Each rerun is a NEW `GateAttempt` with `round` incremented (see "Reruns"). A
COMBINED gate (e.g. "Implement+Final") is modeled as a single `GateAttempt` whose
`phase`/`gate_id` names the dominant phase and whose `reviewed_stages` lists the
artifacts it actually reviewed (e.g. `implement` + `verify`) — a deliberate
modeling choice, not "the implement gate only".

Gate-file shape (snake_case; see `docs/plans/product/gate-reviewer-record-schema.md`
§4/§5 for the full schema + a worked example):

```jsonc
{
  "gate_id": "<phase>.r<round>",         // unique per run, e.g. "plan.r2"
  "phase": "plan|implement|verify|review|...",
  "round": 1,                            // 1-based; rerun => round+1, new attempt
  "tier": 1,                             // 0 unknown | 1 | 2 | 3
  "status": "pass|rerun|escalated",
  "reviewed_stages": [                   // >=1; stage must exist on the run
    { "stage": "implement", "role": "diff", "artifact": "<sha or omit>" }
  ],
  "seats": [ /* one per seat, see conventions below */ ],
  "decision": { "degraded_reason": "<why a disregarded seat was allowed>",
                "escalation_reason": "", "deferred_beads": [] },
  "timestamp": "<RFC3339Nano UTC>"
}
```

**Reviewer-seat conventions** (pin `harness`/`provider`/`model` exactly):

| seat   | harness.name | provider.name | provider.model            |
|--------|--------------|---------------|---------------------------|
| opus   | claude-code  | anthropic     | claude-opus-4-7           |
| gemini | gemini-cli   | google        | gemini-3.1-pro-preview    |
| codex  | codex        | openai        | gpt-5.5                   |
| pilms  | pi           | lmstudio      | qwen/qwen3.6-35b-a3b      |

**Verdict mapping** (per seat, covering every outcome):

| outcome                          | `verdict`     | required extra fields                         |
|----------------------------------|---------------|-----------------------------------------------|
| passed                           | `go`          | `optional` (if any)                           |
| blocked                          | `block`       | `required` (the changes); gate `status: rerun`|
| auth/quota/tool invocation failed| `failed`      | `failure_note`                                |
| ran but produced no verdict      | `empty`       | `failure_note`                                |
| root-caused tooling outage       | `malfunction` | `failure_note`                                |
| outage seat skipped (degraded)   | `disregarded` | `failure_note` AND `decision.degraded_reason` |

`failure_note` is REQUIRED for every non-`go`/`block` verdict
(`failed`/`empty`/`malfunction`/`disregarded`) and FORBIDDEN on `go`/`block` —
`capture-gate`'s validation enforces this. So a skipped pilms seat always carries
both `failure_note` (what broke) and `decision.degraded_reason` (why the gate
still passed under the Degraded Gate Policy).

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
