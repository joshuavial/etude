# Cadence retro 3 — executable contracts and complete schema surfaces

Subjects: `etude-9uf.2`, `etude-pqv`, `etude-zdl`.
Trigger: scheduled after three closed beads.

## Retro summary

The cohort replaced an obsolete capture protocol with the supervisor runbook,
made reviewer invocation fallbacks machine-readable, and hardened schema
validation and serialization. The strongest shared lesson is that operational
contracts must be executable and every representation of a schema field must
be enumerated.

## What went wrong

- Reviewer harness exceptions lived in comments, so runtime consumers could
  believe a seat was available without having an executable fallback.
- Generated defaults and checked-in registry behavior had drifted.
- Mode values were open-ended, YAML node-encode errors could be discarded, and
  the identifier predicate had diverging copies.
- The old dogfood protocol described manual capture mechanics that no longer
  matched the live supervisor workflow.

## Root causes

- Prose was carrying behavior that belonged in typed configuration and a
  consumer contract.
- New schema concepts were not initially traced through validation, defaults,
  serialization, fixtures, and documentation as one completeness set.
- Process documentation accumulated alongside implementation instead of being
  retired when the executable workflow superseded it.

## What worked well

- `etude-pqv` separated model fallbacks from full invocation fallbacks and made
  regeneration preserve canonical settings.
- `etude-zdl` closed validation and propagation gaps while centralizing the
  shared identifier rule.
- `etude-9uf.2` consolidated the operating procedure and removed obsolete
  protocol documents instead of maintaining two authorities.

## Recommended changes and dispositions

- Represent runtime exceptions as typed, enumerable data and make the consumer
  record the chosen harness. **Applied:** `etude-pqv`; consumption is already
  tracked by `etude-9uf.1`.
- Require schema plans to enumerate validation, defaults, serialization,
  traversal, display, tests, and docs. **Applied:** the strict plan template and
  `etude-zdl` both encode this practice.
- Keep one operational authority and retire replaced procedures. **Applied:**
  `etude-9uf.2` made the supervisor runbook authoritative.

## Highest-leverage next step

Finish `etude-9uf.1` so the machine-readable reviewer invocation contract is
consumed by the same live CLI that records its selected session provenance.
