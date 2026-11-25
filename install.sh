#!/usr/bin/env sh
set -eu

REPO="${APX_REPO:-bberka/agentpassage}"
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
  *) echo "Unsupported OS: $OS (download Windows binary from Releases)" >&2; exit 1 ;;
esac

VERSION="${APX_VERSION:-latest}"
if [ "$VERSION" = "latest" ]; then
  URL="https://github.com/${REPO}/releases/latest/download/apx_${OS}_${ARCH}.tar.gz"
else
  URL="https://github.com/${REPO}/releases/download/${VERSION}/apx_${OS}_${ARCH}.tar.gz"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading AgentPassage (apx ${OS}/${ARCH}) from ${URL}..."
curl -fsSL "$URL" -o "$TMP/apx.tar.gz"
tar -xzf "$TMP/apx.tar.gz" -C "$TMP"

mkdir -p "$INSTALL_DIR"
install -m 0755 "$TMP/apx" "$INSTALL_DIR/apx"

echo "Installed apx to $INSTALL_DIR/apx"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Add $INSTALL_DIR to your PATH to run 'apx' directly." ;;
esac
