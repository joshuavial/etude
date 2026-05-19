BINARY := etude
BIN_DIR := bin
VERSION ?= dev

.PHONY: build test lint clean

build:
	mkdir -p $(BIN_DIR)
	go build -ldflags "-X github.com/joshuavial/etude/internal/cli.version=$(VERSION)" -o $(BIN_DIR)/$(BINARY) ./cmd/etude

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -rf $(BIN_DIR)
