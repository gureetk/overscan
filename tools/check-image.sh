#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Usage: tools/check-image.sh DIR
#
# Builds and starts the container image with Docker Compose from DIR, which
# holds an overscan binary, the Dockerfile and compose.yaml, as an unpacked
# release archive does, then checks that the server answers, finds ffmpeg,
# runs as its own user and keeps its files private, and that the backup and
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
