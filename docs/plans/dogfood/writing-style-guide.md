# Writing Style Guide

Status: planning note. This guide defines the writing expectations for dogfood
planning docs while `etude` is being built.

## Purpose

Planning docs should make decisions easy to review, replay, and turn into
future product behavior. They should be clear enough for external reviewers to
evaluate without relying on hidden chat context.

## Style Rules

- State whether the document is planning material or implemented behavior.
- Put the main decision near the top.
- Separate decisions from open questions.
- Prefer concrete workflow rules over vague intent.
- Use present tense for the proposed design, while making unimplemented status
  explicit.
- Name the artifact or bead that should own follow-up work.
- Keep examples executable or clearly marked as illustrative.
- Avoid documenting planned behavior as if it is shipped user-facing behavior.
- Prefer relative links to related planning docs.
- Avoid unexplained session-specific details; mark them as examples when they
  are included.

## Review Expectations

When Verify checks documentation changes, it should confirm:

- changed docs follow this guide
- product plans and dogfood process notes are in the right subdirectory
- links resolve
- planned behavior is not presented as shipped behavior
- reviewer-facing docs include enough context to be evaluated independently

Style issues should be treated like other Verify findings:

- minor wording fixes can be made before the gate
- unclear decisions, broken links, or shipped/planned confusion should block
  progress until fixed
