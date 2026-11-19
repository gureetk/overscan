#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Usage: sudo ./install.sh [--no-ffmpeg]
#
# Installs Overscan from the unpacked release archive this script came in, or
# upgrades an installation, on a Linux system with systemd, such as a Debian
# container on Proxmox:
#
# - an "overscan" system user;
# - the binary in /usr/local/bin;
# - ffmpeg from the distribution, with its ffprobe, which repairs the streams
#   of channels set to be repaired (D57) and converts the sound of films that
#   a box cannot play (D68), unless it is there already or --no-ffmpeg is
#   given;
# - /etc/overscan/overscan.yaml from the example, unless it exists already;
# - the systemd unit, enabled and (re)started.
#
# The data lives in /var/lib/overscan, which systemd creates for the service.
set -euo pipefail

with_ffmpeg=yes
for arg in "$@"; do
	case "$arg" in
	--no-ffmpeg) with_ffmpeg=no ;;
	*)
		echo "install.sh: unknown option $arg; the only one is --no-ffmpeg" >&2
		exit 2
		;;
	esac
done

if [ "$(id -u)" -ne 0 ]; then
	echo "install.sh: run this as root, for example with sudo" >&2
	exit 1
fi
here=$(dirname "$(readlink -f "$0")")
for f in overscan overscan.example.yaml overscan.service; do
	if [ ! -f "$here/$f" ]; then
		echo "install.sh: $f is missing; run this from the unpacked release archive" >&2
		exit 1
	fi
done
if ! command -v systemctl >/dev/null; then
	echo "install.sh: systemd is needed" >&2
	exit 1
fi

if ! id overscan >/dev/null 2>&1; then
	useradd --system --home-dir /var/lib/overscan --no-create-home \
		--shell /usr/sbin/nologin overscan
	echo "Added the overscan user."
fi

install -m 0755 "$here/overscan" /usr/local/bin/overscan
echo "Installed $(/usr/local/bin/overscan -version) in /usr/local/bin."

if [ "$with_ffmpeg" = yes ] && ! command -v ffmpeg >/dev/null; then
	no_ffmpeg="channels set to be repaired, and films whose sound a box cannot play, play as they are until ffmpeg is installed"
	if command -v apt-get >/dev/null; then
		echo "Installing ffmpeg, which repairs streams and converts films' sound..."
		if apt-get update -qq && DEBIAN_FRONTEND=noninteractive \
			apt-get install -y -qq --no-install-recommends ffmpeg >/dev/null; then
			echo "Installed $(ffmpeg -hide_banner -version | head -n 1)."
		else
			echo "install.sh: ffmpeg could not be installed; $no_ffmpeg" >&2
		fi
	else
		echo "install.sh: install ffmpeg with the system's package manager; $no_ffmpeg" >&2
	fi
fi

install -d -m 0750 -o root -g overscan /etc/overscan
if [ -e /etc/overscan/overscan.yaml ]; then
	echo "Kept /etc/overscan/overscan.yaml."
else
	install -m 0640 -o root -g overscan "$here/overscan.example.yaml" /etc/overscan/overscan.yaml
	echo "Wrote /etc/overscan/overscan.yaml from the example."
fi

install -m 0644 "$here/overscan.service" /etc/systemd/system/overscan.service
systemctl daemon-reload
systemctl enable --quiet overscan
systemctl restart overscan

sleep 2
if systemctl is-active --quiet overscan; then
	echo "Overscan is running. Its log: journalctl -u overscan"
	echo "Manage it as its user, for example: runuser -u overscan -- overscan devices list"
else
	echo "install.sh: Overscan did not start. The end of its log:" >&2
	journalctl -u overscan -n 20 --no-pager >&2 || true
	echo "If the log mentions status 226/NAMESPACE, the container needs the nesting" >&2
	echo "feature (Proxmox: the container's Options, Features, Nesting)." >&2
	exit 1
fi
