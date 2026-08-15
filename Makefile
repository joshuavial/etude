BINARY := etude
BIN_DIR := bin
VERSION ?= dev
DOCS_DIR := docs/cli
LDFLAGS := -X github.com/joshuavial/etude/internal/cli.version=$(VERSION)

.PHONY: build install test lint clean docs docs-check docs-reality reconcile example dogfood-audit dogfood-audit-test pre-push-test retro-index retro-index-test seat-adapter-test shell-test

build:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/etude

# Install to $GOBIN (or $GOPATH/bin) with the same version stamp as build. Use
# this instead of a plain `go install ./cmd/etude`, which stamps `dev`.
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/etude

test:
	go test ./...

lint:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...

clean:
	rm -rf $(BIN_DIR)

docs:
	go run ./cmd/gen-docs -out $(DOCS_DIR)

docs-check:
	@TMP=$$(mktemp -d); trap 'rm -rf "$$TMP"' EXIT; \
		go run ./cmd/gen-docs -out "$$TMP" && diff -r "$$TMP" $(DOCS_DIR)

# Mechanical guard against hand-written-doc/CLI drift. Kept SEPARATE from
# docs-check (which only diffs generated docs/cli) so it can report hand-written
# drift without breaking the generated-docs check.
docs-reality:
	@bash scripts/docs-reality-check.sh

# Epic-close holistic gate: re-runs the whole-surface docs/reality checks at the
# integration point after all sibling beads have landed. MUST exit 0 before
# bd close <epic>. Fails non-zero if either leg fails.
reconcile:
	$(MAKE) docs-reality
	$(MAKE) docs-check

example: build
	@ETUDE_BIN=$(CURDIR)/$(BIN_DIR)/$(BINARY) bash examples/summarize/walkthrough.sh

# Dogfood completeness audit: check whether recent closed beads have run refs,
# gate records, and pushed refs. Uses --last 9 by default.
dogfood-audit:
	@bash scripts/dogfood-completeness-audit.sh --last 9

# Fixture-based tests for dogfood-completeness-audit.sh.
dogfood-audit-test:
	@bash scripts/dogfood-completeness-audit_test.sh

# Fixture-based tests for the .beads/hooks/pre-push enforcement block. These run
# against THIS checkout's tracked hook. Note core.hooksPath points at the main
# checkout's .beads/hooks, so from a worktree the hook under test is not yet the
# one executing on pushes — see bead etude-6d9 for making that drift detectable.
pre-push-test:
	@bash scripts/pre-push_test.sh

# Fixture tests for the gate seat adapter. Its fail-closed paths are the only
# thing standing between a truncated model reply and a recorded GO.
seat-adapter-test:
	@bash scripts/seat-adapter_test.sh

# Read-only cross-retro failure-mode / root-cause index over current cadence retros.
retro-index:
	@bash scripts/retro-meta-index.sh

# Fixture-based tests for retro-meta-index.sh.
retro-index-test:
	@bash scripts/retro-meta-index_test.sh

# Every shell suite in one command. `make test` is `go test ./...` and reaches
# none of these, and there is no CI, so this is discoverability rather than
# enforcement — it still takes a person to run it.
shell-test: dogfood-audit-test pre-push-test seat-adapter-test retro-index-test
