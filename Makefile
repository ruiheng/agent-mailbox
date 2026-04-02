GO ?= go
CMD_PATH ?= ./cmd/mailbox
BIN_DIR ?= bin
BINARY_NAME ?= agent-mailbox
MCP_CMD_PATH ?= ./cmd/agent-mailbox-mcp
MCP_BINARY_NAME ?= agent-mailbox-mcp
PREFIX ?= /usr/local
DESTDIR ?=
INSTALL_DIR ?= $(PREFIX)/bin
BUILD_OUTPUT := $(BIN_DIR)/$(BINARY_NAME)
MCP_BUILD_OUTPUT := $(BIN_DIR)/$(MCP_BINARY_NAME)

.PHONY: help build build-mcp test run run-mcp install install-mcp clean

help:
	@printf '%s\n' \
		'Available targets:' \
		'  make build                 Build the agent-mailbox CLI into $(BUILD_OUTPUT)' \
		'  make build-mcp             Build the stdio MCP server into $(MCP_BUILD_OUTPUT)' \
		'  make test                  Run the Go test suite' \
		'  make run ARGS="..."        Run the CLI with go run and pass ARGS through' \
		'  make run-mcp               Run the stdio MCP server with go run' \
		'  make install               Install the built CLI into $(DESTDIR)$(INSTALL_DIR)' \
		'  make install-mcp           Install the MCP server into $(DESTDIR)$(INSTALL_DIR)' \
		'  make clean                 Remove local build output'

build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BUILD_OUTPUT) $(CMD_PATH)

build-mcp:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(MCP_BUILD_OUTPUT) $(MCP_CMD_PATH)

test:
	$(GO) test ./...

run:
	$(GO) run $(CMD_PATH) $(ARGS)

run-mcp:
	$(GO) run $(MCP_CMD_PATH)

install: build
	@mkdir -p $(DESTDIR)$(INSTALL_DIR)
	install -m 0755 $(BUILD_OUTPUT) $(DESTDIR)$(INSTALL_DIR)/$(BINARY_NAME)

install-mcp: build-mcp
	@mkdir -p $(DESTDIR)$(INSTALL_DIR)
	install -m 0755 $(MCP_BUILD_OUTPUT) $(DESTDIR)$(INSTALL_DIR)/$(MCP_BINARY_NAME)

clean:
	rm -rf $(BIN_DIR)
