BINARY := etude
BIN_DIR := bin
VERSION ?= dev
DOCS_DIR := docs/cli

.PHONY: build test lint clean docs docs-check docs-reality reconcile example

build:
	mkdir -p $(BIN_DIR)
	go build -ldflags "-X github.com/joshuavial/etude/internal/cli.version=$(VERSION)" -o $(BIN_DIR)/$(BINARY) ./cmd/etude

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
