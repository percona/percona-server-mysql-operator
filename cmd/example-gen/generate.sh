#!/usr/bin/env bash

sort_cr_yaml() {
	GENERAL_ORDER='"metadata", "unsafeFlags", "pause", "crVersion", "enableVolumeExpansion", "secretsName", "sslSecretName", "updateStrategy", "upgradeOptions", "initContainer", "ignoreAnnotations", "ignoreLabels", "tls", "mysql", "proxy", "orchestrator", "pmm", "backup", "toolkit"'

	POD_SPEC_ORDER='"size", "image", "imagePullPolicy","imagePullSecrets", "runtimeClassName", "tolerations", "annotations", "labels", "nodeSelector", "priorityClassName", "schedulerName", "serviceAccountName","gracePeriod", "initContainer", "env", "envFrom", "podDisruptionBudget", "resources","startupProbe", "readinessProbe", "livenessProbe", "affinity", "topologySpreadConstraints", "containerSecurityContext", "podSecurityContext"'
	MYSQL_ORDER='"clusterType", "autoRecovery", "vaultSecretName", '"$POD_SPEC_ORDER"',"exposePrimary", "expose", "volumeSpec", "configuration", "sidecars", "sidecarVolumes", "sidecarPVCs"'
	HAPROXY_ORDER='"enabled", "expose", '"$POD_SPEC_ORDER"
	ROUTER_ORDER='"enabled", "expose", '"$POD_SPEC_ORDER"', "ports"'
	ORCHESTRATOR_ORDER='"enabled", "expose", '"$POD_SPEC_ORDER"

	PMM_ORDER='"enabled","image","imagePullPolicy","serverHost","mysqlParams","containerSecurityContext", "resources", "readinessProbes", "livenessProbes"'
	BACKUP_ORDER='"enabled","pitr","sourcePod","image","imagePullPolicy","imagePullSecrets","schedule","backoffLimit", "serviceAccountName", "initContainer", "containerSecurityContext", "resources","storages","pitr"'
	TOOLKIT_ORDER='"image","imagePullPolicy","imagePullSecrets","env","envFrom","resources","containerSecurityContext", "startupProbe", "readinessProbe", "livenessProbe"'

	yq - "$@" \
		| yq '.spec |= pick((['"$GENERAL_ORDER"'] + keys) | unique)' \
		| yq '.spec.mysql |= pick((['"$MYSQL_ORDER"'] + keys) | unique)' \
		| yq '.spec.proxy.haproxy |= pick((['"$HAPROXY_ORDER"'] + keys) | unique)' \
		| yq '.spec.proxy.router |= pick((['"$ROUTER_ORDER"'] + keys) | unique)' \
		| yq '.spec.orchestrator |= pick((['"$ORCHESTRATOR_ORDER"'] + keys) | unique)' \
		| yq '.spec.pmm |= pick((['"$PMM_ORDER"'] + keys) | unique)' \
		| yq '.spec.backup |= pick((['"$BACKUP_ORDER"'] + keys) | unique)' \
		| yq '.spec.toolkit |= pick((['"$TOOLKIT_ORDER"'] + keys) | unique)'
}

remove_fields() {
	# - removing initImage as it is deprecated
	# - removing binlogServer is not used
	# - removing azure-blob fields to reduce size
	# - removing non-s3 fields in s3-us-west
	yq - "$@" \
		| yq 'del(.status)' \
		| yq 'del(.spec.backup.initImage)' \
		| yq 'del(.spec.initImage)' \
		| yq 'del(.spec.mysql.initImage)' \
		| yq 'del(.spec.orchestrator.initImage)' \
		| yq 'del(.spec.proxy.haproxy.initImage)' \
		| yq 'del(.spec.proxy.router.initImage)' \
		| yq 'del(.spec.backup.pitr.binlogServer)' \
		| yq 'del(.spec.backup.storages.azure-blob.affinity)' \
		| yq 'del(.spec.backup.storages.azure-blob.annotations)' \
		| yq 'del(.spec.backup.storages.azure-blob.gcs)' \
		| yq 'del(.spec.backup.storages.azure-blob.labels)' \
		| yq 'del(.spec.backup.storages.azure-blob.nodeSelector)' \
		| yq 'del(.spec.backup.storages.azure-blob.priorityClassName)' \
		| yq 'del(.spec.backup.storages.azure-blob.runtimeClassName)' \
		| yq 'del(.spec.backup.storages.azure-blob.s3)' \
		| yq 'del(.spec.backup.storages.azure-blob.schedulerName)' \
		| yq 'del(.spec.backup.storages.azure-blob.volumeSpec)' \
		| yq 'del(.spec.backup.storages.s3-us-west.azure)' \
		| yq 'del(.spec.backup.storages.s3-us-west.gcs)'
}

comment_field() {
	local field_path="$1"
	shift || true

	yq - "$@" \
		| yq "($field_path | key) head_comment = (($field_path | key) as \$key | (($field_path | parent) | pick([\$key])) | to_yaml)" \
		| yq "$field_path=null" \
		| yq "$field_path=\"SEDSHOULDDELETEIT\"" \
		| sed '/SEDSHOULDDELETEIT/d'
}

comment_arr_value() {
	local value_path="$1"
	shift || true

	query='
('$value_path' | parent | key) foot_comment =
(
  ('$value_path' | parent | key | foot_comment) as $existing_comment |
    (($existing_comment | select(length > 1) | from_yaml) // []) + ['$value_path'] as $resulting_list |
    {"SEDSHOULDDELETEIT": $resulting_list}
    | to_yaml
)
'

	yq - "$@" \
		| yq "$query" \
		| yq "$value_path=\"SEDSHOULDDELETEIT\"" \
		| sed '/SEDSHOULDDELETEIT/d'
}

normalize_comments() {
	sed -r 's/^([[:space:]]*)# (.*)$/#\1\2/' - "$@"
}

# TODO: Refactor this function. We should introduce an allowlist of fields to keep uncommented and comment everything else.
comment_cr_yaml() {
	yq - "$@" \
		| comment_arr_value ".metadata.finalizers[1]" \
		| comment_arr_value ".metadata.finalizers[1]" \
		| comment_field ".spec.metadata" \
		| comment_field ".spec.unsafeFlags" \
		| comment_field ".spec.pause" \
		| comment_field ".spec.enableVolumeExpansion" \
		| comment_field ".spec.initContainer" \
		| comment_field ".spec.ignoreAnnotations" \
		| comment_field ".spec.ignoreLabels" \
		| comment_field ".spec.tls" \
		| comment_field ".spec.mysql.runtimeClassName" \
		| comment_field ".spec.mysql.tolerations" \
		| comment_field ".spec.mysql.annotations" \
		| comment_field ".spec.mysql.labels" \
		| comment_field ".spec.mysql.nodeSelector" \
		| comment_field ".spec.mysql.priorityClassName" \
		| comment_field ".spec.mysql.schedulerName" \
		| comment_field ".spec.mysql.serviceAccountName" \
		| comment_field ".spec.mysql.imagePullSecrets" \
		| comment_field ".spec.mysql.initContainer" \
		| comment_field ".spec.mysql.vaultSecretName" \
		| comment_field ".spec.mysql.env" \
		| comment_field ".spec.mysql.envFrom" \
		| comment_field ".spec.mysql.podDisruptionBudget.minAvailable" \
		| comment_field ".spec.mysql.startupProbe" \
		| comment_field ".spec.mysql.readinessProbe" \
		| comment_field ".spec.mysql.livenessProbe" \
		| comment_field ".spec.mysql.affinity.advanced" \
		| comment_field ".spec.mysql.topologySpreadConstraints" \
		| comment_field ".spec.mysql.expose" \
		| comment_field ".spec.mysql.exposePrimary.annotations" \
		| comment_field ".spec.mysql.exposePrimary.labels" \
		| comment_field ".spec.mysql.exposePrimary.loadBalancerSourceRanges" \
		| comment_field ".spec.mysql.exposePrimary.type" \
		| comment_field ".spec.mysql.exposePrimary.internalTrafficPolicy" \
		| comment_field ".spec.mysql.exposePrimary.externalTrafficPolicy" \
		| comment_field ".spec.mysql.containerSecurityContext" \
		| comment_field ".spec.mysql.podSecurityContext" \
		| comment_field ".spec.mysql.configuration" \
		| comment_field ".spec.mysql.sidecars" \
		| comment_field ".spec.mysql.sidecarVolumes" \
		| comment_field ".spec.mysql.sidecarPVCs" \
		| comment_field ".spec.mysql.volumeSpec.emptyDir" \
		| comment_field ".spec.mysql.volumeSpec.hostPath" \
		| comment_field ".spec.mysql.volumeSpec.persistentVolumeClaim.storageClassName" \
		| comment_field ".spec.mysql.volumeSpec.persistentVolumeClaim.accessModes" \
		| comment_field ".spec.proxy.haproxy.runtimeClassName" \
		| comment_field ".spec.proxy.haproxy.tolerations" \
		| comment_field ".spec.proxy.haproxy.annotations" \
		| comment_field ".spec.proxy.haproxy.labels" \
		| comment_field ".spec.proxy.haproxy.nodeSelector" \
		| comment_field ".spec.proxy.haproxy.priorityClassName" \
		| comment_field ".spec.proxy.haproxy.schedulerName" \
		| comment_field ".spec.proxy.haproxy.serviceAccountName" \
		| comment_field ".spec.proxy.haproxy.imagePullSecrets" \
		| comment_field ".spec.proxy.haproxy.podDisruptionBudget.minAvailable" \
		| comment_field ".spec.proxy.haproxy.resources.limits" \
		| comment_field ".spec.proxy.haproxy.env" \
		| comment_field ".spec.proxy.haproxy.envFrom" \
		| comment_field ".spec.proxy.haproxy.startupProbe" \
		| comment_field ".spec.proxy.haproxy.readinessProbe" \
		| comment_field ".spec.proxy.haproxy.livenessProbe" \
		| comment_field ".spec.proxy.haproxy.affinity.advanced" \
		| comment_field ".spec.proxy.haproxy.expose" \
		| comment_field ".spec.proxy.haproxy.topologySpreadConstraints" \
		| comment_field ".spec.proxy.haproxy.initContainer" \
		| comment_field ".spec.proxy.haproxy.containerSecurityContext" \
		| comment_field ".spec.proxy.haproxy.podSecurityContext" \
		| comment_field ".spec.proxy.haproxy.configuration" \
		| comment_field ".spec.proxy.router.runtimeClassName" \
		| comment_field ".spec.proxy.router.tolerations" \
		| comment_field ".spec.proxy.router.annotations" \
		| comment_field ".spec.proxy.router.labels" \
		| comment_field ".spec.proxy.router.nodeSelector" \
		| comment_field ".spec.proxy.router.priorityClassName" \
		| comment_field ".spec.proxy.router.schedulerName" \
		| comment_field ".spec.proxy.router.serviceAccountName" \
		| comment_field ".spec.proxy.router.imagePullSecrets" \
		| comment_field ".spec.proxy.router.podDisruptionBudget.minAvailable" \
		| comment_field ".spec.proxy.router.resources.limits" \
		| comment_field ".spec.proxy.router.env" \
		| comment_field ".spec.proxy.router.envFrom" \
		| comment_field ".spec.proxy.router.startupProbe" \
		| comment_field ".spec.proxy.router.readinessProbe" \
		| comment_field ".spec.proxy.router.livenessProbe" \
		| comment_field ".spec.proxy.router.affinity.advanced" \
		| comment_field ".spec.proxy.router.expose" \
		| comment_field ".spec.proxy.router.topologySpreadConstraints" \
		| comment_field ".spec.proxy.router.initContainer" \
		| comment_field ".spec.proxy.router.containerSecurityContext" \
		| comment_field ".spec.proxy.router.podSecurityContext" \
		| comment_field ".spec.proxy.router.configuration" \
		| comment_field ".spec.proxy.router.ports" \
		| comment_field ".spec.orchestrator.runtimeClassName" \
		| comment_field ".spec.orchestrator.tolerations" \
		| comment_field ".spec.orchestrator.annotations" \
		| comment_field ".spec.orchestrator.labels" \
		| comment_field ".spec.orchestrator.nodeSelector" \
		| comment_field ".spec.orchestrator.priorityClassName" \
		| comment_field ".spec.orchestrator.schedulerName" \
		| comment_field ".spec.orchestrator.serviceAccountName" \
		| comment_field ".spec.orchestrator.imagePullSecrets" \
		| comment_field ".spec.orchestrator.podDisruptionBudget.minAvailable" \
		| comment_field ".spec.orchestrator.resources.limits" \
		| comment_field ".spec.orchestrator.env" \
		| comment_field ".spec.orchestrator.envFrom" \
		| comment_field ".spec.orchestrator.startupProbe" \
		| comment_field ".spec.orchestrator.readinessProbe" \
		| comment_field ".spec.orchestrator.livenessProbe" \
		| comment_field ".spec.orchestrator.affinity.advanced" \
		| comment_field ".spec.orchestrator.expose" \
		| comment_field ".spec.orchestrator.topologySpreadConstraints" \
		| comment_field ".spec.orchestrator.initContainer" \
		| comment_field ".spec.orchestrator.containerSecurityContext" \
		| comment_field ".spec.orchestrator.podSecurityContext" \
		| comment_field ".spec.pmm.mysqlParams" \
		| comment_field ".spec.pmm.readinessProbes" \
		| comment_field ".spec.pmm.livenessProbes" \
		| comment_field ".spec.pmm.containerSecurityContext" \
		| comment_field ".spec.backup.sourcePod" \
		| comment_field ".spec.backup.schedule" \
		| comment_field ".spec.backup.backoffLimit" \
		| comment_field ".spec.backup.imagePullSecrets" \
		| comment_field ".spec.backup.initContainer" \
		| comment_field ".spec.backup.containerSecurityContext" \
		| comment_field ".spec.backup.storages.azure-blob" \
		| comment_field ".spec.backup.storages.s3-us-west.resources" \
		| comment_field ".spec.backup.storages.s3-us-west.topologySpreadConstraints" \
		| comment_field ".spec.backup.storages.s3-us-west.tolerations" \
		| comment_field ".spec.backup.storages.s3-us-west.containerSecurityContext" \
		| comment_field ".spec.backup.storages.s3-us-west.labels" \
		| comment_field ".spec.backup.storages.s3-us-west.nodeSelector" \
		| comment_field ".spec.backup.storages.s3-us-west.podSecurityContext" \
		| comment_field ".spec.backup.storages.s3-us-west.priorityClassName" \
		| comment_field ".spec.backup.storages.s3-us-west.annotations" \
		| comment_field ".spec.backup.storages.s3-us-west.containerOptions" \
		| comment_field ".spec.backup.storages.s3-us-west.volumeSpec" \
		| comment_field ".spec.toolkit.imagePullSecrets" \
		| comment_field ".spec.toolkit.env" \
		| comment_field ".spec.toolkit.envFrom" \
		| comment_field ".spec.toolkit.resources" \
		| comment_field ".spec.toolkit.containerSecurityContext" \
		| comment_field ".spec.toolkit.startupProbe" \
		| comment_field ".spec.toolkit.readinessProbe" \
		| comment_field ".spec.toolkit.livenessProbe" \
		| normalize_comments
}

yq --version

go run cmd/example-gen/main.go \
	| sort_cr_yaml \
	| remove_fields \
	| comment_cr_yaml \
	| cat >deploy/cr.yaml
