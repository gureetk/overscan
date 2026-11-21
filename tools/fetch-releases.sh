#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Usage: tools/fetch-releases.sh DIR
#
# Downloads the Linux amd64 binary of every full release from v0.3.0 on,
# and of the newest pre-release when it is newer than them all, into DIR as
# overscan-<tag>, each checked against its release's SHA256SUMS. The
# upgrade test (tools/upgradetest) upgrades a household from each (D118).
#
# It needs the GitHub CLI, signed in or given GH_TOKEN, and reads the
# releases of the repository that GITHUB_REPOSITORY names, as GitHub's
# workflows set it, or of gureetk/overscan.
set -euo pipefail

dir="${1:?usage: $0 DIR}"
repo="${GITHUB_REPOSITORY:-gureetk/overscan}"
first=0.3.0 # the first full release that 1.0 upgrades from directly
mkdir -p "$dir"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# newer A B: whether version A comes after version B, both without "v" and
# without a pre-release's suffix.
newer() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1)" = "$1" ]; }

# Published releases, newest first, each with whether it is a pre-release.
gh api --paginate "repos/$repo/releases?per_page=100" \
	--jq '.[] | select(.draft | not) | "\(.tag_name) \(.prerelease)"' >"$work/releases"

tags=()
latest=""
while read -r tag pre; do
	v=${tag#v}
	if [ "$pre" = false ] && { [ "$v" = "$first" ] || newer "$v" "$first"; }; then
		tags+=("$tag")
		if [ -z "$latest" ] || newer "$v" "$latest"; then
			latest=$v
		fi
	fi
done <"$work/releases"
if [ "${#tags[@]}" -eq 0 ]; then
	echo "fetch-releases.sh: $repo has no full release from v$first on" >&2
	exit 1
fi
pre=$(awk '$2 == "true" {print $1; exit}' "$work/releases")
if [ -n "$pre" ]; then
	base=${pre#v}
	base=${base%%-*}
	if newer "$base" "$latest"; then
		tags+=("$pre")
	fi
fi

for tag in "${tags[@]}"; do
	v=${tag#v}
	archive="overscan_${v}_linux_amd64.tar.gz"
	gh api "repos/$repo/releases/tags/$tag" --jq '.assets[] | "\(.id) \(.name)"' >"$work/assets"
	for name in "$archive" SHA256SUMS; do
		id=$(awk -v name="$name" '$2 == name {print $1}' "$work/assets")
		if [ -z "$id" ]; then
			echo "fetch-releases.sh: $tag has no $name" >&2
			exit 1
		fi
		gh api -H "Accept: application/octet-stream" "repos/$repo/releases/assets/$id" >"$work/$name"
	done
	(cd "$work" && sha256sum --quiet -c --ignore-missing SHA256SUMS)
	tar -C "$work" -xzf "$work/$archive"
	install -m 0755 "$work/overscan_${v}_linux_amd64/overscan" "$dir/overscan-$tag"
	rm -rf "${work:?}/$archive" "$work/SHA256SUMS" "$work/overscan_${v}_linux_amd64"
	echo "$tag: $("$dir/overscan-$tag" -version)"
done
