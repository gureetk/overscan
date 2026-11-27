#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Usage: tools/check-image.sh DIR
#
# Builds and starts the container image with Docker Compose from DIR, which
# holds an overscan binary, the Dockerfile and compose.yaml, as an unpacked
# release archive does, then checks that the server answers, finds ffmpeg,
# holds VA-API drivers and tests the graphics, runs as its own user and
# keeps its files private, and that the backup and
# restore steps of docs/install-docker.md work. It stops the container and
# removes its volume at the end.
set -euo pipefail

dir="${1:?usage: $0 DIR}"
cd "$dir"
compose=(docker compose --project-name overscan-check)

work=$(mktemp -d)
cleanup() {
	"${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

fail() {
	echo "check-image: $*" >&2
	"${compose[@]}" logs --no-color >&2 || true
	exit 1
}

answers() {
	for _ in $(seq 1 60); do
		if curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	return 1
}

"${compose[@]}" up --detach --build
answers || fail "the server does not answer /healthz"
echo "The server answers."

# grep reads the whole log rather than stopping at the match (-q): that would
# cut the logs command off with SIGPIPE, which pipefail takes for a failure.
"${compose[@]}" logs --no-color | grep "ffmpeg found to repair streams with" >/dev/null || fail "the server found no ffmpeg"
"${compose[@]}" logs --no-color | grep "ffprobe found to read" >/dev/null || fail "the server found no ffprobe"
echo "The server found ffmpeg and ffprobe."

# Debian's VA-API drivers are in the image (D149), and the test of the
# graphics ends, one way or the other, without any.
"${compose[@]}" exec -T overscan sh -c 'ls /usr/lib/*/dri/*_drv_video.so' | grep -E '/(radeonsi|iHD)_drv_video\.so$' >/dev/null ||
	fail "the image has no VA-API drivers"
tested=no
for _ in $(seq 1 30); do
	if "${compose[@]}" logs --no-color | grep -E "graphics tested for ffmpeg|no graphics for ffmpeg" >/dev/null; then
		tested=yes
		break
	fi
	sleep 1
done
[ "$tested" = yes ] || fail "the server did not say what its graphics can do"
"${compose[@]}" exec -T overscan overscan gpu || [ $? -eq 1 ] || fail "the gpu command failed"
echo "The image has VA-API drivers, and tests the graphics."

uid=$("${compose[@]}" exec -T overscan id -u)
[ "$uid" != 0 ] || fail "the server runs as root"
echo "The server runs as user $uid."

modes=$("${compose[@]}" exec -T overscan stat -c '%a %n' /var/lib/overscan /var/lib/overscan/overscan.db \
	/var/lib/overscan/overscan.db-shm /var/lib/overscan/secret.key)
echo "$modes"
if echo "$modes" | grep -v -E '^(700|600) '; then
	fail "files that others can read"
fi

"${compose[@]}" exec -T overscan overscan backup || fail "the backup failed"

# The backup and restore steps of docs/install-docker.md.
"${compose[@]}" exec -T overscan overscan backup /var/lib/overscan/backups/overscan.db
"${compose[@]}" cp overscan:/var/lib/overscan/backups/overscan.db "$work/overscan.db"
"${compose[@]}" exec -T overscan rm /var/lib/overscan/backups/overscan.db
"${compose[@]}" stop
"${compose[@]}" run --rm -T --entrypoint sh overscan \
	-c 'cd /var/lib/overscan && cat >overscan.db && rm -f overscan.db-wal overscan.db-shm' <"$work/overscan.db"
"${compose[@]}" start
answers || fail "the server does not answer after a restore"
owner=$("${compose[@]}" exec -T overscan stat -c '%a %U' /var/lib/overscan/overscan.db)
[ "$owner" = "600 overscan" ] || fail "the restored database is $owner"
echo "Backed up and restored."
echo "The image works."
