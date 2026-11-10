#!/bin/sh
# Download installer for llm-semantic and llm-semantic-mcp.
#
# Installs both binaries from a GitHub release into ~/.local/bin. It needs no
# sudo, no clone and no Go: only sh, curl or wget, tar and sha256sum or shasum.
# To build from source instead, use install.sh in a checkout.
#
#   curl -fsSL https://github.com/samestrin/llm-semantic-axi/releases/latest/download/install-release.sh | sh
#
# The copy attached to a release is stamped with that release's tag, so
# .../releases/download/vX.Y.Z/install-release.sh installs vX.Y.Z, not latest.
# --version overrides the stamp.
#
# Options (each also read from the environment variable named after it):
#   --version vX.Y.Z  LLM_SEMANTIC_VERSION          release to install (default: the stamped tag, else latest)
#   --base-url URL    LLM_SEMANTIC_BASE_URL         where the release assets live
#   --dir DIR         LLM_SEMANTIC_INSTALL_DIR      install directory (default: ~/.local/bin)
#   --timeout SECS    LLM_SEMANTIC_INSTALL_TIMEOUT  per-download limit (default: 600)
#
# The archive is checked against the release's SHA256SUMS.txt before anything
# is written, and a mismatch, a missing checksum or a cut-off download stops
# the install with the existing binaries untouched.
set -eu

REPO_URL="https://github.com/samestrin/llm-semantic-axi"
# Release packaging rewrites this line to the release tag (release.yml).
PINNED_VERSION=""
VERSION="${LLM_SEMANTIC_VERSION:-$PINNED_VERSION}"
BASE_URL="${LLM_SEMANTIC_BASE_URL:-}"
INSTALL_DIR="${LLM_SEMANTIC_INSTALL_DIR:-${HOME}/.local/bin}"
TIMEOUT="${LLM_SEMANTIC_INSTALL_TIMEOUT:-600}"

die() {
    echo "install-release.sh: error: $*" >&2
    exit 1
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
        --base-url) [ $# -ge 2 ] || die "--base-url needs a value"; BASE_URL="$2"; shift 2 ;;
        --dir) [ $# -ge 2 ] || die "--dir needs a value"; INSTALL_DIR="$2"; shift 2 ;;
        --timeout) [ $# -ge 2 ] || die "--timeout needs a value"; TIMEOUT="$2"; shift 2 ;;
        -h | --help) sed -n '2,22p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) die "unknown option: $1" ;;
    esac
done

if [ -z "$BASE_URL" ]; then
    if [ -n "$VERSION" ]; then
        BASE_URL="$REPO_URL/releases/download/$VERSION"
    else
        BASE_URL="$REPO_URL/releases/latest/download"
    fi
fi
BASE_URL="${BASE_URL%/}"

case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) die "unsupported OS $(uname -s): release archives exist for Linux and macOS" ;;
esac
case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "unsupported architecture $(uname -m): release archives exist for amd64 and arm64" ;;
esac
ARCHIVE="llm-semantic-${os}-${arch}.tar.gz"

# Everything written before the final mv lives in $work or in the staged
# files, and both are removed on any exit, so a failed or interrupted run
# leaves nothing behind.
work=""
stage_cli=""
stage_mcp=""
cleanup() {
    if [ -n "$work" ]; then rm -rf "$work"; fi
    if [ -n "$stage_cli" ]; then rm -f "$stage_cli"; fi
    if [ -n "$stage_mcp" ]; then rm -f "$stage_mcp"; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

download() {
    if command -v curl >/dev/null 2>&1; then
        curl --fail --location --silent --show-error \
            --connect-timeout 30 --max-time "$TIMEOUT" -o "$2" "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget --quiet --tries=1 --timeout="$TIMEOUT" -O "$2" "$1"
    else
        die "neither curl nor wget is installed"
    fi
}

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{ print $1 }'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{ print $1 }'
    else
        die "neither sha256sum nor shasum is installed"
    fi
}

work="$(mktemp -d "${TMPDIR:-/tmp}/llm-semantic-install.XXXXXX")"

echo "Downloading $ARCHIVE from $BASE_URL"
download "$BASE_URL/SHA256SUMS.txt" "$work/SHA256SUMS.txt" ||
    die "could not download $BASE_URL/SHA256SUMS.txt"
download "$BASE_URL/$ARCHIVE" "$work/$ARCHIVE" ||
    die "could not download $BASE_URL/$ARCHIVE"

expected="$(awk -v f="$ARCHIVE" '$2 == f || $2 == "*" f { print $1; exit }' "$work/SHA256SUMS.txt")"
[ -n "$expected" ] || die "SHA256SUMS.txt has no checksum for $ARCHIVE; refusing to install an unverified archive"
actual="$(sha256 "$work/$ARCHIVE")"
if [ "$actual" != "$expected" ]; then
    die "checksum mismatch for $ARCHIVE (expected $expected, got $actual); refusing to install. Nothing was changed."
fi
echo "Checksum verified"

mkdir "$work/x"
tar -xzf "$work/$ARCHIVE" -C "$work/x" llm-semantic llm-semantic-mcp ||
    die "$ARCHIVE does not hold both llm-semantic and llm-semantic-mcp"
for bin in llm-semantic llm-semantic-mcp; do
    [ -f "$work/x/$bin" ] || die "$ARCHIVE does not hold $bin"
done

# Stage both binaries inside the install directory, so the final mv is a
# rename on one filesystem: each binary is either the old file or the whole
# new one, never a partial copy. The two renames run with INT and TERM
# ignored, so a stop cannot land between them and split the pair.
mkdir -p "$INSTALL_DIR"
stage_cli="$INSTALL_DIR/.llm-semantic.install.$$"
stage_mcp="$INSTALL_DIR/.llm-semantic-mcp.install.$$"
cp "$work/x/llm-semantic" "$stage_cli"
cp "$work/x/llm-semantic-mcp" "$stage_mcp"
chmod 755 "$stage_cli" "$stage_mcp"
trap '' INT TERM
mv -f "$stage_cli" "$INSTALL_DIR/llm-semantic"
stage_cli=""
mv -f "$stage_mcp" "$INSTALL_DIR/llm-semantic-mcp"
stage_mcp=""
trap 'exit 130' INT
trap 'exit 143' TERM

echo "Installed llm-semantic and llm-semantic-mcp to $INSTALL_DIR"

case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
        echo ""
        echo "WARNING: $INSTALL_DIR is not on your PATH."
        echo "llm-semantic-mcp finds llm-semantic through PATH, so the MCP server cannot run"
        echo "the CLI until it is. Add this line to your shell profile and open a new shell:"
        echo ""
        echo "    export PATH=\"$INSTALL_DIR:\$PATH\""
        ;;
esac
