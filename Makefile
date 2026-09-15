.PHONY: build build-all test test-race test-cover lint vet fmt install clean hooks

CLI_BINARY := llm-semantic
MCP_BINARY := llm-semantic-mcp
BUILD_DIR  := build
DIST_DIR   := dist

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")

# Version is stamped into each binary's own package. Both targets are vars
# rather than consts, because -X rewrites a symbol and a const has none.
CLI_LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)"
MCP_LDFLAGS := -ldflags "-s -w -X main.serverVersion=$(VERSION)"

PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64

build:
	@mkdir -p $(BUILD_DIR)
	go build $(CLI_LDFLAGS) -o $(BUILD_DIR)/$(CLI_BINARY) ./cmd/llm-semantic
	go build $(MCP_LDFLAGS) -o $(BUILD_DIR)/$(MCP_BINARY) ./cmd/llm-semantic-mcp

build-all:
	@mkdir -p $(DIST_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "Building $$os/$$arch..."; \
		GOOS=$$os GOARCH=$$arch go build $(CLI_LDFLAGS) -o $(DIST_DIR)/$(CLI_BINARY)-$$os-$$arch$$ext ./cmd/llm-semantic; \
		GOOS=$$os GOARCH=$$arch go build $(MCP_LDFLAGS) -o $(DIST_DIR)/$(MCP_BINARY)-$$os-$$arch$$ext ./cmd/llm-semantic-mcp; \
	done

test:
	go test ./...

test-race:
	go test -race ./...

test-cover:
	go test -coverprofile=coverage.txt -covermode=atomic ./...
	go tool cover -func=coverage.txt | tail -1

lint: vet fmt

vet:
	go vet ./...

fmt:
	gofmt -s -l .

install: build
	@echo "Installing to /usr/local/bin (may require sudo)..."
	install -m 755 $(BUILD_DIR)/$(CLI_BINARY) /usr/local/bin/$(CLI_BINARY)
	install -m 755 $(BUILD_DIR)/$(MCP_BINARY) /usr/local/bin/$(MCP_BINARY)

hooks:
	git config core.hooksPath .githooks

clean:
	rm -rf $(BUILD_DIR) $(DIST_DIR) coverage.txt coverage.html
