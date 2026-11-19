#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Checks that every tracked source file names its licence in an
# SPDX-License-Identifier line within its first five lines. Files under
# docs/protocol/, conformance/ and docs/logo/ are Apache-2.0; files vendored
# from other projects, under web/admin/static/vendor/, keep their own licence
# and name it; everything else is AGPL-3.0-or-later. See CONTRIBUTING.md.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

status=0
while IFS= read -r -d '' file; do
	case "$file" in
	docs/protocol/* | conformance/* | docs/logo/*) want="Apache-2.0" ;;
	web/admin/static/vendor/*) want="" ;;
	*) want="AGPL-3.0-or-later" ;;
	esac
	# grep reads all five lines rather than stopping at a match (-q), which
	# would cut head off with SIGPIPE on a long line, and pipefail would
	# take that for a missing identifier.
	if ! head -n 5 -- "$file" | grep -E "SPDX-License-Identifier: ${want:-[A-Za-z0-9.+-]+}" >/dev/null; then
		echo "$file: no 'SPDX-License-Identifier: ${want:-<its licence>}' in its first five lines" >&2
		status=1
	fi
done < <(git ls-files -z -- '*.go' '*.js' '*.ts' '*.css' '*.html' '*.sh' '*.yml' '*.yaml' '*.sql' '*Dockerfile')

exit "$status"
