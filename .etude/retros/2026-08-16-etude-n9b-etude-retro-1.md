# GH16 verification retrospective

## Retro summary

The first GH16 closeout implemented and unit-tested the checkout-read grant, but
its built-binary proof did not adversarially exercise the default output-only
path. The implementation work was otherwise well-scoped: schema, manifest,
prompt policy, immutable pinned checkout, and the GH14 limitation were explicit.

## What went wrong

- The proof showed the opted-in path but did not make the same seat attempt the
  same checkout read without the grant.
- The PR summarized a built-binary proof without preserving the commands and
  observed output needed to audit it.
- Automated tests and a cooperative prompt-policy test were treated as stronger
  evidence than an adversarial live seat at a capability boundary.

## Root causes

The existing Verify guidance requires a built binary, but it does not state that
an opt-in capability must be tested on both sides of the boundary with a consumer
that actively attempts the protected action. That omission allowed a one-sided
happy-path proof to satisfy the checklist. The PR template's preference for short
test summaries also obscured evidence that the issue explicitly needed preserved.

## What worked well

- The issue's opt-in/default split made the missing negative control easy to name.
- The pinned-marker design gives a falsifiable proof of which tree a seat read.
- The GH14 dependency was handled fail-closed and disclosed instead of claiming
  untested submodule coverage.

## Recommended changes

Add an opt-in capability-boundary rule to the dogfood Verify phase design: the
built binary must run one adversarial consumer with the capability enabled and
disabled, and the evidence must preserve real commands and output. This is a
high-leverage, low-risk update to
`docs/plans/dogfood/verify-phase-design.md`; it is applied with this retro.

## Highest-leverage next step

Enforce the new Verify rule in the GH16 manual QA by using one seat script that
attempts the same relative checkout read in both runs.
