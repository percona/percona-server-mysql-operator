#!/usr/bin/env bash

# shellcheck source=cmd/example-gen/scripts/lib/util.sh
. "$SCRIPT_DIR/lib/util.sh"

export RESOURCE_PATH="deploy/backup/restore.yaml"

sort_yaml() {
	SPEC_ORDER='"clusterName", "backupName", "pitr", "containerOptions", "backupSource"'
	PITR_BACKUP_SOURCE_ORDER='"binlogServer"'
	BINLOG_SERVER_SPEC_ORDER='"size", "image", "serverId", "storage", "connectTimeout", "readTimeout", "writeTimeout", "idleTime", "logLevel"'
	CONTAINER_OPTS_ORDER='"env", "args"'

	yq - \
		| yq '.spec |= pick((['"$SPEC_ORDER"'] + keys) | unique)' \
		| yq '.spec.pitr.backupSource |= pick((['"$PITR_BACKUP_SOURCE_ORDER"'] + keys) | unique)' \
		| yq '.spec.pitr.backupSource.binlogServer |= pick((['"$BINLOG_SERVER_SPEC_ORDER"'] + keys) | unique)' \
		| yq '.spec.containerOptions |= pick((['"$CONTAINER_OPTS_ORDER"'] + keys) | unique)'
}

remove_fields() {
	yq - \
		| yq "del(.status)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.affinity)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.annotations)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.checkpointInterval)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.checkpointSize)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.configuration)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.containerSecurityContext)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.env)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.envFrom)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.gracePeriod)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.imagePullPolicy)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.imagePullSecrets)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.initContainer)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.initImage)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.labels)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.livenessProbe)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.nodeSelector)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.podDisruptionBudget)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.podSecurityContext)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.priorityClassName)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.readinessProbe)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.resources)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.rewriteFileSize)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.runtimeClassName)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.schedulerName)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.serviceAccountName)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.sslMode)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.startupProbe)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.tolerations)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.topologySpreadConstraints)" \
		| yq "del(.spec.pitr.backupSource.binlogServer.verifyChecksum)" \
		| yq "del(.spec.backupSource.backupSource)" \
		| yq "del(.spec.backupSource.completed)" \
		| yq "del(.spec.backupSource.image)" \
		| yq "del(.spec.backupSource.state)" \
		| yq "del(.spec.backupSource.stateDescription)" \
		| yq "del(.spec.backupSource.storage.azure)" \
		| yq "del(.spec.backupSource.storage.gcs)" \
		| yq "del(.spec.backupSource.storage.affinity)" \
		| yq "del(.spec.backupSource.storage.annotations)" \
		| yq "del(.spec.backupSource.storage.containerOptions)" \
		| yq "del(.spec.backupSource.storage.containerSecurityContext)" \
		| yq "del(.spec.backupSource.storage.encryptionKeySecret)" \
		| yq "del(.spec.backupSource.storage.labels)" \
		| yq "del(.spec.backupSource.storage.nodeSelector)" \
		| yq "del(.spec.backupSource.storage.podSecurityContext)" \
		| yq "del(.spec.backupSource.storage.priorityClassName)" \
		| yq "del(.spec.backupSource.storage.resources)" \
		| yq "del(.spec.backupSource.storage.runtimeClassName)" \
		| yq "del(.spec.backupSource.storage.schedulerName)" \
		| yq "del(.spec.backupSource.storage.tolerations)" \
		| yq "del(.spec.backupSource.storage.topologySpreadConstraints)" \
		| yq "del(.spec.backupSource.storage.verifyTLS)" \
		| yq "del(.spec.backupSource.storage.volumeSpec)" \
		| yq "del(.spec.backupSource.type)" \
		| yq "del(.spec.backupSource.conditions)"
}

del_fields_to_comment() {
	yq - \
		| yq "del(.spec.pitr)" \
		| yq "del(.spec.containerOptions)" \
		| yq "del(.spec.backupSource)"
}
