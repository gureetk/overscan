#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Usage: tools/check-packages.sh DIST
#
# Checks what a release ships, as built by tools/build-release.sh into DIST,
# on a fresh Debian system with systemd, in a container (D124):
#
# - the amd64 Debian package installs, starts the service and answers its
#   health check; installs again over itself, keeping a changed
#   configuration file; is removed, keeping the configuration and the data;
#   and is purged, keeping the data;
# - the amd64 archive's install.sh installs and starts the service, runs
#   again as an upgrade, and uninstalls, keeping the configuration and the
#   data.
#
# It needs Docker, and runs the containers privileged, as systemd needs.
set -euo pipefail

dist=$(readlink -f "${1:?usage: $0 DIST}")
deb=$(cd "$dist" && ls overscan_*_amd64.deb)
archive=$(cd "$dist" && ls overscan_*_linux_amd64.tar.gz)
image=overscan-check-debian

docker build -q -t "$image" - >/dev/null <<'DOCKERFILE'
FROM debian:trixie-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends systemd systemd-sysv curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
DOCKERFILE

container=""
cleanup() {
	if [ -n "$container" ]; then
		docker rm -f "$container" >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

# start runs a fresh system, and waits for systemd to be up.
start() {
	cleanup
	container=$(docker run -d --privileged --cgroupns=host -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
		-v "$dist:/dist:ro" "$image")
	for _ in $(seq 60); do
		state=$(docker exec "$container" systemctl is-system-running 2>/dev/null || true)
		case "$state" in running | degraded) return 0 ;; esac
		sleep 1
	done
	echo "check-packages: systemd did not start in the container ($state)" >&2
	exit 1
}

# inside runs a shell script in the container, and shows the service's log
# if it fails.
inside() {
	if ! docker exec -i "$container" bash -euxo pipefail; then
		docker exec "$container" journalctl -u overscan --no-pager -n 40 >&2 || true
		echo "check-packages: $1 failed" >&2
		exit 1
	fi
}

echo "== the Debian package"
start
inside "installing the package" <<SCRIPT
apt-get update -qq
apt-get install -y --no-install-recommends /dist/$deb
overscan -version
systemctl is-active overscan
for _ in \$(seq 20); do curl -fsS http://127.0.0.1:8080/healthz && break; sleep 1; done
curl -fsS http://127.0.0.1:8080/healthz
[ "\$(stat -c '%a %U %G' /etc/overscan/overscan.yaml)" = "640 root overscan" ]
[ "\$(stat -c '%a %U' /var/lib/overscan)" = "700 overscan" ]
test -e /var/lib/overscan/overscan.db
SCRIPT
inside "installing it again" <<SCRIPT
echo '# kept by an upgrade' >>/etc/overscan/overscan.yaml
apt-get install -y --reinstall --no-install-recommends /dist/$deb
grep -q '# kept by an upgrade' /etc/overscan/overscan.yaml
systemctl is-active overscan
SCRIPT
inside "removing the package" <<'SCRIPT'
apt-get remove -y overscan
if systemctl is-active --quiet overscan; then exit 1; fi
test ! -e /usr/bin/overscan
test -e /etc/overscan/overscan.yaml
test -e /var/lib/overscan/overscan.db
apt-get purge -y overscan
test ! -e /etc/overscan/overscan.yaml
test -e /var/lib/overscan/overscan.db
getent passwd overscan
SCRIPT

echo "== the archive's install.sh"
start
inside "installing from the archive" <<SCRIPT
mkdir -p /tmp/archive
tar -C /tmp/archive -xzf /dist/$archive
cd /tmp/archive/overscan_*
./install.sh --no-ffmpeg
systemctl is-active overscan
for _ in \$(seq 20); do curl -fsS http://127.0.0.1:8080/healthz && break; sleep 1; done
curl -fsS http://127.0.0.1:8080/healthz
echo '# kept by an upgrade' >>/etc/overscan/overscan.yaml
./install.sh --no-ffmpeg
grep -q '# kept by an upgrade' /etc/overscan/overscan.yaml
systemctl is-active overscan
./install.sh --uninstall
if systemctl is-active --quiet overscan; then exit 1; fi
test ! -e /usr/local/bin/overscan
test ! -e /etc/systemd/system/overscan.service
test -e /etc/overscan/overscan.yaml
test -e /var/lib/overscan/overscan.db
SCRIPT

echo "check-packages: the package and the archive install, upgrade and uninstall cleanly"
