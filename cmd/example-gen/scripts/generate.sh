#!/usr/bin/env bash

resource=${1:-}

SCRIPT_DIR="$(dirname ${BASH_SOURCE[0]})"

case "$resource" in
	ps | ps-backup | ps-restore)
		. "$SCRIPT_DIR/lib/$resource.sh"
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
		>$RESOURCE_PATH
