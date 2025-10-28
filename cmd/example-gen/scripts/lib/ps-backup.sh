#!/usr/bin/env bash

. "$SCRIPT_DIR/lib/util.sh"

RESOURCE_PATH="deploy/backup/backup.yaml"

sort_yaml() {
	SPEC_ORDER='"clusterName", "storageName", "sourcePod", "containerOptions"'
	CONTAINER_OPTS_ORDER='"env", "args"'

	yq - "$@" \
		| yq '.spec |= pick((['"$SPEC_ORDER"'] + keys) | unique)' \
		| yq '.spec.containerOptions |= pick((['"$CONTAINER_OPTS_ORDER"'] + keys) | unique)'
}

remove_fields() {
	yq - "$@" \
		| yq "del(.status)"
}

del_fields_to_comment() {
	yq - "$@" \
		| yq "del(.spec.containerOptions)" \
		| yq "del(.spec.sourcePod)"
}
