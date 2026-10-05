# Tandem daemon.
#
# The SQLite session extension has to be compiled into mattn's amalgamation, and
# a per-package #cgo CFLAGS directive cannot reach another package's C sources.
# These flags therefore have to be set for the whole program at link time, which
# makes them a property of the build rather than of a source file. Keeping them
# here means nobody has to remember an environment variable, and nobody can
# accidentally build without them and discover it at runtime.
#
# Without them the session symbols are absent from the amalgamation and the link
# fails loudly on sqlite3session_attach. That failure is the good version of this
# problem, but it is still a failure, so the Makefile is the supported path.

CGO_CFLAGS := -DSQLITE_ENABLE_SESSION -DSQLITE_ENABLE_PREUPDATE_HOOK
export CGO_CFLAGS

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
PKG     := github.com/10xdev4u-alt/tandem/internal/version
LDFLAGS := -X $(PKG).Commit=$(COMMIT)

BIN     := bin/tandemd
CONFIG  := config.json

.PHONY: all build test vet fmt fmt-check check run tidy clean help

all: check build

## build: compile the daemon into bin/
build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/tandemd

## test: run every test with the flags the session extension needs
test:
	go test ./... -count=1 -timeout 120s

## vet: static analysis
vet:
	go vet ./...

## fmt: rewrite sources
fmt:
	gofmt -w .

## fmt-check: fail if anything is unformatted
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "unformatted files:"; echo "$$unformatted"; exit 1; \
	fi

## check: everything CI should run
check: fmt-check vet test

## run: build and start the daemon against config.json
run: build
	$(BIN) --config $(CONFIG)

## tidy: prune and verify module requirements
tidy:
	go mod tidy

## clean: remove build output
clean:
	rm -rf bin

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
