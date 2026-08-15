# Retro 3 — epic etude-9uf

Subject: `etude-9uf.3` (delete the retroactive capture path). One bead, but
**16 gate attempts** — 5 pass, 7 rerun, 4 escalated — which is more than the
previous six beads of this lane combined. Worth understanding why.

## Every single non-passing round was a false claim, not broken code

Not one gate round on this bead found a defect in the shipped code. Every
substantive BLOCK found a sentence that was not true:

| Round | The claim |
|---|---|
| plan r1 | two self-granted exemptions to the bead's own acceptance |
| plan r1 | a 689-line test file described as one suite; it is three |
| plan r2 | a verification grep anchored on `.sh` that structurally could not see a live wrong pointer naming `dogfood-close` |
| verify r1 | "every push since the edit has exercised the hook" — no push ever had |
| verify r3 | the corrected section still OPENED by asserting what it retracted three paragraphs later |
| docs r1 | an error message attributed to `etude gate`, which cannot emit it |
| docs r2 | "`etude capture` and `etude gate` push their refs" — neither pushes anything |
| review r1 | round counts invented rather than read from the manifest |
| review r2 | the corrected counts miscounted, in the paragraph recording the first miscount |

A bead whose whole purpose is deleting documentation that describes a mechanism
the repo no longer has spent nine rounds writing documentation that described
mechanisms the repo does not have.

## The lesson that generalizes: replacing a stale pointer is a code change

The sharpest finding, docs r2. The pre-push hook's comment read:

> This lets dogfood-capture.sh and dogfood-gate-capture.sh push their refs

That was **stale but TRUE** — both scripts ran `git push`. The sweep rewrote it
to name `etude capture` and `etude gate`. Neither pushes anything; the only
pusher is `etude sync`. A true sentence had been replaced with a false one, in
the file whose entire job is telling a blocked operator what to do next.

It was also operationally harmful in a way pure prose never is: the audit that
hook fronts GAPs on `unpushed-ref`, so an operator following the new text would
capture, gate, skip the push, and stay blocked — by the very hook that had just
told them what to do.

**Durable rule:** a replacement pointer is a factual claim about the code and
must be verified against the source, exactly like a code change. The sweep was
rigorous about FINDING every stale pointer and careless about whether each
replacement was true. Finding them is the easy half.

## The gate found the epic's own defect, in this bead, five times

Five real in-harness verdicts were acted on but never recorded: `verify.r6`,
`verify.r7`, `docs.r4`, `review.r1`, `review.r2` (their late gate_ids). One of
them was the docs r2 BLOCK above — the bead's most valuable finding, living only
in a transcript. Another was the review r1 BLOCK that *discovered* the first
three; it went unrecorded itself, and round 2 caught that.

**This is structural, not carelessness.** The `opus` seat cannot be invoked as a
subprocess, so `etude gate` never produces an attempt for it (all 4 `escalated`
rounds are exactly this, fail-closed and correct). The verdict comes back
in-harness, and appending it is a separate manual step — easiest to skip
precisely when the verdict is interesting enough to act on immediately.

Nothing mechanical caught any of it. `dogfood-completeness-audit.sh` does not
detect this class of gap, and bead `.4` shrinks that audit further. A seat caught
it, twice.

The real fix is that a verdict acted on but not appended should not be possible —
which means `etude gate` must be able to consume an in-harness seat's verdict.
That is bead `etude-4ed`, still open, and this bead is the strongest evidence yet
for it. `etude-8li` (the dead `agy` fallback) is the same root cause one level
down.

## Two checks that looked rigorous and were not

- **A negative control that could not fail.** The plan specified breaking the
  hook's `refs/etude/*` exemption to prove the suite was wired to the real hook.
  Run as specified, 18/18 still passed — not a wiring failure, a badly chosen
  control: with that arm removed the ref falls to `*)`, which sets
  `_all_exempt=false` but leaves `_has_code_ref` false, and the next guard exits
  0 anyway. The breakage was masked downstream. Breaking `refs/heads/*`
  classification instead fails 5 of 18. **A control has to target a branch whose
  breakage is observable in the assertion you are watching.**
- **"Verified pre-existing by stashing."** `make dogfood-audit` scans a moving
  `--last 9` window, so its output is a function of when it runs. A stash-and-
  compare against it is not reproducible evidence, and two successive versions of
  that claim went stale before a seat could check them. The fix was to stop
  quoting a number at all.

## When being careful twice fails, stop being careful

Final review r1 blocked on invented round counts. I corrected them by hand. r2
blocked because the corrected counts were also wrong — inside the paragraph
describing the first miscount.

The fix that held was mechanical: every status count in the artifact is now
generated by reading `manifest.json`. Resolving to be more careful had already
failed once; doing it again was not a plan.

**Rule: after the second failure of the same manual step, replace the step, not
the intention.**

## What went right

- **The one thing the bead almost got wrong, it got right.** The bead said delete
  a 689-line test file. Reading it showed three suites, two of which test a hook
  that survives. A line-count-driven deletion would have passed `make test` and
  `make lint` cleanly while removing the only coverage of a live push gate. 11 +
  18 = 29 parity is what proved nothing was dropped.
- **Fixing the invariant, not the site.** After plan r2 named one missed sweep
  site, the response was a whole-repo inventory of all 26 hits in 14 files, split
  into edit sites and an enumerated no-op baseline. That found the
  `review-gate-runbook.md` section was stale on two further counts nobody had
  reported — it was still telling readers `docs` is not a captured stage and to
  alias the docs gate onto the implement diff stage, which `etude-1od` had
  removed two beads earlier.
- **Four negative controls**, each proving something different, including one
  that proved a carried dependency (the bd PATH shim) was load-bearing by
  replacing `bd` with a hostile stub: 13 of 18 fail.

## Cost

Sixteen attempts is a lot for a deletion. But the alternative was not "the same
bead, faster" — it was shipping a live runbook that instructs readers to use a
mechanism the epic removed, a push gate whose message misdirects the operator it
blocks, and a test file two-thirds deleted with the surviving third gone silently.
Each of those would have been found later, by someone with less context.
