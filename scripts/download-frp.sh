#!/usr/bin/env bash
# download-frp.sh — Download pinned frp binaries for bundling with hatchway.
#
# Usage: ./scripts/download-frp.sh [output_dir]
#   output_dir defaults to dist/frp
#
# Pinned version: v0.68.1
# Checksums are verified against the official release artifacts.

set -euo pipefail

FRP_VERSION="v0.68.1"
OUTPUT_DIR="${1:-dist/frp}"
BASE_URL="https://github.com/fatedier/frp/releases/download/${FRP_VERSION}"

# SHA256 checksums from the official release page
declare -A CHECKSUMS=(
	["frp_0.68.1_darwin_amd64.tar.gz"]="41a3b1a21f60b92021764bad923f9e76168a65afc282456609f3b0f2cbed4abf"
	["frp_0.68.1_darwin_arm64.tar.gz"]="55ed076c7e17b8907e02d4da141426cb2dbc17b4f560be179fc5878caf021640"
	["frp_0.68.1_linux_amd64.tar.gz"]="4a4e88987d39561e1b3b3b23d0ede48a457eebf76a87231999957e870f5f02b6"
	["frp_0.68.1_linux_arm64.tar.gz"]="e7ad15b0cfe4cf0125df4217778b66cb4426179270967b59900ecb2362d8cd01"
)

TARGETS=(
	"darwin_amd64"
	"darwin_arm64"
	"linux_amd64"
	"linux_arm64"
)

mkdir -p "$OUTPUT_DIR"

for target in "${TARGETS[@]}"; do
	archive="frp_0.68.1_${target}.tar.gz"
	url="${BASE_URL}/${archive}"
	out="${OUTPUT_DIR}/${archive}"

	echo "Downloading ${archive}..."
	curl -fSL -o "$out" "$url"

	# Verify checksum
	expected="${CHECKSUMS[$archive]}"
	actual=$(shasum -a 256 "$out" | cut -d' ' -f1)
	if [ "$actual" != "$expected" ]; then
		echo "ERROR: checksum mismatch for ${archive}"
		echo "  expected: ${expected}"
		echo "  actual:   ${actual}"
		exit 1
	fi

	# Extract frpc and frps binaries
	tmpdir=$(mktemp -d)
	tar xzf "$out" -C "$tmpdir"
	mv "${tmpdir}/frp_0.68.1_${target}/frpc" "${OUTPUT_DIR}/frpc_${target}"
	mv "${tmpdir}/frp_0.68.1_${target}/frps" "${OUTPUT_DIR}/frps_${target}"
	rm -rf "$tmpdir" "$out"

	chmod +x "${OUTPUT_DIR}/frpc_${target}" "${OUTPUT_DIR}/frps_${target}"
	echo "  -> frpc_${target}, frps_${target}"
done

echo "Done. Binaries in ${OUTPUT_DIR}/"
