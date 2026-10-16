# Single-module dev targets. The pre-consolidation umbrella Makefile orchestrated
# seven repos (integration/Frog/demo/multi-repo deploy); those targets are dropped
# — integration-tests is a deferred separate repo, and web/obsidian are siblings now.

# Local builds carry `git describe` (e.g. 0.8.0, 0.8.0-3-gabc123-dirty); release
# builds get the tag from goreleaser. Outside a git checkout the version is "dev".
VERSION ?= $(or $(patsubst v%,%,$(shell git describe --tags --dirty 2>/dev/null)),dev)
LDFLAGS := -s -w -X github.com/pawlenartowicz/leyline/internal/buildinfo.Value=$(VERSION)

.PHONY: build test vet snapshot install

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

# Local release dry-run — builds all four binaries + the deb/rpm/apk packages
# without publishing. Requires goreleaser and a git repo (run after `git init`).
snapshot:
	goreleaser release --snapshot --clean

# Install the two binaries the user invokes from PATH (server + plugin live in
# deploy/dev workflows, not on PATH).
install:
	go build -ldflags "$(LDFLAGS)" -o $(HOME)/.local/bin/leyline ./cmd/leyline
	go build -ldflags "$(LDFLAGS)" -o $(HOME)/.local/bin/leyline-web ./cmd/leyline-web
