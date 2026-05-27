# RELEASING

Ordered v1 release procedure. Follow these steps in sequence to cut the
`v1.0.0` tag. The doc does **not** cut the tag itself — that is a human action
(step 5).

For what is in v1 versus deferred, see
[`docs/plans/product/V1-SCOPE.md`](docs/plans/product/V1-SCOPE.md).

For version mechanics (ldflags, `make build VERSION=...`, `--version` wiring),
see the [`## Releasing`](README.md#releasing) section of `README.md`.

---

## 1. Pre-flight

Ensure the working tree is clean and on the intended release commit:

```bash
git status          # must be clean (no uncommitted changes)
git pull            # fast-forward to latest origin/main
```

All subsequent steps assume a clean tree on the commit that will be tagged.

---

## 2. Green-gate

Run each check and confirm it exits 0:

```bash
go build ./...         # all packages compile
go test ./...          # all tests pass
make lint              # gofmt -l cmd internal must be empty; go vet ./... must pass
make docs-reality      # hand-written doc / CLI drift check passes
make reconcile         # docs-reality + docs-check both pass
make dogfood-audit     # recent closed beads have run refs, gate records, and pushed refs
```

Expected result for each: exit 0, no error output. `make lint` passes only
when `gofmt -l cmd internal` produces no output (all Go source files are
gofmt-clean) and `go vet ./...` reports no issues.

Do not proceed past step 2 if any check is non-zero.

---

## 3. Version and CHANGELOG

Pick the release version: **v1.0.0**.

Open `CHANGELOG.md` and update the unreleased heading for this version:

```
## [v1.0.0] — Unreleased
```

Change it to the release date, for example:

```
## [v1.0.0] — 2026-05-27
```

Remove (or replace) the `> v1.0.0 has not been tagged ...` note beneath it,
as it will no longer be accurate once the tag is cut.

For the version mechanics (how `make build` stamps the binary via ldflags),
see the [`## Releasing`](README.md#releasing) section of `README.md` — those
details are not repeated here.

---

## 4. Build and verify version

Build the release binary with the version stamp:

```bash
make build VERSION=v1.0.0
./bin/etude --version
```

Expected output:

```
etude v1.0.0
```

If the version string does not match, re-check step 3 and the ldflags wiring
described in `README.md ## Releasing` before continuing.

---

## 5. Cut the tag — HUMAN action

The operator cuts the tag and pushes it to the remote:

```bash
git tag v1.0.0
git push origin v1.0.0
```

This doc describes the procedure; it does not run these commands.

After pushing the tag, verify it appears on the remote:

```bash
git ls-remote origin refs/tags/v1.0.0
```

---

## 6. Sync etude run data (refs caveat)

`refs/etude/*` refs do **not** travel with a plain `git push` or `git fetch`.
Those commands transfer only `refs/heads/*` and `refs/tags/*` by default. A
fresh clone or CI environment will not see captured run data (runs, evals,
retros) after a normal clone.

To move run data explicitly:

```bash
etude init     # writes the +refs/etude/*:refs/etude/* fetch refspec into
               # git config so subsequent plain git-fetches pick up the namespace
etude sync     # explicitly pushes and fetches refs/etude/* right now
```

`etude sync` passes the refspec on the command line and works regardless of
whether `etude init` was previously run. Running both is the recommended
post-release sequence: `init` wires up future fetches; `sync` transfers any
run data accumulated on the release commit immediately.

See [`docs/sync.md`](docs/sync.md) for the full sync behavior, reconciliation
rules, and error cases.

---

## 7. Scope pointer

For the definitive list of what is in v1 and what is deferred (live xenota
capture adapter, `etude import`, standalone `etude eval`, `query` command,
external artifact pointer capture via the CLI, docs site), see
[`docs/plans/product/V1-SCOPE.md`](docs/plans/product/V1-SCOPE.md).
