#!/bin/bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# INSTALL_DIR may be overridden, e.g. INSTALL_DIR="$HOME/.local/bin" ./install.sh
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BUILD_DIR="build"

# Binaries to build and install. The CLI must come first: the MCP server
# resolves the CLI binary at startup (see internal/semantic/mcpserver).
BINARIES=(
    "llm-semantic"
    "llm-semantic-mcp"
)

echo -e "${YELLOW}llm-semantic installer${NC}"
echo "======================"

# Create the install directory if it is missing, so the check below tests the
# real target. Root is needed only when it is not writable, as /usr/local/bin
# is not for most users.
mkdir -p "$INSTALL_DIR" 2>/dev/null || true
if [[ $EUID -ne 0 && ! -w "$INSTALL_DIR" ]]; then
    echo -e "${RED}Error: $INSTALL_DIR is not writable; run with sudo, or set INSTALL_DIR to a directory you can write${NC}"
    echo "Usage: sudo ./install.sh"
    exit 1
fi

# Get the directory where the script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Stamped from the script's own checkout, so a run from another directory
# does not stamp that directory's commit.
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo dev)"
CLI_LDFLAGS="-s -w -X main.version=$VERSION -X main.commit=$COMMIT"
MCP_LDFLAGS="-s -w -X main.serverVersion=$VERSION"

# Check for go
if ! command -v go &> /dev/null; then
    echo -e "${RED}Error: go is not installed${NC}"
    exit 1
fi

echo -e "${GREEN}Building binaries...${NC}"
mkdir -p "$BUILD_DIR"

echo "  Building llm-semantic..."
go build -ldflags "$CLI_LDFLAGS" -o "$BUILD_DIR/llm-semantic" ./cmd/llm-semantic
echo "  Building llm-semantic-mcp..."
go build -ldflags "$MCP_LDFLAGS" -o "$BUILD_DIR/llm-semantic-mcp" ./cmd/llm-semantic-mcp

echo -e "${GREEN}Installing to $INSTALL_DIR...${NC}"
for bin in "${BINARIES[@]}"; do
    echo "  Installing $bin..."
    # Clear xattrs on source before copying
    xattr -cr "$BUILD_DIR/$bin" 2>/dev/null || true
    # Remove old binary first
    rm -f "$INSTALL_DIR/$bin"
    # Copy binary
    cp "$BUILD_DIR/$bin" "$INSTALL_DIR/$bin"
    # Clear all extended attributes recursively
    xattr -cr "$INSTALL_DIR/$bin" 2>/dev/null || true
    # Re-sign with ad-hoc signature (required for Apple Silicon)
    codesign --force --sign - "$INSTALL_DIR/$bin" 2>/dev/null || true
    chmod 755 "$INSTALL_DIR/$bin"
done

echo ""
echo -e "${GREEN}Installation complete!${NC}"
echo "Installed ${#BINARIES[@]} binaries to $INSTALL_DIR"

# limit2 runs a command with a 2-second limit. Stock macOS has no `timeout`;
# Homebrew's coreutils installs it as `gtimeout`. With neither, the command
# runs unlimited: both binaries exit at once for --version or an empty stdin.
limit2() {
    if command -v timeout >/dev/null 2>&1; then
        timeout 2 "$@"
    elif command -v gtimeout >/dev/null 2>&1; then
        gtimeout 2 "$@"
    else
        "$@"
    fi
}

# Verify installation
echo ""
echo "Verifying installation..."
FAILED=0
for bin in "${BINARIES[@]}"; do
    if [[ -x "$INSTALL_DIR/$bin" ]]; then
        # MCP binaries need stdin, so just check they start without being killed
        if [[ "$bin" == *"-mcp" ]]; then
            # Send empty input and check it doesn't get killed (exit 137 = killed)
            # `|| EXIT_CODE=$?` keeps set -e from ending the script here.
            EXIT_CODE=0
            echo "" | limit2 "$INSTALL_DIR/$bin" >/dev/null 2>&1 || EXIT_CODE=$?
            if [[ $EXIT_CODE -eq 137 || $EXIT_CODE -eq 143 ]]; then
                echo -e "  ${RED}✗${NC} $bin (killed by macOS)"
                FAILED=1
            else
                echo -e "  ${GREEN}✓${NC} $bin"
            fi
        else
            # CLI binaries - try --version first, fall back to --help
            if limit2 "$INSTALL_DIR/$bin" --version >/dev/null 2>&1 || \
               limit2 "$INSTALL_DIR/$bin" --help >/dev/null 2>&1; then
                echo -e "  ${GREEN}✓${NC} $bin"
            else
                echo -e "  ${RED}✗${NC} $bin"
                FAILED=1
            fi
        fi
    else
        echo -e "  ${RED}✗${NC} $bin (not found)"
        FAILED=1
    fi
done

if [[ $FAILED -eq 1 ]]; then
    echo -e "${RED}Some binaries failed to install${NC}"
    exit 1
fi

echo ""
echo -e "${GREEN}All binaries installed successfully!${NC}"
