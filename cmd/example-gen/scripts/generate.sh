#!/usr/bin/env bash

resource=${1:-}

SCRIPT_DIR="$(dirname "${BASH_SOURCE[0]}")"

case "$resource" in
	ps)
		# shellcheck source=lib/ps.sh
		. "$SCRIPT_DIR/lib/ps.sh"
		;;
	ps-backup)
		# shellcheck source=lib/ps-backup.sh
		. "$SCRIPT_DIR/lib/ps-backup.sh"
		;;
	ps-restore)
		# shellcheck source=lib/ps-restore.sh
		. "$SCRIPT_DIR/lib/ps-restore.sh"
		;;
	*)
		echo "Usage: $0 {ps|ps-backup|ps-restore}" >&2
		exit 2
		;;
esac

go run cmd/example-gen/main.go "$resource" \
	| sort_yaml \
	| remove_fields \
	| comment_fields \
		>"$RESOURCE_PATH"
