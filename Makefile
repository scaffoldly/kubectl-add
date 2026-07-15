BINARY := kubectl-add
INSTALL_DIR := $(HOME)/.local/bin

.PHONY: build install test test-e2e clean

build:
	go build -o $(BINARY) .

install:
	go build -o $(INSTALL_DIR)/$(BINARY) .

test:
	go test ./...

test-e2e:
	go test -tags e2e -count=1 ./test/e2e/ -v -timeout 15m

clean:
	rm -f $(BINARY)
