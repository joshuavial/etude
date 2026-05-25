# Product Plans

These notes describe planned `etude` product behavior. They are not shipped
user-facing documentation.

- [Design brief](BRIEF.md) - current product direction and phased plan.
- [Retrospectives](retrospectives.md) - first-class retro artifacts, triggers,
  CLI shape, and manifest integration.
- [etude retro command](etude-retro-command.md) - concrete design for the
  `etude retro` CLI + `refs/etude/retros/*` storage, phased capture-first plan,
  triggers, and open-question resolutions (implementation plan for etude-14r).
- [Manifest schema v2](manifest-schema-v2.md) - deferred manifest-schema
  decisions (typed external refs, distinct unknown pointer size) and their
  revisit triggers.
- [Gate reviewer record schema](gate-reviewer-record-schema.md) - structured
  data model for review-gate attempts and reviewer-seat results (top-level
  `gates []`, six verdict states, provider axis, v3-iff-gates compat); the
  design for the etude-roadmap.2 epic.
