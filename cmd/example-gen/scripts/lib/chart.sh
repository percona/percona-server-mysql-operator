#!/usr/bin/env bash

# shellcheck source=cmd/example-gen/scripts/lib/ps.sh
. "$SCRIPT_DIR/lib/ps.sh"

replace_image() {
	local path=$1

	local image
	local repository
	local tag

	tmp="$(mktemp)"
	cat >"$tmp"

	image=$(yq "$path.image" "$tmp")

	tag="${image##*:}"
	repository="${image%:*}"

	yq "del($path.image) | $path.image.repository=\"$repository\" | $path.image.tag=\"$tag\"" "$tmp"

	rm -f "$tmp"
}
