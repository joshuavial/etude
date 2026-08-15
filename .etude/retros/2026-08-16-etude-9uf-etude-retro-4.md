# Retro 4 — epic etude-9uf

Subjects: `etude-9uf.4` (shrink the completeness audit) and `etude-9uf.5` (the
first supervised worker-lane proof run, carrying `etude-3xt` to `origin/main`).

## The proof run worked, and the number that matters is zero

`etude-3xt` was worked by a real `workmux` lane and gated by a supervisor in
another worktree. It landed as `752be1a`. Every phase carries a gate attempt;
no stage artifact is ungated; the worker closed its own bead and filed its own
follow-ons.

Measured from the run ref:

| | |
|---|---|
| attempts | 10, exactly 2 per phase |
| produced by `etude gate` alone | 5 — **all escalated** |
| completed by a bootstrap append | 5 — **all passed** |
| **gates `etude gate` passed on its own** | **0 of 5** |
| seat BLOCK verdicts | 0 |

Every escalation has the same cause: the `opus` seat cannot be invoked as a
subprocess. The machinery is not failing — checks ran, codex ran, synthesis was
correct, fail-closed behaviour was right every time. It simply **cannot complete
a panel**, so a human-equivalent step is mandatory five times per bead.

That is the evidence `etude-4ed` has been missing. Not an argument that verdict
injection would be nice: a count showing the command cannot pass a single gate
without one.

## The defect that would have passed silently

`etude gate` runs its deterministic checks **in the caller's working directory**.

Supervisor and worker are in different worktrees. When the verify gate ran
(`verify.r1`, supervisor at `1b837a1`) the supervisor's worktree did not contain
the worker's change at all — `grep -c retro_file internal/cli/retro.go` returned
0 there. Running `etude gate --stage verify` from the supervisor's own cwd — the
obvious thing, since that is where the supervisor is — would have run `make test`
and `make lint` against a tree without the change, passed both, and certified
nothing.

(That evidence is pinned to a commit deliberately. The supervisor has since
rebased past `752be1a`, so the grep returns 6 today and the un-pinned version of
this sentence reads as false. A seat blocked the implement artifact for exactly
that, which is a small illustration of the epic's own thesis: a claim about a
moment, written in the present tense, becomes a lie the moment the tree moves.)

**Every other gate defect this epic found announced itself with a BLOCK. This one
announces itself with a pass.** Filed `etude-4qi` (P1). The durable half is not a
flag but recording the resolved check directory in the attempt: confirmed at the
gate that no field anywhere in a gate record captures it, so a gate attempt
cannot be audited for this at all.

## Six unrecorded verdicts, and the three lines that would have caught them

Across `.3` and `.4`, six real seat verdicts were acted on and never appended.
I wrote retro 3 about this after `.3` and then reproduced it twice in `.4`.

The cause is structural: the verdict arrives in-harness, appending it is a
separate manual step, and it is easiest to skip exactly when the finding is
interesting enough to act on immediately. Each was caught only by a later seat
reading the manifest.

**The recount at `.5`'s implement gate found the thing I had missed for four
beads.** Round numbering carries a visible fingerprint: `.3`'s verify rounds run
1,3,4,5,6,7 and its docs rounds 1,3,4; `.4`'s verify rounds run 1,3,4,…,11. Every
gap is a verdict appended later under a different id, or never.

**A gap in a phase's round sequence is machine-detectable.** Three lines of
arithmetic over the manifest would have caught, at the moment it happened, a
defect that instead needed six separate seats to notice. That is worth more than
any of the individual fixes, and it is the single most useful thing this epic
produced about its own process.

## Shrinking a safety check is when fail-opens get written

`.4` replaced 2481 lines with 723. Its gates found **eight fail-opens**, the
sharpest being a run ref with no readable `manifest.json` exiting **0, clean**, on
a hard check — a `pipefail` interaction where an `|| echo -1` fallback appended a
second line to a sentinel already printed. The 902-line predecessor handled that
case correctly, so it was a regression the shrink introduced, and the new test
had no coverage for it at all.

Three more holes were found not by reading tests but by **mutating the script**:
deleting `refs/etude/retros` from check (d)'s sweep left the suite fully green,
so half a hard check was silently removable.

**Rule: when a bead's job is to shrink a check, the review must mutate the
survivor, not read it.** A test suite that has never been seen to fail is a
guess, and the smaller the check the easier it is to believe it is obviously
correct.

## Correcting docs is wrong in both directions

`.4`'s docs gate blocked three times, twice on errors the sweep itself
introduced. The obvious failure is prose asserting a guarantee that was deleted.
The subtle one is **past-tensing a behaviour that survives** — I wrote that the
audit's superseded-set was "the pattern used before `etude-9uf.4` shrank it" when
the shrunk audit still builds it, twenty-two lines above a comment I had written
saying it still does.

The second is harder to catch because the sentence looks appropriately humble.

Also: "this file is historical" is a judgement about a FILE, and a file is not
uniformly one thing. `retro-ledger.md` is mostly dated history with a
prescriptive convention block in the middle, and that block survived the first
sweep asserting a deleted check still enforced the convention.

**Nothing mechanical detects either direction.** `make docs-check` and
`make docs-reality` were green throughout every round and neither reads prose —
nor builds the site, which is how a nav entry pointing at a file `.2` deleted
survived four docs gates (`etude-fn8`).

## When being careful twice fails, replace the step

`.4`'s final review blocked on invented counts. I corrected them by hand. It
blocked again because the corrected counts were also wrong — inside the paragraph
describing the first miscount. The fix that held was generating every count from
the manifest.

Then `.5`'s implement artifact said `.3` and `.4` took "16 and 20" attempts; the
manifest says 16 and 22. In the artifact whose subject is measurement discipline.

**After the second failure of the same manual step, replace the step, not the
intention.** I wrote that rule in retro 3 and then broke it twice more. Writing a
rule down is not the same as building the thing that enforces it — which is, one
level up, this entire epic's thesis.

## What the worker did better than the supervisor

The worker took **zero BLOCK rounds across five phases**. It is n=1 on a bead
selected for tractability, so it is not a result about the model. But two of its
habits are worth copying:

- it **measured** that its regressions fail without the change, by scripted
  neutering edits, twice — where I have repeatedly asserted equivalents;
- it **inverted the bead's suggested fix** and argued for the inversion, rather
  than implementing what it was told. Both plan seats returned GO on the inverted plan.

The supervisor's job turned out to be almost entirely mechanical: capture, gate,
append, relay findings verbatim. The one judgement call that mattered — running
the gate from the lane's worktree — was the one the tooling gave no help with.
