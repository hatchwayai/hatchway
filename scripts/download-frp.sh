#!/usr/bin/env bash
# download-frp.sh — Download pinned frp binaries for bundling with hatchway.
#
# Usage: ./scripts/download-frp.sh [output_dir]
#   output_dir defaults to .release/frp
#
# Pinned version: v0.69.0
# Checksums are verified against the official release artifacts.

set -euo pipefail

FRP_VERSION="0.69.0"
OUTPUT_DIR="${1:-.release/frp}"
BASE_URL="https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}"

TARGETS=(
	"darwin_amd64"
	"darwin_arm64"
	"linux_amd64"
	"linux_arm64"
)

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
		return
	fi
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
		return
	fi
	echo "ERROR: sha256sum or shasum is required" >&2
	return 1
}

expected_checksum() {
	# Use a case statement instead of an associative array so the release
	# helper also runs on the Bash 3.2 shipped with macOS.
	case "$1" in
	"frp_0.69.0_darwin_amd64.tar.gz")
		echo "3bb1df7aa716a80ddd0b0f108b4e6487bc1e9dae60b22bb67fff6c890bfcc182"
		;;
	"frp_0.69.0_darwin_arm64.tar.gz")
		echo "07663f5fa71330f074b25e32cc8bc4ae5ed40d9c2ee1690cbd981774475997a2"
		;;
	"frp_0.69.0_linux_amd64.tar.gz")
		echo "6b90d1cd28fc661f170c0de90dde03d2c63e4fd7ce0ae2da2ca1c28014b8146e"
		;;
	"frp_0.69.0_linux_arm64.tar.gz")
		echo "24a4fc82b4c041835103419685ea124c4d6a7dbf83d0425481c5831b4ce4b3a4"
		;;
	*)
		echo "ERROR: no checksum configured for $1" >&2
		return 1
		;;
	esac
}

mkdir -p "$OUTPUT_DIR"

for target in "${TARGETS[@]}"; do
	archive="frp_${FRP_VERSION}_${target}.tar.gz"
	url="${BASE_URL}/${archive}"
	out="${OUTPUT_DIR}/${archive}"

	echo "Downloading ${archive}..."
	curl -fSL --retry 3 --retry-all-errors --connect-timeout 10 -o "$out" "$url"

	# Verify checksum
	expected=$(expected_checksum "$archive")
	actual=$(sha256_file "$out")
	if [ "$actual" != "$expected" ]; then
		echo "ERROR: checksum mismatch for ${archive}"
		echo "  expected: ${expected}"
		echo "  actual:   ${actual}"
		exit 1
	fi

	# Client release archives bundle only frpc. The server image downloads its
	# own pinned frps binary in Dockerfile.frps.
	tmpdir=$(mktemp -d)
	tar xzf "$out" -C "$tmpdir"
	target_dir="${OUTPUT_DIR}/${target}"
	mkdir -p "$target_dir"
	mv "${tmpdir}/frp_${FRP_VERSION}_${target}/frpc" "${target_dir}/frpc"
	rm -rf "$tmpdir" "$out"

	chmod +x "${target_dir}/frpc"
	echo "  -> ${target}/frpc"
done

echo "Done. Binaries in ${OUTPUT_DIR}/"
