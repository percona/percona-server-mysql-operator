#!/usr/bin/env bash

# shellcheck source=cmd/example-gen/scripts/lib/util.sh
. "$SCRIPT_DIR/lib/util.sh"

export RESOURCE_PATH="deploy/backup/restore.yaml"

sort_yaml() {
	SPEC_ORDER='"clusterName", "backupName", "containerOptions", "backupSource"'
	CONTAINER_OPTS_ORDER='"env", "args"'

	yq - \
		| yq '.spec |= pick((['"$SPEC_ORDER"'] + keys) | unique)' \
		| yq '.spec.containerOptions |= pick((['"$CONTAINER_OPTS_ORDER"'] + keys) | unique)'
}

remove_fields() {
	yq - \
		| yq "del(.status)" \
		| yq "del(.spec.backupSource.backupSource)" \
		| yq "del(.spec.backupSource.completed)" \
		| yq "del(.spec.backupSource.image)" \
		| yq "del(.spec.backupSource.state)" \
		| yq "del(.spec.backupSource.stateDescription)" \
		| yq "del(.spec.backupSource.storage.azure)" \
		| yq "del(.spec.backupSource.storage.gcs)"
}

del_fields_to_comment() {
	yq - \
		| yq "del(.spec.containerOptions)" \
		| yq "del(.spec.backupSource)"
}
