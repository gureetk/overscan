#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Usage: tools/build-release.sh VERSION
#
# Builds release archives for Linux on amd64 and arm64 into dist/, each with
# the binary, the licence, the README, an example configuration, the install
# script and systemd unit, the Dockerfile and Compose file, and the licences
# of the third-party code compiled in, plus a SHA256SUMS file. The
# binaries are static (no cgo), stripped and built with -trimpath, so the same
# commit and Go toolchain give the same binaries.
set -euo pipefail

version="${1:?usage: $0 VERSION}"
cd "$(git rev-parse --show-toplevel)"

rm -rf dist
mkdir -p dist

for arch in amd64 arm64; do
	name="overscan_${version#v}_linux_${arch}"
	dir="dist/$name"
	mkdir -p "$dir/licenses"

	CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
		go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$dir/overscan" ./cmd/overscan
	cp LICENSE README.md deploy/overscan.example.yaml deploy/install.sh deploy/systemd/overscan.service \
		deploy/docker/Dockerfile deploy/docker/compose.yaml "$dir/"

	GOOS=linux GOARCH="$arch" go list -deps \
		-f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Dir}}{{end}}{{end}}' ./cmd/overscan |
		sort -u | while read -r path module_dir; do
			mkdir -p "$dir/licenses/$path"
			cp "$module_dir"/LICENSE* "$dir/licenses/$path/"
		done

	tar -C dist -czf "dist/$name.tar.gz" "$name"
	rm -rf "$dir"
done

(cd dist && sha256sum -- *.tar.gz >SHA256SUMS)
