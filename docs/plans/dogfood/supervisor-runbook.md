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
registry records the workaround in a comment: run that seat as an in-harness
sub-agent instead.

That exception lives in prose, not in code, so `etude gate` cannot honour it. It
resolves the `invoke` string verbatim, the seat fails, and the gate escalates —
which is the correct fail-closed behavior for a machine that has been handed a
rule it cannot read. Bead `etude-pqv` covers making registry exceptions
machine-honored. Until it lands, a supervisor that is itself a Claude Code
session runs the opus seat out-of-band and records it via the bootstrap path
below, writing down which seat could not be invoked and why.

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
