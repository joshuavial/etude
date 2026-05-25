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

## Check Planning Docs On Close

Shipped-doc checks above keep `README`/`docs/README` accurate. They do not keep
`docs/plans/` notes fresh, which is how planning docs silently go stale. When a
bead closes, also reconcile planning notes that referenced the work it just
landed:

- Search `docs/plans/` for the bead ID and for status language that the close
  invalidates: "next bead", "until X works", "recommended sequence", "current
  bias", "completed foundations", "not yet".
- Move the just-closed work out of any "next" or "pending" list and into the
  completed/foundations section of the relevant note.
- If a planning note's whole premise is now satisfied, mark it superseded or
  delete the stale section rather than leaving misleading guidance.

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

## Mechanical reality check

Run `make docs-reality` (the source-built CLI-inventory gate the docs-reality
retro found missing). It builds etude fresh and fails, listing every finding,
when a shipped command is not advertised in `README.md` (as `etude <cmd>` usage)
or named in the `docs/README.md` index, when a shipped command lacks its
`docs/cli/etude_<cmd>.md` page, when a doc names a command that does not exist,
or when a `docs/plans/**` line still calls a SHIPPED command future/unimplemented.
Resolve every finding before close; for genuinely-planned prose (about unshipped
behavior) add a justified suppression to `scripts/docs-reality-allow.txt`. This
is separate from `make docs-check` (which only diffs generated `docs/cli`).

## Final Review Prompt

Final Review should ask:

- Does `make docs-reality` pass (no doc/CLI drift)?
- Does `docs/README.md` still explain why user-facing docs are sparse?
- Does `README.md` match the final diff?
- Did any user-facing behavior ship without documentation?
- Did any planned behavior leak into shipped docs?
- Did closing this bead make any planning note stale (next-bead lists,
  "until X works", recommended-sequence or status sections)?
