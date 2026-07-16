BINARY := kubectl-add
INSTALL_DIR := $(HOME)/.local/bin
CMD := ./cmd/kubectl-add

# VERSION identifies the build; overridable, e.g. make build VERSION=v0.3.0.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
VERSION_PKG := github.com/scaffoldly/kubectl-add/v1alpha1/version
# -s -w strip the symbol table and DWARF debug info for a smaller binary.
LDFLAGS := -s -w -X $(VERSION_PKG).Version=$(VERSION)

# Versioned install (the Claude Code model), flat in INSTALL_DIR. kubectl only
# discovers executables whose name starts with the literal `kubectl-` prefix, so
# the versioned binary `kubectl_add_<VERSION>` (underscores — no `kubectl-`
# prefix) is invisible to plugin discovery; only the `kubectl-add` symlink is
# found. Repointing the symlink swaps versions atomically without touching the
# running binary, and rollback is just a symlink flip. Every version sits
# alongside the link in INSTALL_DIR — no separate store dir, no $PWD dependency.
# Strip the leading `v` from the tag so the file reads `kubectl_add_0.1.0`, not
# `kubectl_add_v0.1.0` (the `--version` string keeps the conventional `v`).
VERSIONED_NAME := kubectl_add_$(VERSION:v%=%)
VERSIONED_BIN := $(INSTALL_DIR)/$(VERSIONED_NAME)
INSTALL_LINK := $(INSTALL_DIR)/$(BINARY)

.PHONY: build install link uninstall vet test test-e2e clean

vet:
	go vet ./...

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

# Build the versioned binary into INSTALL_DIR, then repoint the PATH symlink.
install: link

link:
	mkdir -p $(INSTALL_DIR)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(VERSIONED_BIN) $(CMD)
	ln -sfn $(VERSIONED_NAME) $(INSTALL_LINK)
	@echo "linked $(INSTALL_LINK) -> $(VERSIONED_NAME)"

# Remove the symlink and every versioned binary this scheme installed.
uninstall:
	rm -f $(INSTALL_LINK) $(INSTALL_DIR)/kubectl_add_*

test:
	go test -short ./...

test-e2e:
	go test -tags e2e -count=1 ./test/e2e/ -v -timeout 15m

# Remove the repo-root build artifact and the local dev install (symlink +
# versioned binaries in INSTALL_DIR).
clean: uninstall
	rm -f $(BINARY)
