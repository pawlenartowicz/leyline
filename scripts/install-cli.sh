#!/bin/bash
# install-cli.sh — install leyline CLI on a user's laptop
#
# Detects OS/arch, checks Go toolchain, runs `go install`, verifies
# $PATH, and prints the next step. Curl-pipe friendly; no clone needed.
# Idempotent: re-running reinstalls latest, exits 0.
#
# Usage:
#   bash install-cli.sh
#   curl https://raw.githubusercontent.com/pawlenartowicz/leyline/main/scripts/install-cli.sh | bash

set -euo pipefail

# Detect OS. The CLI is linux-only — the daemon relies on unix-only syscalls
# (flock, unix sockets) not yet ported to macOS/Windows.
os=$(uname -s)
case "$os" in
	Linux) os_type="linux" ;;
	*)
		echo "Error: unsupported OS: $os"
		echo "leyline currently ships linux-only."
		exit 1
		;;
esac

# Detect architecture
arch=$(uname -m)
case "$arch" in
	x86_64) arch_type="amd64" ;;
	arm64)  arch_type="arm64" ;;
	aarch64) arch_type="arm64" ;;
	*)
		echo "Error: unsupported architecture: $arch"
		echo "leyline is supported on amd64 and arm64."
		exit 1
		;;
esac

echo "Detected: $os_type / $arch_type"

# Check Go is installed
if ! command -v go &>/dev/null; then
	echo "Error: Go is not installed. Install Go 1.25+ from https://go.dev/dl/, then re-run this script."
	exit 1
fi

echo "Installing leyline..."
go install github.com/pawlenartowicz/leyline/cmd/leyline@latest

# Determine $GOBIN or fallback to $GOPATH/bin
gobin="${GOBIN:-$(go env GOPATH)/bin}"

# Check if leyline is on $PATH
if ! command -v leyline &>/dev/null; then
	echo "Warning: leyline binary is installed at $gobin, but it is not on your \$PATH."
	echo "Add $gobin to your \$PATH and re-run: leyline --version"
	exit 0
fi

echo "leyline installed successfully!"
echo ""
echo "Next step:"
echo "  leyline init <host>/<vaultID>"
echo ""
echo "Example:"
echo "  leyline init example.com/my-vault"
