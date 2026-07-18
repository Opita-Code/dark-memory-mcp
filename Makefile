# Makefile for dark-memory-mcp.
#
# Per CONSTITUTION.md Rule 1, every release build goes through
# `make release`, which calls scripts/inject-version.sh to resolve
# the canonical version from the git tag and inject it via
# `-ldflags "-X internal/version.buildVersion=<v>"`.
#
# On Windows hosts without bash, run scripts/inject-version.ps1
# directly. The Makefile assumes a POSIX shell (git-bash, WSL, or
# Linux/macOS).

# Resolve the canonical -ldflags expression. Output is something like
# "-X github.com/dark-agents/dark-memory-mcp/internal/version.buildVersion=1.3.2"
# or "-X ...buildVersion=1.3.2-3-gabc1234" for commits past a tag.
ifeq ($(OS),Windows_NT)
    # git-bash on Windows: scripts/inject-version.sh is the canonical
    # path; the .ps1 variant is a fallback for pure PowerShell sessions.
    INJECT_VERSION := bash scripts/inject-version.sh
    MKDIR_BIN := mkdir
    RM_BIN := rm -f
    BINS_EXT := .exe
else
    INJECT_VERSION := ./scripts/inject-version.sh
    MKDIR_BIN := mkdir -p
    RM_BIN := rm -f
    BINS_EXT :=
endif

VERSION_LDFLAGS := $(shell $(INJECT_VERSION))

# Where the built binaries land.
BIN_DIR := bin

# Binaries produced. The .exe suffix is appended on Windows by `go
# build` automatically; the BINS_EXT variable above documents this
# for shell-based cleanup targets.
BINS := \
    $(BIN_DIR)/dark-mem-mcp \
    $(BIN_DIR)/dark-mem-cli \
    $(BIN_DIR)/dark-mem-inspect

# Default target: show the available targets.
.PHONY: help
help:
	@echo "dark-memory-mcp Makefile"
	@echo ""
	@echo "Targets:"
	@echo "  make build       Build all 3 binaries into $(BIN_DIR)/ (dev mode: ldflags=dev)"
	@echo "  make release     Build all 3 binaries with the canonical git tag injected"
	@echo "  make drift-check Run drift checks (version, git status, vet, unit tests)"
	@echo "  make test        Run go test ./..."
	@echo "  make clean       Remove $(BIN_DIR)/"
	@echo "  make version     Print the resolved version + commit + dirty flag"
	@echo "  make inspect     Run $(BIN_DIR)/dark-mem-inspect --json"
	@echo "  make tag         Print the latest git tag (for CI version pinning)"
	@echo ""
	@echo "Override the version explicitly:  DARK_VERSION=1.4.0-rc.1 make release"

# Build (dev mode). Uses the resolver's debug.ReadBuildInfo() path; the
# version printed by `dark-mem-mcp` will reflect the module version or
# "dev" if built from a non-tagged commit. Drift warnings are expected
# here.
#
# Each cmd/<binary> is its own Go module (it imports the parent library
# via a `replace` directive). We cd into each module to run `go build`
# because the parent module's `go build ./cmd/...` does not see the
# child modules.
.PHONY: build
build: $(BIN_DIR)
	cd cmd/dark-mem-mcp     && go build -o ../../$(BIN_DIR)/dark-mem-mcp .
	cd cmd/dark-mem-cli     && go build -o ../../$(BIN_DIR)/dark-mem-cli .
	cd cmd/dark-mem-inspect && go build -o ../../$(BIN_DIR)/dark-mem-inspect .

# Release build. The version resolver is injected with the canonical
# git tag via scripts/inject-version.sh. This is the only path that
# satisfies CONSTITUTION.md Rule 1 for production binaries.
.PHONY: release
release: $(BIN_DIR)
	@echo "Injecting version: $(VERSION_LDFLAGS)"
	cd cmd/dark-mem-mcp     && go build -ldflags "$(VERSION_LDFLAGS)" -o ../../$(BIN_DIR)/dark-mem-mcp .
	cd cmd/dark-mem-cli     && go build -ldflags "$(VERSION_LDFLAGS)" -o ../../$(BIN_DIR)/dark-mem-cli .
	cd cmd/dark-mem-inspect && go build -ldflags "$(VERSION_LDFLAGS)" -o ../../$(BIN_DIR)/dark-mem-inspect .
	@echo "Built:"
	@ls -lh $(BIN_DIR)/

# Drift check: a battery of pre-commit / pre-push gates.
#   1. Working tree is clean (Rule 3: tags and CHANGELOG must be in
#      the same commit; a dirty tree at release time is a smell).
#   2. HEAD is at a git tag.
#   3. CHANGELOG.md has the matching `## [<tag>]` entry.
#   4. Internal version unit tests pass.
#   5. `go vet ./...` is clean.
# Exits non-zero on any failure. Run before `git push` and before
# cutting a new tag.
.PHONY: drift-check
drift-check:
	@echo "--- drift-check ---"
	@echo "1. Working tree status:"
	@git status --short --branch || (echo "drift-check: not a git repo" && exit 1)
	@if [ -n "$$(git status --porcelain)" ]; then \
	    echo "drift-check: FAIL: working tree is dirty" >&2; \
	    exit 1; \
	fi
	@echo "  ok (clean)"
	@echo ""
	@echo "2. HEAD is at a tag:"
	@TAG=$$(git describe --tags --exact-match HEAD 2>/dev/null || echo ""); \
	if [ -z "$$TAG" ]; then \
	    echo "drift-check: FAIL: HEAD is not at any tag" >&2; \
	    exit 1; \
	fi; \
	echo "  tag: $$TAG"
	@echo ""
	@echo "3. CHANGELOG.md has matching entry:"
	@TAG=$$(git describe --tags --exact-match HEAD); \
	VERSION=$${TAG#v}; \
	if ! grep -q "^## \[$$VERSION\]" CHANGELOG.md; then \
	    echo "drift-check: FAIL: CHANGELOG.md missing entry for $$VERSION" >&2; \
	    exit 1; \
	fi; \
	echo "  CHANGELOG.md has [$$VERSION] entry"
	@echo ""
	@echo "4. internal/version unit tests:"
	go test -count=1 ./internal/version/... || (echo "drift-check: FAIL: version tests" && exit 1)
	@echo ""
	@echo "5. go vet:"
	go vet ./... || (echo "drift-check: FAIL: go vet" && exit 1)
	@echo ""
	@echo "drift-check: ALL GREEN"

# Tests.
.PHONY: test
test:
	go test ./...

# Cleanup.
.PHONY: clean
clean:
	$(RM_BIN) $(BINS)
	@rmdir $(BIN_DIR) 2>/dev/null || true

# Print the resolved version (matches what the resolver would inject).
.PHONY: version
version:
	@bash scripts/inject-version.sh --raw
	@echo ""

# Print the JSON fingerprint (used by CI to stamp release artifacts).
.PHONY: version-json
version-json:
	@bash scripts/inject-version.sh --json

# Run the inspect binary against the default store.
.PHONY: inspect
inspect: $(BIN_DIR)/dark-mem-inspect
	$(BIN_DIR)/dark-mem-inspect --json

# Print the latest tag (for CI pinning).
.PHONY: tag
tag:
	@git describe --tags --abbrev=0

# Directory target.
$(BIN_DIR):
	$(MKDIR_BIN) $(BIN_DIR)
