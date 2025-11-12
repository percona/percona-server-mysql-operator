#!/usr/bin/env bash

resource=${1:-}

SCRIPT_DIR="$(dirname "${BASH_SOURCE[0]}")"

case "$resource" in
	ps)
		# shellcheck source=cmd/example-gen/scripts/lib/ps.sh
		. "$SCRIPT_DIR/lib/ps.sh"
		;;
	ps-backup)
		# shellcheck source=cmd/example-gen/scripts/lib/ps-backup.sh
		. "$SCRIPT_DIR/lib/ps-backup.sh"
		;;
	ps-restore)
		# shellcheck source=cmd/example-gen/scripts/lib/ps-restore.sh
		. "$SCRIPT_DIR/lib/ps-restore.sh"
		;;
	chart)
		# shellcheck source=cmd/example-gen/scripts/lib/chart.sh
		. "$SCRIPT_DIR/lib/chart.sh"

		go run cmd/example-gen/cmd/cr-gen/main.go "ps" \
			| remove_fields \
			| yq 'del(.kind) | del(.apiVersion) | del(.status)' \
			| yq '.finalizers = .metadata.finalizers | del(.metadata)' \
			| yq '. = (. + .spec) | del(.spec)' \
			| yq '.nameOverride = ""' \
			| yq '.fullnameOverride = ""' \
			| replace_image ".mysql" \
			| replace_image ".orchestrator" \
			| replace_image ".proxy.haproxy" \
			| replace_image ".proxy.router" \
			| replace_image ".toolkit" \
			| replace_image ".pmm" \
			| sort_yaml '.' \
			| comment_fields '.' >deploy/chart/values.yaml

		go run cmd/example-gen/cmd/chart-gen/main.go api/v1 >deploy/chart/templates/cluster.yaml
		exit 0
		;;
	*)
		echo "Usage: $0 {ps|ps-backup|ps-restore|chart}" >&2
		exit 2
		;;
esac

go run cmd/example-gen/cmd/cr-gen/main.go "$resource" \
	| sort_yaml \
	| remove_fields \
	| comment_fields \
		>"$RESOURCE_PATH"
