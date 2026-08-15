# Supervisor Runbook

Status: planning note. This describes how a bead is worked while `etude` is
still being built. It is not shipped user-facing behavior.

This runbook replaces the manual capture protocol. Under that protocol the agent
did the work, closed the bead, and a script then *reconstructed* five stage
artifacts from the bead's `design` field and `git show <sha>`. Recording was
reconstruction, so it was skippable — and skipped: of the run refs under
`refs/etude/runs/`, all but one are named after a bead id, captured after the
fact rather than produced by a run. A large completeness audit existed to police
that gap.

The replacement has two moving parts. A **worker lane** does the producing work.
A **supervisor** advances each phase boundary with one command that runs the
gate for real. The supervisor cannot record a gate it did not run, so the audit
shrinks to the checks that caught real misses. (As shipped in `etude-9uf.4` that
is three hard checks — run-ref present, run has gates, refs pushed — plus retro
cadence as a warning. The planning note said "two"; nothing forces `etude gate`
to be used, so the two checks that detect a supervisor skipping the model both
survived.)

This is deliberately not live `etude run`: the producing step stays outside
etude until caller-cwd runner mode lands (GH #15). What changes is that gating
stops being a manual reconstruction and becomes a command.

---

## The two roles

**Worker lane.** A claude worker on its own workmux worktree, running the
`etude-loop` skill, works one bead through `plan → implement → verify → docs →
review`. It produces each phase's artifact and nothing else: it does not run its
own gate, does not write verdicts, and does not decide whether a phase advances.

**Supervisor.** Holds the bead board and advances each phase boundary. It never
produces the artifact under review and never writes a verdict by hand. Its whole
job at a boundary is two commands: record the stage, then run the gate.

The split is what makes the record trustworthy. A single agent that produces an
artifact, judges it, and writes down the judgement is recording its own opinion.
Here the artifact comes from the worker, the verdict comes from the seats, and
the supervisor only carries them to the run ref.

---

## Where a phase artifact lands

`.etude/tmp/<bead>/<phase>.md`, in the worker's worktree.

That path is gitignored scratch on purpose. The durable record is the run ref:
`etude capture` content-addresses the artifact into `refs/etude/runs/<bead>`, so
the bytes that were gated are recoverable from git forever without a single
planning file landing in the repo tree. Phase artifacts are never committed.

The same rule covers gate prompts and raw reviewer output. They inline the full
artifact, so committing them duplicates content the run ref already holds.

---

## The per-phase loop

The worker writes `.etude/tmp/<bead>/<phase>.md`. Then the supervisor runs:

```bash
# 1. record the stage — creates the run ref on the first capture of the bead
etude capture <phase> --run <bead> \
    --output <role>=.etude/tmp/<bead>/<phase>.md \
    --workflow default --workflow-version default-v1 \
    --harness claude-code --model <model> --skill-id <the stage's skill>

# 2. run the gate for real
etude gate --run <bead> --stage <phase> --artifact .etude/tmp/<bead>/<phase>.md
```

`<role>` is the stage's `produces` value in `.etude/workflow.yaml` (`plan`,
`diff`, `verify`, `docs-diff`, `review`). `--skill-id` is that stage's `skill`.

**The role is not always the stage name. Two of the five differ, so do not
pattern-match — read the table.**

| stage | produced role | |
|---|---|---|
| `plan` | `plan` | same |
| `implement` | **`diff`** | **differs** |
| `verify` | `verify` | same |
| `docs` | **`docs-diff`** | **differs** |
| `review` | `review` | same |

A supervisor who infers the role from the three that match writes
`--output implement=…` or `--output docs=…`, no stage producing `diff` or
`docs-diff` exists, and the gate then refuses with "no reviewable stage on run".
`implement` is the easier one to get wrong, since `diff` shares no letters with
the stage name at all. Copy these rather than deriving them:

```bash
etude capture implement --run <bead> --output diff=.etude/tmp/<bead>/implement.md \
    --workflow default --workflow-version default-v1 \
    --harness claude-code --model <model> --skill-id dev-executor

etude capture docs --run <bead> --output docs-diff=.etude/tmp/<bead>/docs.md \
    --workflow default --workflow-version default-v1 \
    --harness claude-code --model <model> --skill-id dev-docs-writer
```

`etude gate` resolves the reviewed stage by the stage's produced ROLE, never by
its name — so a stage merely *named* `docs` that produces something else does not
satisfy the docs gate, and the gate writes nothing on that path.

**Run the gate from the WORKER'S worktree, not your own.** `etude gate` execs its
deterministic checks with the caller's working directory. Supervisor and worker
are in different worktrees, so running it from yours executes `make test` and
`make lint` against a tree that does not contain the change, passes both, and
certifies nothing. Every other gate defect announces itself with a BLOCK; this
one announces itself with a pass. Measured on `refs/etude/runs/etude-3xt`, the
first bead run this way: two of its five `etude gate` attempts were invoked from
the supervisor's worktree before it was noticed. Those two were `plan` and
`implement`; neither carries a `checks:` block, which is the only reason nothing
was falsely certified. (Only `verify` declares checks at all, so the exposure is
that one phase — which is also the phase whose entire value is running them.) Bead `etude-4qi` covers making the
resolved check directory part of the gate record, which is what would let anyone
audit this after the fact; today nothing records it.

```bash
cd <worker-worktree> && <supervisor>/bin/etude gate \
    --run <bead> --stage <phase> \
    --artifact .etude/tmp/<bead>/<phase>.md
```

(Precisely: the checks run in the caller's WORKTREE ROOT, not its literal cwd —
`etude gate` resolves the root and sets the command's directory from it. Running
from a subdirectory of the right worktree is fine; running from the wrong
worktree is not.)

**Exit 0** — the gate passed. Advance to the next phase.

**Non-zero** — the gate did not pass. The supervisor hands the printed
`required` items back to the worker verbatim, the worker redoes the phase, and
the supervisor captures the new artifact under a rerun stage name and gates
again:

```bash
etude capture <phase>.r2 --run <bead> --output <role>=... [...]
etude gate --run <bead> --stage <phase> --artifact ...
```

The rerun stage name matches the convention `etude run`'s own gate uses for a
re-driven stage. `etude gate` derives the round from the attempts already on the
run, so the second attempt records as round 2 without being told.

**Verify the ref after every capture.** `git show-ref refs/etude/runs/<bead>`
before moving on. The run ref is the whole durable record; a capture that
reported success but left no ref is worth catching at the boundary rather than
at close.

---

## `etude gate`

> `etude gate` is shipped (bead `etude-9uf.1`). This section was written first,
> as its specification, and was amended in that bead to describe what actually
> shipped. Where implementation taught us something the spec had wrong, the
> correction is recorded inline rather than quietly applied.

```
etude gate --run <id> --stage <name> --artifact <file>
```

### Contract (shipped in bead etude-9uf.1)

This section was written before the command existed, as its specification. It now
describes shipped behavior: bead `etude-9uf.1` implemented `etude gate` against
this list and its VERIFY phase reported against each item individually.

**A correction, recorded because the mechanism was overstated.** The version of
this section written in bead .2 claimed the `docs-reality` tripwire would force
this amendment once `gate` shipped. It would not have. Check 3 of
`scripts/docs-reality-check.sh` only fires when a stale-claim phrase and an
`etude <cmd>` mention appear on the **same line**, and this section's phrasing
never paired them — so the check stayed green with the text still calling a
shipped command unbuilt. What actually keeps this document honest is the
obligation on the implementing bead's verify phase to walk the list item by item
against the built binary. That is a discipline, not a tripwire, and it is worth
naming as such rather than trusting a check that would not have fired.

What the command does, in order:

1. **Resolve the stage.** Look up `<name>` in `.etude/workflow.yaml`. An unknown
   stage is a clear error and a non-zero exit, with nothing recorded.
2. **Read that stage's gate block** — `gate.tier`, `gate.abstraction`,
   `gate.checks`.
3. **Resolve the tier's seats** from `.etude/registry.yaml` (`tiers.<L>.seats`).
   The tier is read from config, never from a flag: there is no way to ask for a
   lighter panel than the stage's gate declares.
4. **Require the run ref to exist.** `refs/etude/runs/<id>` must already be
   there, created by the first `etude capture` for the bead. A missing run ref
   is a clear error and a non-zero exit. The gate does not create runs — a gate
   attaches to work that was recorded, and auto-creating the run would let a
   gate exist with nothing to review.
5. **Derive the round** from the attempts already recorded for that phase, so a
   re-gate after a worker fix is round N+1.
6. **Run the checks, then the seats,** both against the same shared prompt. A
   check is deterministic and its exit code is the verdict: 0 passes, anything
   else blocks.
7. **Synthesize one verdict** with the existing fail-closed algorithm: any
   failing check is a not-pass; fewer than `min(2, len(seats))` usable seat
   verdicts is `escalated`; otherwise the seats must clear the pass threshold,
   which defaults to 1.0 — unanimous, matching the registry's `quorum`.
8. **Append one `GateAttempt`** to the run ref with per-seat verdicts, raw
   output and session evidence.
9. **Print** the synthesized verdict and each blocking seat's `required` items.
10. **Exit 0 only on `pass`.** `rerun` and `escalated` exit non-zero, so a
    supervisor cannot advance past a failed gate by missing a line of output.

There is no rerun loop. `etude run`'s gate re-invokes the stage runner and
re-reviews its new output; a supervised lane has no runner to re-invoke, so a
not-pass returns to the supervisor, who returns it to the worker.

### The record must match the bootstrap path

Both `etude gate` and `etude capture-gate` write `GateAttempt` records to the
same run ref, so they must write the *same* record: same field set, same
`gate_id` convention (`<phase>.r<round>`), same verdict vocabulary, same
`failure_note` rules. `etude gate` should build the record through the same
construction path `capture-gate` uses rather than a parallel one, so the two
cannot drift and a run's history reads the same regardless of which path wrote
each entry.

---

## The seat contract

A seat is invoked as a subprocess with a deliberately small environment:
`PATH`, `ETUDE_INPUTS_DIR`, `ETUDE_OUTPUT_FILE`, plus any variable NAMES listed
in the workflow's `env_allowlist`. The shared prompt is materialized as a file
under `$ETUDE_INPUTS_DIR`. The seat MUST write a JSON verdict envelope to
`$ETUDE_OUTPUT_FILE`:

```json
{
  "verdict": "go",
  "required": [],
  "optional": [],
  "session": {"session_id": "", "transcript_path": ""}
}
```

Nothing else is read. A seat that writes prose to stdout and leaves
`$ETUDE_OUTPUT_FILE` untouched is recorded `empty`; a seat that writes non-JSON
is recorded `malfunction`. Neither is a pass.

`scripts/seat-adapter.sh` is the shipped bridge: `invoke` names it, it feeds the
prompt to the model CLI on stdin, parses the four-line RETURN block off stdout,
and writes the envelope. It fails closed — a non-zero CLI, a truncated reply with
no `VERDICT:` line, or an unrecognized verdict all leave `$ETUDE_OUTPUT_FILE`
ABSENT and exit non-zero, so the engine's own `empty`/`failed` classification is
what decides, never the adapter. The only path that writes `{"verdict":"go"}` is
a reply that literally said `VERDICT: GO`. `make seat-adapter-test` pins every
one of those paths.

**So a seat's registry `invoke` must name an adapter, not a bare model CLI.** A
bare `claude -p` or `codex exec` writes prose to stdout and satisfies none of
this. `examples/research/approve-seat.sh` is the minimal example of a conformant
seat; `scripts/seat-adapter.sh` is the real one.

A repo-relative adapter path is anchored to the repo root before the seats run,
because exec resolves a relative program path against the CALLER's cwd rather
than the child's directory — without that, a gate run from a subdirectory fails
every seat with a "file not found" that reads as an outage.

Changing an `invoke` to point at an adapter does not change the panel: the seat
identities, the tiers they belong to, and the quorum are the same. Only the
invocation becomes contract-conformant.

**Credentials.** The environment is hermetic, so a model CLI that reads its
credentials from the user's home directory sees nothing unless `env_allowlist`
carries `HOME`. `env_allowlist` holds NAMES only — values are resolved at run
time and are never written to the manifest, a log, or a gate record.

---

## The shared prompt

Every seat at a gate receives the identical prompt. Seats are model identities
voting on one prompt; they are never role personas with different briefs. That
is the whole basis of the redundancy — divergence is supposed to come from
model diversity, not from having asked two reviewers different questions.

The prompt carries the reviewer role, the phase, the decision standard, the
stage's `gate.abstraction` prose, the bead's acceptance criteria, and the
artifact inlined in full.

`gate.abstraction` is the altitude control: it is what stops a plan gate from
blocking on implementation minutiae, or an implement gate from re-litigating the
design. It is per-stage config, so it is the workflow that decides how each
phase is judged.

For what a good prompt contains and the failure modes of a bad one, see
[Review gate runbook — Gate Inputs](review-gate-runbook.md#gate-inputs) and
[Reviewer Prompt Template](review-gate-runbook.md#reviewer-prompt-template). The
short version: inline the whole artifact, never a summary; include the unchanged
code an invariant depends on; and never mechanically truncate the thing under
review.

---

## Degraded gates and stop-the-line

**A seat that cannot run is never a pass.** Not an implicit one, not a silent
one. A gate with too few usable verdicts escalates, and escalation exits
non-zero.

There is one documented seat exception, and it is live today. The `opus` seat's
registry invocation is `claude -p --model opus`, which fails to authenticate
when the orchestrator is itself Claude Code — the host session's credentials are
not exposed to a subprocess, and the CLI reports that it is not logged in. The
registry declares the workaround in the seat's `invocation_fallbacks`, marked
`in-harness:`: run that seat as a fresh sub-agent instead.

**That exception is encoded, and `etude gate` reads it.** Bead `etude-pqv` made
registry exceptions machine-honored and is CLOSED; `etude-5f6` landed the
fallback ladder in `17d0ab5`, and `runSeatLadder` walks it, skipping the
in-harness candidate with an explicit `IN_HARNESS_CANDIDATE_SKIPPED` note rather
than failing blind.

**What `etude gate` still cannot do is CONSUME an in-harness verdict.** Observed
end to end on `etude-9uf.5`'s `docs.r6`, with a binary built after the ladder
landed — the recorded `failure_note` is the whole story:

```
CANDIDATE_FAILED harness=claude-code invoke=…seat-adapter.sh opus claude -p --model opus verdict=failed;
IN_HARNESS_CANDIDATE_SKIPPED harness=claude-code-subagent invoke=in-harness:task …;
CANDIDATE_FAILED harness=agy invoke=…seat-adapter.sh opus agy --model opus --print
```

The ladder walks all three rungs. The primary fails to authenticate, the
in-harness rung is skipped by design (nothing can exec it), and the `agy` rung —
the one genuinely exec-able fallback — fails too, because its invocation is wrong
and that account's quota is exhausted (bead **`etude-8li`**). Only then is the
seat recorded `failed` and the gate escalated.

So **every gate escalates for a Claude Code supervisor today**, but not because
the ladder is unwalked: because its last exec-able rung is dead. Fix `etude-8li`
and the opus seat could be filled by Antigravity, recorded under provider
`anthropic/claude-opus` — at which point a supervisor MUST check which harness
actually filled the seat rather than assuming it was the sub-agent. The gap that
remains regardless is bead **`etude-4ed`** (consume an in-harness verdict), which
is OPEN.

**Rebuild `bin/etude` (`make build`) before you trust any of this.** A binary predating
`17d0ab5` does not walk the ladder at all and fails the seat flat, with a bare
adapter error and no `CANDIDATE_FAILED` markers. That is exactly what produced
the escalations measured earlier in this epic, and it was not noticed until a
seat checked the binary's build time against the commit.

So: a supervisor that is itself a Claude Code session runs the opus seat
out-of-band and records the completed panel via the bootstrap path below, writing
down which seat could not be invoked and why. **That is the standing procedure
today, not a temporary measure pending `etude-pqv`** — an earlier version of this
paragraph said "until it lands", conditioned on a bead that has since closed, so
the one instruction covering the manual append read as expired.

The bounded exception for *disregarding* a seat — the conditions, the reroll
bar, who authorizes it, and how it is recorded — is unchanged and lives in
[Review gate runbook — Degraded Gate Policy](review-gate-runbook.md#degraded-gate-policy).
Note that `etude gate` does not implement it: its synthesis escalates on
insufficient usable seats rather than passing on the remainder. Disregarding a
seat is an orchestrator judgement, so it is recorded through the bootstrap path
with `decision.degraded_reason` stating the evidence.

---

## The bootstrap path

```bash
scripts/dogfood-gate-capture.sh <bead-id> <gate.json>
```

This appends a hand-assembled `GateAttempt` to the run ref and pushes it. It
builds etude fresh, fetches the ref so the optimistic-concurrency check guards a
stale tip, verifies the local manifest before pushing, and only then pushes.

It survives this epic because it is the only path available in two cases:

1. before `etude gate` exists, and
2. when a documented seat exception makes a seat un-invokable as a subprocess,
   so the verdict is genuinely produced out-of-band.

Using it obliges the supervisor to write down, in the gate record, which seat
could not be invoked and why. A hand-assembled record with no such note is a
record of a gate nobody can check.

### Show a seat the evidence in the form it actually lives

A gate packet that inlines only a git diff can only prove things that live in
git. This repo's tracker is `bd` (beads/dolt), so a requirement satisfied by a
tracker note is structurally invisible to a diff-only packet — and a seat asked
to check that requirement will correctly BLOCK on its absence, even though the
work was done.

That is a packet bug, not a finding: rebuild the packet with the evidence
inlined and re-run the seat. The general rule is that every requirement a packet
asks a seat to adjudicate must appear IN the packet, whatever medium it lives in
— tracker state, a ref, a command's output, a file outside the diff.

### When a seat blocks twice on one invariant, audit every site

Two findings on this epic's own `etude gate` bead were the same shape: something
other than a stated GO could produce a `go`. The first was an agentic seat CLI
inheriting `$ETUDE_OUTPUT_FILE` and writing its own envelope. The second was
"last VERDICT line wins" turning a stated BLOCK into a pass. Fixing the second
at the site the seat named — `VERDICT` — left the identical bug one label over in
`BLOCKING`, which the next round caught.

The tell is a seat naming a NEW SITE for a finding you already fixed. When that
happens, stop patching the named site and enumerate every place the invariant
must hold, then fix them in one pass and re-gate once. For a guard, that means
listing every input the guard reads and every way it picks among several
candidates — a "last wins" or "first match" rule is unsafe wherever the earlier
candidates carry real content.

It is cheaper than it sounds: the exhaustive pass over the adapter's selection
primitives took one audit and closed a filed follow-up as a side effect.

### Append the verdict BEFORE you act on it

Every tier includes the `opus` seat, and `etude gate` cannot invoke it as a
subprocess (see the seat exception above — that is the condition when a Claude
Code session is the supervisor), so **every gate escalates and you must complete
the panel by hand** through the bootstrap path. Measured on
`refs/etude/runs/etude-3xt`: `etude gate` alone passed **0 of its 5** attempts.

That manual step is where records go missing. Across `etude-9uf.3` and `.4`, seat
verdicts were acted on and never appended — each caught only by a later seat
reading the manifest, never by tooling. It is easiest to skip precisely when the
verdict is interesting enough to act on immediately.

**Append the previous round's verdict BEFORE capturing the next revision.** Not
merely before acting on it — before the next capture. Otherwise the new
artifact's own account of the record excludes a verdict you already hold, and
every count in it is stale the moment it is frozen. This was the last
act-then-append instance found on `etude-9uf.5`, by 35 seconds.

**The record has a PARTIAL fingerprint for this.** A gap in a phase's round
sequence — `verify.r1, r3, r4…` with no `r2` — is an attempt whose verdict was
appended late under a different id, or never. There are exactly three such gaps
in this epic that ARE missing verdicts: `etude-9uf.3` verify `r2` and docs `r2`,
and `etude-9uf.4` verify `r2`. They are machine-detectable with a few lines of arithmetic over the
manifest.

**Partial in BOTH directions, which this epic's own record demonstrates.**

- *False negatives:* a verdict never given a round id leaves no gap. The
  arithmetic finds holes, not every missed verdict, so a clean check is not proof
  the record is complete.
- *False positives:* `etude-9uf.1`'s plan rounds run 1, 3, 4 — a hole by the same
  rule, but no verdict is missing. `nextPhaseRound` takes its maximum over STAGE
  names as well as gate rounds, so capturing a stage called `plan.r2` pushes the
  next gate to `r3` and leaves a hole behind.

  **Do not use "is there a stage of that name?" as the discriminator.** All four
  holes in this epic have a same-named stage — `.1` `plan.r2`, `.3` `verify.r2`
  and `docs.r2`, `.4` `verify.r2` — so that test dismisses the three real ones
  too. The separator that actually works is whether some later attempt reviews an
  OLD stage: `.3`'s `verify.r6`/`r7` and `docs.r4`, and `.4`'s `verify.r5`, were
  all appended after their phase had advanced and each reviews a much earlier
  revision. That is a late append. `.1` has no such attempt.

A sharper false negative than either: `etude-9uf.1` has **no docs or review gate
attempt at all**. A whole phase missing leaves no hole in any round sequence,
because there is no sequence.

Treat a hit as a question, not a verdict. Bead `etude-a1i` covers implementing
the check properly, including telling those cases apart. Until it lands:

```bash
git show refs/etude/runs/<bead>:manifest.json | python3 -c '
import json,sys,collections
g=json.load(sys.stdin)["gates"]; d=collections.defaultdict(list)
for a in g: ph,_,r=a["gate_id"].rpartition("."); d[ph].append(int(r[1:]))
for ph,rs in d.items():
    for n in range(1,max(rs)+1):
        if n not in rs: print("hole:",ph+".r%d"%n)'
```

Two more things the bootstrap path does that `etude gate` does not, both measured
on `etude-9uf.5` and both recorded on `etude-4ed`:

- a hand-assembled panel **re-records** the seats `etude gate` already ran, and
  the copies carry no `raw_output` and no `session` — so the duplicate is
  evidence-free, and a run can show more codex verdicts than codex invocations;
- the completed attempt **does not carry the deterministic check seats**, so on
  that run the only `pass`-marked verify attempt contained zero check evidence.
  Read the escalated attempt for that, not the passing one.

### Do not edit an artifact after its gate passes

Folding a seat's optionals into an already-gated artifact leaves the file on disk
different from the bytes any seat reviewed. The run ref preserves the reviewed
bytes, so the record stays honest — but a reader opening the worktree sees a
document nobody gated, with no signal that it diverged.

Measured on `refs/etude/runs/etude-3xt`: two of its five gated artifacts drifted
(`plan` and `verify`). It then happened again on `etude-9uf.5`, where two of the
three artifacts that had passed a gate no longer match what was gated. Either
fold before the gate, or capture the revision under a rerun stage name and gate
it again. Bead `etude-pq7` covers detecting the drift.

You can check it yourself today — this is how `etude-pq7` was confirmed:

```bash
shasum -a 256 .etude/tmp/<bead>/<phase>.md
git show refs/etude/runs/<bead>:manifest.json | grep -A8 '"stage": "<phase>"'
```

### If a run ref is lost

It has happened: a run ref disappeared mid-bead and the next `etude capture`
silently created a fresh manifest, discarding three recorded gate attempts
(tracked as `etude-nad`, which also covers capture's inability to tell "create"
from "append"). If it happens to you:

- Rebuild the run from the artifacts and seat verdicts still on disk, and say so
  in the bead. Re-recording an already-adjudicated verdict is honest as long as
  it is the ORIGINAL verdict and the reconstruction is disclosed; silently
  continuing on a truncated run is not.
- Push `refs/etude/runs/<bead>` immediately afterwards, so a second local loss
  cannot destroy it again.
- Once `etude-nad` has landed a fix, a recurrence is no longer an accident to
  absorb — re-run the affected gates rather than re-recording them.

For the `GateAttempt` field shape, the per-seat `harness`/`provider`/`model`
conventions, and the verdict-to-`failure_note` rules, see
[Review gate runbook — Recording Results](review-gate-runbook.md#recording-results)
and `docs/gates.md`.

---

## What the supervisor may not do

- Lower a tier, or drop a seat, to get past a BLOCK. The tier comes from
  `workflow.yaml` and its seats from `registry.yaml`; a bead that turns out to
  touch a heavier surface escalates upward, never down.
- Substitute its own judgement for a seat verdict.
- Record a gate it did not run.
- Advance a phase whose gate exited non-zero.

A BLOCK is information. The response is to fix the work.

---

## Session boot

1. `bd prime`, then `bd ready` — pick the next unblocked bead.
2. `etude run list` and `etude run show <bead>` — recover what shipped on recent
   beads and how it was reviewed, from the run refs rather than from `bd` alone.
3. Read this runbook, then
   [Review gate runbook](review-gate-runbook.md) for how to judge, and
   [Verify phase design](verify-phase-design.md) for the workflow shape.
4. Hand the bead to a worker lane and run the per-phase loop above.

## Close

1. Every phase gate for the bead exited 0.
2. `make test` and `make lint` green — these are the verify stage's
   `gate.checks` in `workflow.yaml`, so `etude gate` runs them at that boundary
   rather than leaving them to memory.
3. One bead, one commit, one short sentence. Stage paths explicitly.
4. `bd close <id> --reason "<what landed> <sha>"` — the reason is the durable
   record once the session is gone.
5. Push the branch, then push the run refs with `etude sync` (or
   `git push <remote> 'refs/etude/*:refs/etude/*'`, which is what sync runs).
   Neither `etude capture` nor `etude gate` pushes anything — both write only to
   the local refstore — so this is always a separate step. An unpushed run ref is
   lost when the worktree is removed, which is one of the two conditions the
   completeness check still enforces, and the pre-push hook blocks code pushes on
   exactly that gap.
6. A retro every three closed beads. Write the body, then run
   `etude retro capture cohort --file <body> --subject-run <the closed bead-runs>
   --trigger cadence-retro --meta-file <json>`: that writes the ref AND lands the
   markdown under `.etude/retros/`, printing the path to stage. Commit that path
   and push the ref with `etude sync`. Do not hand-write the markdown and skip
   the capture — that produces a retro the ref namespace cannot see, which is
   what bead `etude-3xt` was filed for. See `docs/retro.md` §"The landed
   markdown" and `retro-ledger.md` §"Retro cadence".
