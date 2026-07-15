BINARY := kubectl-add
INSTALL_DIR := $(HOME)/.local/bin
CMD := ./cmd/kubectl-add

.PHONY: build install test test-e2e clean

build:
	go build -o $(BINARY) $(CMD)

install:
	go build -o $(INSTALL_DIR)/$(BINARY) $(CMD)

test:
	go test ./...

test-e2e:
	go test -tags e2e -count=1 ./test/e2e/ -v -timeout 15m

clean:
	rm -f $(BINARY)
