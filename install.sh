#!/usr/bin/env sh
set -eu

REPO="${APX_REPO:-bberka/agentpassage}"
BRANCH="${APX_BRANCH:-main}"
INSTALL_DIR="${APX_INSTALL_DIR:-$HOME/.local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

case "$OS" in
  linux|darwin) ;;
  *) echo "Unsupported OS: $OS (use install.ps1 on Windows)" >&2; exit 1 ;;
esac

if ! command -v go >/dev/null 2>&1; then
  echo "Go (1.24+) is required to build AgentPassage (apx) from source." >&2
  if [ "$OS" = "darwin" ]; then
    echo "Install Go with:  brew install go" >&2
  else
    echo "Install Go from:  https://go.dev/dl/  (or your package manager: apt/pacman/dnf install go)" >&2
  fi
  exit 1
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

SRC_DIR="$TMP/src"
if command -v git >/dev/null 2>&1; then
  echo "Cloning AgentPassage (${REPO}@${BRANCH})..."
  git clone --depth 1 --branch "$BRANCH" "https://github.com/${REPO}.git" "$SRC_DIR" >/dev/null 2>&1
else
  URL="https://github.com/${REPO}/archive/refs/heads/${BRANCH}.tar.gz"
  echo "Downloading AgentPassage source from ${URL}..."
  curl -fsSL "$URL" -o "$TMP/src.tar.gz"
  mkdir -p "$SRC_DIR"
  tar -xzf "$TMP/src.tar.gz" --strip-components=1 -C "$SRC_DIR"
fi

echo "Building apx (${OS}/${ARCH})..."
COMMIT=""
if [ -d "$SRC_DIR/.git" ]; then
  COMMIT="$(git -C "$SRC_DIR" rev-parse --short HEAD)"
fi
(cd "$SRC_DIR" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/bberka/agentpassage/internal/gateway.Commit=$COMMIT" -o "$TMP/apx" ./cmd/apx)

mkdir -p "$INSTALL_DIR"
# Atomic install works even while an older apx binary is running.
install -m 0755 "$TMP/apx" "$INSTALL_DIR/apx.tmp"
mv -f "$INSTALL_DIR/apx.tmp" "$INSTALL_DIR/apx"

echo "Installed apx to $INSTALL_DIR/apx"

# Restart background service automatically if already installed.
if "$INSTALL_DIR/apx" service status 2>/dev/null | grep -qi "Installed:[[:space:]]*true"; then
  echo "Restarting running apx background service..."
  "$INSTALL_DIR/apx" service restart || true
fi

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo ""
    echo "Add $INSTALL_DIR to your PATH:"
    echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
    ;;
esac

echo ""
echo "Run 'apx' to open the TUI, or 'apx service install --auto-start' to run in the background."
