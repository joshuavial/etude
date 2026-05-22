# etude

## Status

Current implementation: manual capture CLI plus internal storage and manifest
packages.

Implemented:

- Go module and `etude` binary entrypoint.
- Root command help and version output.
- Manual `etude capture` command for local file artifacts.
- Internal `refs/etude/*` Git storage package for run and eval refs.
- Internal content-addressed artifact storage package for run-tree files,
  external pointer records, and manifest-ready metadata.
- Internal run manifest package for deterministic `manifest.json` records and
  validated writes to run refs.
- Local build, test, lint, and clean commands.

The storage and manifest packages are Go APIs internal to this module. The
implemented CLI surface is limited to manual capture, help, and version output.

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
./bin/etude capture plan --run run-1 --output output=plan.md
```

See [Manual Capture](docs/capture.md) for the capture command.
