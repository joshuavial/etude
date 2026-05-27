# etude

## Status

Current implementation: `etude init`, manual capture (`etude capture` and
`etude capture-gate`), run inspection, sync, replay, bench, gc, and reindex CLIs
plus internal storage, manifest, workflow-schema, replay, eval, bench, gc, and
index packages.

Implemented:

- Go module and `etude` binary entrypoint.
- Root command help and version output.
- `etude init` command to scaffold `.etude/workflow.yaml`, rubric placeholders,
  and configure `refs/etude/*` fetch/push refspecs on a git remote.
- Manual `etude capture` command for local file artifacts.
- `etude capture-gate` to append structured review-gate reviewer records to a run.
- `etude capture-run` to capture a complete multi-stage run from a single YAML spec in one operation.
- `etude run list` to list all stored runs.
- `etude run show <run-id>` to inspect the detail of one run (including gates).
- `etude sync` to push and fetch `refs/etude/*` with a git remote.
- `etude replay <run-id> <stage>` to re-execute a recorded stage end-to-end and emit its output.
- `etude bench <stage>` to replay a cohort and report replay-vs-original win rates.
- `etude gc` to report artifact storage and explicitly prune named run refs.
- `etude reindex` to rebuild the derived SQLite query index from run and eval refs.
- `etude retro capture <scope>` to store an externally-authored retro as a `refs/etude/retros/*` ref.
- `etude retro generate <scope>` to produce a retro via an external generator over the subject runs' artifacts.
- `etude retro list` and `etude retro show <retro-id>` to list and inspect stored retros.
- `etude prime` to print a structured agent-onboarding primer to stdout (runs anywhere, no args, no side effects).
- `etude log` to narrate runs and retros as a chronological timeline (read-only).
- Internal `refs/etude/*` Git storage package for run and eval refs.
- Internal content-addressed artifact storage package for run-tree files,
  external pointer records, and manifest-ready metadata.
- Internal run manifest package for deterministic `manifest.json` records and
  validated writes to run refs.
- Internal workflow schema package for parsing and validating `.etude/workflow.yaml`.
- Local build, test, lint, and clean commands.

The storage, manifest, workflow-schema, replay, eval, bench, gc, and index
packages are Go APIs internal to this module. The implemented CLI surface is
`etude init`, `etude capture`, `etude capture-gate`, `etude capture-run`,
`etude run list`, `etude run show`, `etude sync`, `etude replay`, `etude bench`,
`etude gc`, `etude reindex`, `etude retro capture`, `etude retro generate`,
`etude retro list`, `etude retro show`, `etude prime`, `etude log`,
and the root help and version output. (The `eval` package is a library used by `etude bench`; there is no standalone `etude eval` cobra command.)

The design rationale is in [`docs/plans/product/BRIEF.md`](docs/plans/product/BRIEF.md). For the current v1 scope and what is deferred, see [`docs/plans/product/V1-SCOPE.md`](docs/plans/product/V1-SCOPE.md).

## Still planned / not yet built

v1 ships the CLI surface listed above. The following are confirmed deferred to post-v1 and tracked in [`docs/plans/product/V1-SCOPE.md`](docs/plans/product/V1-SCOPE.md): live xenota capture adapter, `etude import` backfill, standalone `etude eval` command, a `query` command over the SQLite index, external artifact pointer capture via the CLI, and a browsable docs site.

## Build And Test

```bash
make build
make test
make lint
make clean
```

`make build` writes `bin/etude`.

## Releasing

The `etude` binary reports its version via `etude --version`. The version is
read from the package-level variable `var version = "dev"` in
`internal/cli/root.go` (line 11), which is wired to the cobra root command's
`Version` field (line 24) and printed via the template
`"{{.Name}} {{.Version}}\n"` (line 35).

The `Makefile` sets `VERSION ?= dev` (line 3) and stamps the binary at build
time using ldflags (lines 8–10):

```
go build -ldflags "-X github.com/joshuavial/etude/internal/cli.version=$(VERSION)" \
  -o bin/etude ./cmd/etude
```

A plain `make build` (or `go build`) therefore produces a binary that reports
`etude dev`. A release build stamps the correct version:

```bash
make build VERSION=v1.0.0
./bin/etude --version   # prints: etude v1.0.0
```

Cutting the actual release (tagging the commit and setting `VERSION`) is a
human release action. The full ordered procedure is covered by the v1 release
checklist (etude-kb0.4), which references these mechanics.

## CLI

```bash
./bin/etude --help
./bin/etude --version
./bin/etude init
./bin/etude init --force
./bin/etude init --remote upstream
./bin/etude capture plan --run run-1 --output output=plan.md
./bin/etude capture-run spec.yaml
./bin/etude run list
./bin/etude run show run-1
./bin/etude sync
./bin/etude sync --remote upstream
./bin/etude replay run-1 plan --runner ./run.sh
./bin/etude replay run-1 plan --runner ./run.sh --output result.md
./bin/etude capture-gate --run run-1 --gate-file gate.json
./bin/etude bench plan --last 10 --runner ./run.sh --judge ./judge.sh
./bin/etude gc
./bin/etude reindex
./bin/etude retro capture workflow --file retro.md
./bin/etude retro generate run --subject-run run-1 --generator ./retro.sh
./bin/etude retro list
./bin/etude retro show run-1-20260523
./bin/etude prime
./bin/etude log
./bin/etude log --kind retro --subject run-1 --limit 20
```

See [Init](docs/init.md) for the init command.
See [Manual Capture](docs/capture.md) for the capture command.
See [Batch Capture](docs/capture-run.md) for `etude capture-run`.
See [Gate reviewer records](docs/gates.md) for `etude capture-gate` and gate inspection.
See [Runs](docs/run.md) for the run list and run show commands.
See [Sync](docs/sync.md) for the sync command.
See [Replay](docs/replay.md) for the replay command.
See [Bench](docs/bench.md) for the bench command.
See [GC](docs/gc.md) for the gc command.
See [Reindex](docs/reindex.md) for the reindex command.
See [Prime](docs/cli/etude_prime.md) for the prime command.
See [CLI reference](docs/cli/etude.md) for the generated per-command reference.

For a no-tracker walkthrough (no beads, no LLM, just git + sh + etude), see
[examples/summarize/README.md](examples/summarize/README.md).
