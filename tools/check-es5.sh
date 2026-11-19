#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Checks that the browser scripts under web/, hand-written or built, parse as
# ES5, which is all a set-top box's old browser can be assumed to run. The
# admin, under web/admin/, runs in desktop browsers and is left out. It needs
# Node.js and fetches a pinned version of the acorn parser with npx. See
# CONTRIBUTING.md.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

status=0
while IFS= read -r -d '' file; do
	if ! npx --yes acorn@8.19.0 --ecma5 --silent "$file"; then
		echo "$file: does not parse as ES5" >&2
		status=1
	fi
done < <(git ls-files -z -- 'web/*.js' ':!web/admin/*')

exit "$status"
