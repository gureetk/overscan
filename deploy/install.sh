#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Usage: sudo ./install.sh [--no-ffmpeg] [--gpu | --no-gpu]
#        sudo ./install.sh --uninstall
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
# - the systemd unit, enabled and (re)started;
# - with --gpu, a drop-in that lets the service reach the server's graphics
#   (D148, D149), for ffmpeg to transcode on them, the overscan user in the
#   group that owns them, and the VA-API driver for Intel or AMD graphics;
#   --no-gpu removes the drop-in. Without either, an upgrade keeps what an
#   earlier install chose.
#
# The data lives in /var/lib/overscan, which systemd creates for the service.
#
# --uninstall stops the service and removes it, its drop-in and the binary.
# It keeps the configuration, the data and the user, which it names.
set -euo pipefail

with_ffmpeg=yes
uninstall=no
gpu=keep
for arg in "$@"; do
	case "$arg" in
	--no-ffmpeg) with_ffmpeg=no ;;
	--uninstall) uninstall=yes ;;
	--gpu) gpu=yes ;;
	--no-gpu) gpu=no ;;
	-h | --help)
		echo "Usage: sudo ./install.sh [--no-ffmpeg] [--gpu | --no-gpu]"
		echo "                                    install or upgrade Overscan as a service; --gpu lets it"
		echo "                                    transcode on the server's graphics, --no-gpu stops that"
		echo "       sudo ./install.sh --uninstall  remove the service and the binary, keeping the data"
		exit 0
		;;
	*)
		echo "install.sh: unknown option $arg; the options are --no-ffmpeg, --gpu, --no-gpu and --uninstall" >&2
		exit 2
		;;
	esac
done
dropin_dir=/etc/systemd/system/overscan.service.d
dropin=$dropin_dir/gpu.conf

if [ "$(id -u)" -ne 0 ]; then
	echo "install.sh: run this as root, for example with sudo" >&2
	exit 1
fi
if ! command -v systemctl >/dev/null; then
	echo "install.sh: systemd is needed" >&2
	exit 1
fi
# A Debian package's Overscan is upgraded and removed with its package.
if command -v dpkg-query >/dev/null &&
	[ "$(dpkg-query -W -f '${db:Status-Status}' overscan 2>/dev/null)" = installed ]; then
	echo "install.sh: Overscan is installed from its Debian package here; upgrade it with the" >&2
	echo "new package (apt install ./overscan_<version>_<arch>.deb), or remove it with apt remove overscan" >&2
	exit 1
fi

if [ "$uninstall" = yes ]; then
	systemctl disable --now --quiet overscan 2>/dev/null || true
	rm -f /etc/systemd/system/overscan.service /usr/local/bin/overscan "$dropin"
	rmdir "$dropin_dir" 2>/dev/null || true
	systemctl daemon-reload
	echo "Stopped Overscan and removed the service and /usr/local/bin/overscan."
	echo "Kept /etc/overscan, the data in /var/lib/overscan and the overscan user;"
	echo "remove them yourself once you no longer need them."
	exit 0
fi
here=$(dirname "$(readlink -f "$0")")
for f in overscan overscan.example.yaml overscan.service overscan-gpu.conf; do
	if [ ! -f "$here/$f" ]; then
		echo "install.sh: $f is missing; run this from the unpacked release archive" >&2
		exit 1
	fi
done
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

# The graphics (D148, D149): the drop-in, the group and the driver.
if [ "$gpu" = yes ]; then
	if ! getent group render >/dev/null; then
		echo "install.sh: there is no render group, which owns the graphics' render devices; without" >&2
		echo "udev or systemd's sysusers there may be none. Add it, or leave out --gpu." >&2
		exit 1
	fi
	usermod -aG render overscan
	install -d -m 0755 "$dropin_dir"
	install -m 0644 "$here/overscan-gpu.conf" "$dropin"
	echo "Installed $dropin, which lets the service reach the graphics."
	# The driver for the first render device's maker, where apt can install it.
	vendor=""
	for node in /sys/class/drm/renderD*/device/vendor; do
		if [ -r "$node" ]; then
			vendor=$(cat "$node")
			break
		fi
	done
	case "$vendor" in
	0x8086) driver=intel-media-va-driver ;;
	0x1002) driver=mesa-va-drivers ;;
	*) driver="" ;;
	esac
	if [ -z "$driver" ]; then
		echo "install.sh: no Intel or AMD render device found; install the VA-API driver for the graphics" >&2
		echo "yourself, such as intel-media-va-driver or mesa-va-drivers." >&2
	elif dpkg-query -W -f '${db:Status-Status}' "$driver" 2>/dev/null | grep -q '^installed$'; then
		echo "Kept $driver, the VA-API driver for the graphics."
	elif command -v apt-get >/dev/null; then
		echo "Installing $driver, the VA-API driver for the graphics..."
		if apt-get update -qq && DEBIAN_FRONTEND=noninteractive \
			apt-get install -y -qq --no-install-recommends "$driver" >/dev/null; then
			echo "Installed $driver."
		else
			echo "install.sh: $driver could not be installed; ffmpeg transcodes in software until it is" >&2
		fi
	else
		echo "install.sh: install $driver with the system's package manager" >&2
	fi
elif [ "$gpu" = no ] && [ -e "$dropin" ]; then
	rm -f "$dropin"
	rmdir "$dropin_dir" 2>/dev/null || true
	echo "Removed $dropin: the service no longer reaches the graphics."
fi
systemctl daemon-reload
systemctl enable --quiet overscan
systemctl restart overscan

sleep 2
if systemctl is-active --quiet overscan; then
	echo "Overscan is running. Its log: journalctl -u overscan"
	echo "Manage it as its user, for example: runuser -u overscan -- overscan devices list"
	if [ -e "$dropin" ]; then
		echo "What the graphics can do: runuser -u overscan -- overscan gpu, or the admin's Transcoding page"
	fi
else
	echo "install.sh: Overscan did not start. The end of its log:" >&2
	journalctl -u overscan -n 20 --no-pager >&2 || true
	echo "If the log mentions status 226/NAMESPACE, the container needs the nesting" >&2
	echo "feature (Proxmox: the container's Options, Features, Nesting)." >&2
	exit 1
fi
