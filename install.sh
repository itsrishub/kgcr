#!/usr/bin/env bash
set -e

# --- Configuration ---
REPO="itsrishub/kgcr"             # change this to your repo
BINARY_NAME="kgcr"           # change this to your binary name
INSTALL_DIR="/usr/local/bin"
VERSION="1.0.0"

# --- Detect OS and Architecture ---
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "Detected: $OS-$ARCH"

# --- Create temp directory ---
TMP_DIR=$(mktemp -d)
cd "$TMP_DIR"

# --- Download binary tar.gz ---
TAR_NAME="${BINARY_NAME}_${VERSION}_${OS}_${ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${TAR_NAME}"

echo "Downloading from: $DOWNLOAD_URL"
curl -fsSL "$DOWNLOAD_URL" -o "$TAR_NAME"

# --- Extract and move ---
tar -xzf "$TAR_NAME"
chmod +x "$BINARY_NAME"

echo "Moving ${BINARY_NAME} to ${INSTALL_DIR}"
sudo mv "$BINARY_NAME" "$INSTALL_DIR/"

# --- Verify ---
if command -v "$BINARY_NAME" >/dev/null 2>&1; then
    echo "Installed successfully: $($BINARY_NAME --version 2>/dev/null || echo "$BINARY_NAME")"
else
    echo "Installation finished but command not found in PATH"
fi

# --- Cleanup ---
cd /
rm -rf "$TMP_DIR"
