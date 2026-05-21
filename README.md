# etude

## Status

Current implementation: root CLI scaffold plus an internal Git storage package.

Implemented:

- Go module and `etude` binary entrypoint.
- Root command help and version output.
- Internal `refs/etude/*` Git storage package for run and eval refs.
- Local build, test, lint, and clean commands.

The storage package is an internal Go API and is not exposed through CLI
commands.

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

The root command currently exposes help and version output only.
