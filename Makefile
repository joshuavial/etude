BINARY := etude
BIN_DIR := bin
VERSION ?= dev
DOCS_DIR := docs/cli

.PHONY: build test lint clean docs docs-check example

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

example: build
	@ETUDE_BIN=$(CURDIR)/$(BIN_DIR)/$(BINARY) bash examples/summarize/walkthrough.sh
