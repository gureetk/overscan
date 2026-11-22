#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Usage: tools/build-release.sh VERSION
#
# Builds release archives for Linux on amd64 and arm64 into dist/, each with
# the binary, the licence, the README, an example configuration, the install
# script and systemd unit, the Dockerfile and Compose file, and the licences
# of the third-party code compiled in; a Debian package for each, of the same
# binary (D116, D124); and a SHA256SUMS file. The binaries are static (no
# cgo), stripped and built with -trimpath, so the same commit and Go
# toolchain give the same binaries, and the packages' files carry the
# commit's time.
set -euo pipefail

version="${1:?usage: $0 VERSION}"
cd "$(git rev-parse --show-toplevel)"

rm -rf dist
mkdir -p dist
# A Debian version sorts a pre-release before its release: 1.0.0~rc.1.
deb_version=${version#v}
deb_version=${deb_version/-/\~}
epoch=$(git log -1 --format=%ct)

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

	# The Debian package: the same binary in /usr/bin, the unit pointing at
	# it, the example configuration for postinst to copy, and the docs.
	pkg="dist/deb-$arch"
	install -D -m 0755 "$dir/overscan" "$pkg/usr/bin/overscan"
	install -d -m 0755 "$pkg/usr/lib/systemd/system" "$pkg/usr/share/overscan" "$pkg/usr/share/doc/overscan" "$pkg/DEBIAN"
	sed 's|/usr/local/bin/overscan|/usr/bin/overscan|' deploy/systemd/overscan.service >"$pkg/usr/lib/systemd/system/overscan.service"
	install -m 0644 deploy/overscan.example.yaml "$pkg/usr/share/overscan/overscan.example.yaml"
	install -m 0644 README.md LICENSE "$pkg/usr/share/doc/overscan/"
	cp -r "$dir/licenses" "$pkg/usr/share/doc/overscan/licenses"
	install -m 0755 deploy/debian/postinst deploy/debian/prerm deploy/debian/postrm "$pkg/DEBIAN/"
	cat >"$pkg/DEBIAN/control" <<CONTROL
Package: overscan
Version: $deb_version
Architecture: $arch
Maintainer: gureet <noreply@git.gureet.ca>
Installed-Size: $(du -sk --exclude=DEBIAN "$pkg" | cut -f1)
Depends: ffmpeg
Section: video
Priority: optional
Homepage: https://github.com/gureetk/overscan
Description: Portal server for Stalker-compatible set-top boxes and apps
 Overscan serves live TV, films, series and their guides from M3U
 playlists, Xtream servers, Stalker portals, Jellyfin, Plex and folders to
 MAG boxes, Stalker apps and players, on a home network.
CONTROL
	find "$pkg" -exec touch -h -d "@$epoch" {} +
	dpkg-deb --root-owner-group -Zxz --build "$pkg" "dist/overscan_${version#v}_${arch}.deb" >/dev/null
	rm -rf "$dir" "$pkg"
done

(cd dist && sha256sum -- *.tar.gz *.deb >SHA256SUMS)
