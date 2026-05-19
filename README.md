# etude

> Planned: empirically test AI coding-agent skills by replaying them against
> your real past work.

## Status

Current implementation: root CLI scaffold only.

Implemented:

- Go module and `etude` binary entrypoint.
- Root command help and version output.
- Local build, test, lint, and clean commands.

Not implemented yet:

- `etude init`
- `etude capture`
- `etude replay`
- `etude bench`
- refs/etude storage
- workflow schema validation
- generated command reference docs

The full design is in
[`docs/plans/product/BRIEF.md`](docs/plans/product/BRIEF.md). Planning notes
for components that do not exist yet live under [`docs/plans/`](docs/plans/).

## Build And Test

```bash
make build
make test
make lint
make clean
```

`make build` writes `bin/etude`.

## CLI

```bash
./bin/etude --help
./bin/etude --version
```

The root command currently exposes help and version output only. Unknown
subcommands such as `etude init`, `etude capture`, `etude replay`, and
`etude bench` exit non-zero with an unknown-command error.

## Planned Product Direction

Improving the skills that drive a coding agent — planning, implementation, test
design, code review, docs — is mostly guesswork. You edit a skill, it *feels*
better, and you ship it. There's no signal that an upgraded planning skill
actually produces better plans.

`etude` is intended to make skill improvement empirical. The planned product
will capture artifacts produced at each stage of a development workflow as
immutable, git-native records, then let you replay any stage with a different
skill version and evaluate whether the result improved.

- **Capture** — record each workflow stage's input and output as an immutable,
  content-addressed artifact under a `refs/etude/*` git namespace.
- **Replay** — re-run one stage of past work with a new skill version, against
  its original recorded inputs.
- **Bench** — replay and evaluate a stage across your last N real PRs, and
  report a win rate for the skill change.
