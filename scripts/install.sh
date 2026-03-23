#!/bin/sh
set -e

REPO="zuchka/ding"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Detect OS
OS="$(uname -s)"
case "$OS" in
  Linux)  OS="linux" ;;
  Darwin) OS="darwin" ;;
  *)      echo "Unsupported OS: $OS" && exit 1 ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)         ARCH="amd64" ;;
  aarch64|arm64)  ARCH="arm64" ;;
  *)              echo "Unsupported arch: $ARCH" && exit 1 ;;
esac

# Get latest version from GitHub API
VERSION="$(curl -sf "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\(.*\)".*/\1/')"

if [ -z "$VERSION" ]; then
  echo "Could not determine latest release version." && exit 1
fi

FILENAME="ding_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${FILENAME}"
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading ding ${VERSION} for ${OS}/${ARCH}..."
curl -sfL "$URL" -o "${TMP}/${FILENAME}"
curl -sfL "$CHECKSUM_URL" -o "${TMP}/checksums.txt"

# Verify checksum (works on Linux and macOS)
cd "$TMP"
grep "$FILENAME" checksums.txt > check.txt
sha256sum -c check.txt 2>/dev/null \
  || shasum -a 256 -c check.txt 2>/dev/null \
  || { echo "Checksum verification failed." && exit 1; }

tar -xzf "$FILENAME"

# Install (use sudo only if needed)
if [ -w "$INSTALL_DIR" ]; then
  mv ding "$INSTALL_DIR/ding"
else
  sudo mv ding "$INSTALL_DIR/ding"
fi

echo "ding ${VERSION} installed to ${INSTALL_DIR}/ding"
