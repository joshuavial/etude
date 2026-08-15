# Retro 1 — epic etude-9uf (supervised worker lanes + live `etude gate`)

Subjects: `etude-rep`, `etude-9uf.2`, `etude-9uf.1` (implement complete, gate blocked).
Trigger: cadence (3 beads worked) plus a P0 blocker that stopped the lane.

## What the gates actually caught

Every BLOCK this epic took was a real defect in my own work. None was a seat
being wrong, and none was argued down.

| Gate | Finding | Class |
|---|---|---|
| .2 plan r1 | invented a scope exemption the acceptance did not grant | reading the acceptance loosely |
| .2 plan r2 | fixed the body, left the contradicting sentence in Verification | half-applied fix |
| .2 verify r1 | proved the negative half of a criterion, asserted the positive half | evidence adequacy |
| .1 impl r1 | adapter accepted `PASS`/`PASS_WITH_FOLLOWUPS` as `go` | contract breadth |
| .1 impl r2 | claimed "literal GO" while case-folding first | overclaim vs code |
| .1 impl r3 | agentic child CLI inherited `ETUDE_OUTPUT_FILE` and could fabricate a pass | **fail-open** |

The last one is the one that mattered. Every seat behind this gate is an agentic
CLI with file-write tools. Had it shipped, a reviewer could have written its own
`{"verdict":"go"}` without ever stating a verdict — the gate would have read a
fabricated pass. It was found only because the packet asked, adversarially,
"construct a reply that is not a GO but still produces a go envelope".

**Durable change:** when a script's guarantee is "only I may produce X", the gate
packet must ask a seat to *attack* that guarantee, not confirm it. Applied here;
worth applying to every guard-shaped artifact.

## Live proof beats green tests

Ten green Go tests missed that the adapter emitted no session evidence, because
they stub the seat runner. One real invocation surfaced it instantly: a perfectly
good codex `go` was being downgraded to `malfunction` and discarded.

**Durable change:** already in the runbook as the item-by-item verify obligation.
This epic is the evidence for it.

## Where I was wrong, and it stood in a doc

Bead .2 claimed `docs-reality` would force the runbook to be amended once `gate`
shipped. It would not have — check 3 needs the stale phrase and the command name
on the *same line*, and my wording never paired them. Shipping the command and
grepping proved it. The claim is now corrected in the runbook itself rather than
quietly dropped, and the real mechanism (a discipline, not a tripwire) is named.

**Durable change:** a claim that a check protects something must be verified by
making the check fire, not by reasoning that it would.

## Theme cap worked

Three consecutive BLOCKs on one file was the signal to stop patching sites. The
r3 finding was classified as invariant-completeness (same invariant, different
site each round), so every site was audited in one pass instead of taking a
fourth round on the next one. Round 4 passed.

## The seat I declared dead was a spawn-shape bug

I reported the in-harness opus seat unreachable after six attempts and filed a P0
blocker. The seat was fine. Every one of those spawns passed a NAME, and a named
agent becomes a persistent addressable teammate: it runs, goes idle, and waits to
be messaged rather than returning a report. Unnamed spawns return normally.

Two prompt shapes, six silences, one conclusion — and the variable I kept
changing (the prompt) was never the one that mattered. The tell was there: six
IDENTICAL failures across genuinely different inputs is not a flaky seat, it is a
constant. A flaky seat varies.

Cost: an unnecessary P0 escalation, a burned `agy` quota, and a stop that was not
warranted. What it bought, eventually, was the epic's best finding — because once
the seat actually ran it immediately caught a fail-open nothing else had.

**Durable change:** when a seat returns nothing, check the INVOCATION SHAPE
before recording an outage. Identical failures across different inputs indict the
mechanism, not the content.

## The finding that justifies the whole gate

The opus seat's first real verdict was a BLOCK, empirically reproduced: the seat
adapter's "last VERDICT line wins" rule applied even when an earlier line-anchored
VERDICT held the opposite valid token. A reply that stated BLOCK and later said
"if fixed I would say: VERDICT: GO" was recorded as a passing vote carrying the
BLOCK's own findings — and synthesis discards `required`, so the gate would have
passed on a stated block.

Three things make this worth writing down:

1. **It was invisible to 30 passing tests**, because every fixture stated one
   verdict. The hole was in how two verdicts interact.
2. **It is the same class as the round-3 fail-open** (the agentic child inheriting
   `ETUDE_OUTPUT_FILE`). Twice now, the defect was "something other than a stated
   GO can produce a go". That invariant deserves a standing adversarial question
   in any guard-shaped packet, not a per-round rediscovery.
3. **Convergence made it certain.** A second opus instance independently named the
   same attack as its best one. Two independent seats reaching the same specific
   attack is much stronger evidence than one seat's confidence.

## The blocker

`etude-zc3` (P0): the opus seat is unreachable by every documented path —
`claude -p` cannot authenticate under a Claude Code orchestrator, the in-harness
sub-agent fallback returned nothing across five attempts, and the `agy`
substitute hit an individual quota that resets in about seven days. L1/L2/L3 all
require opus and L4 *is* opus, so no gate in this repo can legitimately pass.

The lane stopped rather than dropping a seat. Dropping it would have made every
subsequent gate record false, which is the precise failure this epic exists to
end.

**Process note:** the substitute worked for eight gates and then ran out. A
substitute with a consumable quota is a single point of failure for the whole
gating model, and nothing warned before it ran dry.

**Sharper finding:** the registry comment on the opus seat names the in-harness
sub-agent as *the* workaround for the `claude -p` auth failure. Six attempts —
two by file path, three with the packet fully inlined and tools forbidden, one a
direct mailbox request after the agent reported idle — produced no verdict in
every case. So the documented workaround does not work from this harness, and a
reader of that comment will believe they have an opus seat when they do not. Two
independent failures (a prose-only exception, and a workaround that silently does
not function) compound into "the panel looks configured and cannot convene".

That is the same shape as the defect this whole epic attacks: a protocol that is
written down, believed, and not actually executed. It deserves fixing in code,
not in a comment.

## Data loss, unexplained

`etude-nad` (P1): run refs vanished twice mid-session, and `etude capture`
silently created a fresh run when the ref was missing — destroying three recorded
gate attempts on this epic's own bead. Not reproducible: a sentinel ref survived
the full test suite, lint, docs checks, the research walkthrough, `git stash`,
and `bd`. Mitigation adopted immediately: verify the ref after every capture and
push it to origin after every append.

**Durable change:** already written into the runbook as "If a run ref is lost".
