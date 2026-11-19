#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Usage: tools/check-dco.sh BASE HEAD
#
# Checks that every commit in BASE..HEAD, merge commits aside, carries a
# Signed-off-by trailer for its author, certifying the Developer Certificate of
# Origin. See CONTRIBUTING.md.
set -euo pipefail

if [ "$#" -ne 2 ]; then
	echo "usage: $0 BASE HEAD" >&2
	exit 2
fi

status=0
while IFS= read -r commit; do
	author="$(git log -1 --format='%an <%ae>' "$commit")"
	if ! git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$commit" | grep -qxF -- "$author"; then
		echo "$(git log -1 --format='%h %s' "$commit"): no 'Signed-off-by: $author'" >&2
		status=1
	fi
done < <(git rev-list --no-merges "$1..$2")

exit "$status"
