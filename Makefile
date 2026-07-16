BINARY := kubectl-add
INSTALL_DIR := $(HOME)/.local/bin
CMD := ./cmd/kubectl-add

# VERSION identifies the build; overridable, e.g. make build VERSION=v0.3.0.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
VERSION_PKG := github.com/scaffoldly/kubectl-add/v1alpha1/version
# -s -w strip the symbol table and DWARF debug info for a smaller binary.
LDFLAGS := -s -w -X $(VERSION_PKG).Version=$(VERSION)

.PHONY: build install test test-e2e clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

install:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(INSTALL_DIR)/$(BINARY) $(CMD)

test:
	go test ./...

test-e2e:
	go test -tags e2e -count=1 ./test/e2e/ -v -timeout 15m

clean:
	rm -f $(BINARY)
