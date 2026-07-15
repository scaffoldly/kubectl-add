BINARY := kubectl-add
INSTALL_DIR := $(HOME)/.local/bin

.PHONY: build install clean

build:
	go build -o $(BINARY) .

install:
	go build -o $(INSTALL_DIR)/$(BINARY) .

clean:
	rm -f $(BINARY)
