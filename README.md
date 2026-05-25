# etude

## Status

Current implementation: `etude init`, manual capture, run inspection, sync, and replay CLIs
plus internal storage, manifest, and workflow-schema packages.

Implemented:

- Go module and `etude` binary entrypoint.
- Root command help and version output.
- `etude init` command to scaffold `.etude/workflow.yaml`, rubric placeholders,
  and configure `refs/etude/*` fetch/push refspecs on a git remote.
- Manual `etude capture` command for local file artifacts.
- `etude run list` to list all stored runs.
- `etude run show <run-id>` to inspect the detail of one run.
- `etude sync` to push and fetch `refs/etude/*` with a git remote.
- `etude replay <run-id> <stage>` to re-execute a recorded stage end-to-end and emit its output.
- Internal `refs/etude/*` Git storage package for run and eval refs.
- Internal content-addressed artifact storage package for run-tree files,
  external pointer records, and manifest-ready metadata.
- Internal run manifest package for deterministic `manifest.json` records and
  validated writes to run refs.
- Internal workflow schema package for parsing and validating `.etude/workflow.yaml`.
- Local build, test, lint, and clean commands.

The storage, manifest, and workflow-schema packages are Go APIs internal to
this module. The implemented CLI surface is `etude init`, `etude capture`,
`etude run list`, `etude run show`, `etude sync`, `etude replay`, and the root
help and version output.

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
./bin/etude init
./bin/etude init --force
./bin/etude init --remote upstream
./bin/etude capture plan --run run-1 --output output=plan.md
./bin/etude run list
./bin/etude run show run-1
./bin/etude sync
./bin/etude sync --remote upstream
./bin/etude replay run-1 plan --runner ./run.sh
./bin/etude replay run-1 plan --runner ./run.sh --output result.md
```

See [Init](docs/init.md) for the init command.
See [Manual Capture](docs/capture.md) for the capture command.
See [Runs](docs/run.md) for the run list and run show commands.
See [Sync](docs/sync.md) for the sync command.
See [Replay](docs/replay.md) for the replay command.

For a no-tracker walkthrough (no beads, no LLM, just git + sh + etude), see
[examples/summarize/README.md](examples/summarize/README.md).
