# Docs Freshness Checklist

Status: dogfood process note. This checklist keeps shipped docs accurate while
most `etude` behavior is still planned.

Use this checklist during the Docs phase and Final Review for every bead.

## Check Shipped Docs

- Review the top-level `README.md`.
- Review `docs/README.md`.
- If a user-facing command changed, review any command guide or generated
  command reference that already exists.
- If only internal Go APIs changed, keep shipped docs limited to current
  implementation status unless there is a real user-facing workflow to explain.

## Policy Checks

- Shipped docs describe implemented and verified behavior only.
- Planned behavior, future command shapes, design sketches, and open decisions
  remain under `docs/plans/`.
- Shipped docs do not imply CLI support for internal APIs.
- Links from shipped docs to planning docs are explicit that plans are not
  shipped behavior.

## Common Outcomes

- **No shipped-doc change needed**: record the rationale in the Docs artifact.
- **README status update needed**: update the current implementation summary
  without adding usage docs for unexposed internals.
- **Command docs needed**: update docs only after the command works, and keep
  generated command references separate from hand-written guides.
- **Planning docs needed**: add or update files under `docs/plans/`.

## Final Review Prompt

Final Review should ask:

- Does `docs/README.md` still explain why user-facing docs are sparse?
- Does `README.md` match the final diff?
- Did any user-facing behavior ship without documentation?
- Did any planned behavior leak into shipped docs?
