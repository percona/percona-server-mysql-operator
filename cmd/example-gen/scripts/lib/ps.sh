#!/usr/bin/env bash

# shellcheck source=cmd/example-gen/scripts/lib/util.sh
. "$SCRIPT_DIR/lib/util.sh"

export RESOURCE_PATH="deploy/cr.yaml"

sort_yaml() {
	local prefix="${1:-.spec}"

	GENERAL_ORDER='"nameOverride", "fullnameOverride", "finalizers", "metadata", "unsafeFlags", "pause", "crVersion", "enableVolumeExpansion", "secretsName", "sslSecretName", "updateStrategy", "upgradeOptions", "initContainer", "ignoreAnnotations", "ignoreLabels", "tls", "mysql", "proxy", "orchestrator", "pmm", "backup", "toolkit"'

	POD_SPEC_ORDER='"size", "image", "imagePullPolicy","imagePullSecrets", "runtimeClassName", "tolerations", "annotations", "labels", "nodeSelector", "priorityClassName", "schedulerName", "serviceAccountName","gracePeriod", "initContainer", "env", "envFrom", "podDisruptionBudget", "resources","startupProbe", "readinessProbe", "livenessProbe", "affinity", "topologySpreadConstraints", "containerSecurityContext", "podSecurityContext"'
	MYSQL_ORDER='"clusterType", "autoRecovery", "vaultSecretName", '"$POD_SPEC_ORDER"',"exposePrimary", "expose", "volumeSpec", "configuration", "sidecars", "sidecarVolumes", "sidecarPVCs"'
	HAPROXY_ORDER='"enabled", "expose", '"$POD_SPEC_ORDER"
	ROUTER_ORDER='"enabled", "expose", '"$POD_SPEC_ORDER"', "ports"'
	ORCHESTRATOR_ORDER='"enabled", "expose", '"$POD_SPEC_ORDER"

	PMM_ORDER='"enabled","image","imagePullPolicy","serverHost","mysqlParams","containerSecurityContext", "resources", "readinessProbes", "livenessProbes"'
	BACKUP_ORDER='"enabled","pitr","sourcePod","image","imagePullPolicy","imagePullSecrets","schedule","backoffLimit", "serviceAccountName", "initContainer", "containerSecurityContext", "resources","storages","pitr"'
	TOOLKIT_ORDER='"image","imagePullPolicy","imagePullSecrets","env","envFrom","resources","containerSecurityContext", "startupProbe", "readinessProbe", "livenessProbe"'

	local base_path=$prefix

	if [[ $prefix == '.' ]]; then
		prefix=''
	fi

	yq - \
		| yq "$base_path"' |= pick((['"$GENERAL_ORDER"'] + keys) | unique)' \
		| yq "$prefix"'.mysql |= pick((['"$MYSQL_ORDER"'] + keys) | unique)' \
		| yq "$prefix"'.proxy.haproxy |= pick((['"$HAPROXY_ORDER"'] + keys) | unique)' \
		| yq "$prefix"'.proxy.router |= pick((['"$ROUTER_ORDER"'] + keys) | unique)' \
		| yq "$prefix"'.orchestrator |= pick((['"$ORCHESTRATOR_ORDER"'] + keys) | unique)' \
		| yq "$prefix"'.pmm |= pick((['"$PMM_ORDER"'] + keys) | unique)' \
		| yq "$prefix"'.backup |= pick((['"$BACKUP_ORDER"'] + keys) | unique)' \
		| yq "$prefix"'.toolkit |= pick((['"$TOOLKIT_ORDER"'] + keys) | unique)'
}

remove_fields() {
	# - removing initImage as it is deprecated
	# - removing binlogServer is not used
	# - removing azure-blob fields to reduce size
	# - removing non-s3 fields in s3-us-west
	yq - \
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

del_fields_to_comment() {
	local prefix="${1:-.spec}"

	local metadata_path=".metadata"

	if [[ $prefix == '.' ]]; then
		metadata_path=''
		prefix=''
	fi

	yq - \
		| yq "del($metadata_path.finalizers[1])" \
		| yq "del($metadata_path.finalizers[1])" \
		| yq "del($prefix.metadata)" \
		| yq "del($prefix.unsafeFlags)" \
		| yq "del($prefix.pause)" \
		| yq "del($prefix.enableVolumeExpansion)" \
		| yq "del($prefix.initContainer)" \
		| yq "del($prefix.ignoreAnnotations)" \
		| yq "del($prefix.ignoreLabels)" \
		| yq "del($prefix.tls)" \
		| yq "del($prefix.mysql.runtimeClassName)" \
		| yq "del($prefix.mysql.tolerations)" \
		| yq "del($prefix.mysql.annotations)" \
		| yq "del($prefix.mysql.labels)" \
		| yq "del($prefix.mysql.nodeSelector)" \
		| yq "del($prefix.mysql.priorityClassName)" \
		| yq "del($prefix.mysql.schedulerName)" \
		| yq "del($prefix.mysql.serviceAccountName)" \
		| yq "del($prefix.mysql.imagePullSecrets)" \
		| yq "del($prefix.mysql.initContainer)" \
		| yq "del($prefix.mysql.vaultSecretName)" \
		| yq "del($prefix.mysql.env)" \
		| yq "del($prefix.mysql.envFrom)" \
		| yq "del($prefix.mysql.podDisruptionBudget.minAvailable)" \
		| yq "del($prefix.mysql.startupProbe)" \
		| yq "del($prefix.mysql.readinessProbe)" \
		| yq "del($prefix.mysql.livenessProbe)" \
		| yq "del($prefix.mysql.affinity.advanced)" \
		| yq "del($prefix.mysql.topologySpreadConstraints)" \
		| yq "del($prefix.mysql.expose)" \
		| yq "del($prefix.mysql.exposePrimary.annotations)" \
		| yq "del($prefix.mysql.exposePrimary.labels)" \
		| yq "del($prefix.mysql.exposePrimary.loadBalancerSourceRanges)" \
		| yq "del($prefix.mysql.exposePrimary.type)" \
		| yq "del($prefix.mysql.exposePrimary.internalTrafficPolicy)" \
		| yq "del($prefix.mysql.exposePrimary.externalTrafficPolicy)" \
		| yq "del($prefix.mysql.containerSecurityContext)" \
		| yq "del($prefix.mysql.podSecurityContext)" \
		| yq "del($prefix.mysql.configuration)" \
		| yq "del($prefix.mysql.sidecars)" \
		| yq "del($prefix.mysql.sidecarVolumes)" \
		| yq "del($prefix.mysql.sidecarPVCs)" \
		| yq "del($prefix.mysql.volumeSpec.emptyDir)" \
		| yq "del($prefix.mysql.volumeSpec.hostPath)" \
		| yq "del($prefix.mysql.volumeSpec.persistentVolumeClaim.storageClassName)" \
		| yq "del($prefix.mysql.volumeSpec.persistentVolumeClaim.accessModes)" \
		| yq "del($prefix.proxy.haproxy.runtimeClassName)" \
		| yq "del($prefix.proxy.haproxy.tolerations)" \
		| yq "del($prefix.proxy.haproxy.annotations)" \
		| yq "del($prefix.proxy.haproxy.labels)" \
		| yq "del($prefix.proxy.haproxy.nodeSelector)" \
		| yq "del($prefix.proxy.haproxy.priorityClassName)" \
		| yq "del($prefix.proxy.haproxy.schedulerName)" \
		| yq "del($prefix.proxy.haproxy.serviceAccountName)" \
		| yq "del($prefix.proxy.haproxy.imagePullSecrets)" \
		| yq "del($prefix.proxy.haproxy.podDisruptionBudget.minAvailable)" \
		| yq "del($prefix.proxy.haproxy.resources.limits)" \
		| yq "del($prefix.proxy.haproxy.env)" \
		| yq "del($prefix.proxy.haproxy.envFrom)" \
		| yq "del($prefix.proxy.haproxy.startupProbe)" \
		| yq "del($prefix.proxy.haproxy.readinessProbe)" \
		| yq "del($prefix.proxy.haproxy.livenessProbe)" \
		| yq "del($prefix.proxy.haproxy.affinity.advanced)" \
		| yq "del($prefix.proxy.haproxy.expose)" \
		| yq "del($prefix.proxy.haproxy.topologySpreadConstraints)" \
		| yq "del($prefix.proxy.haproxy.initContainer)" \
		| yq "del($prefix.proxy.haproxy.containerSecurityContext)" \
		| yq "del($prefix.proxy.haproxy.podSecurityContext)" \
		| yq "del($prefix.proxy.haproxy.configuration)" \
		| yq "del($prefix.proxy.router.runtimeClassName)" \
		| yq "del($prefix.proxy.router.tolerations)" \
		| yq "del($prefix.proxy.router.annotations)" \
		| yq "del($prefix.proxy.router.labels)" \
		| yq "del($prefix.proxy.router.nodeSelector)" \
		| yq "del($prefix.proxy.router.priorityClassName)" \
		| yq "del($prefix.proxy.router.schedulerName)" \
		| yq "del($prefix.proxy.router.serviceAccountName)" \
		| yq "del($prefix.proxy.router.imagePullSecrets)" \
		| yq "del($prefix.proxy.router.podDisruptionBudget.minAvailable)" \
		| yq "del($prefix.proxy.router.env)" \
		| yq "del($prefix.proxy.router.envFrom)" \
		| yq "del($prefix.proxy.router.startupProbe)" \
		| yq "del($prefix.proxy.router.readinessProbe)" \
		| yq "del($prefix.proxy.router.livenessProbe)" \
		| yq "del($prefix.proxy.router.affinity.advanced)" \
		| yq "del($prefix.proxy.router.expose)" \
		| yq "del($prefix.proxy.router.topologySpreadConstraints)" \
		| yq "del($prefix.proxy.router.initContainer)" \
		| yq "del($prefix.proxy.router.containerSecurityContext)" \
		| yq "del($prefix.proxy.router.podSecurityContext)" \
		| yq "del($prefix.proxy.router.configuration)" \
		| yq "del($prefix.proxy.router.ports)" \
		| yq "del($prefix.orchestrator.runtimeClassName)" \
		| yq "del($prefix.orchestrator.tolerations)" \
		| yq "del($prefix.orchestrator.annotations)" \
		| yq "del($prefix.orchestrator.labels)" \
		| yq "del($prefix.orchestrator.nodeSelector)" \
		| yq "del($prefix.orchestrator.priorityClassName)" \
		| yq "del($prefix.orchestrator.schedulerName)" \
		| yq "del($prefix.orchestrator.serviceAccountName)" \
		| yq "del($prefix.orchestrator.imagePullSecrets)" \
		| yq "del($prefix.orchestrator.podDisruptionBudget.minAvailable)" \
		| yq "del($prefix.orchestrator.env)" \
		| yq "del($prefix.orchestrator.envFrom)" \
		| yq "del($prefix.orchestrator.startupProbe)" \
		| yq "del($prefix.orchestrator.readinessProbe)" \
		| yq "del($prefix.orchestrator.livenessProbe)" \
		| yq "del($prefix.orchestrator.affinity.advanced)" \
		| yq "del($prefix.orchestrator.expose)" \
		| yq "del($prefix.orchestrator.topologySpreadConstraints)" \
		| yq "del($prefix.orchestrator.initContainer)" \
		| yq "del($prefix.orchestrator.containerSecurityContext)" \
		| yq "del($prefix.orchestrator.podSecurityContext)" \
		| yq "del($prefix.pmm.mysqlParams)" \
		| yq "del($prefix.pmm.readinessProbes)" \
		| yq "del($prefix.pmm.livenessProbes)" \
		| yq "del($prefix.pmm.containerSecurityContext)" \
		| yq "del($prefix.pmm.resources.limits)" \
		| yq "del($prefix.backup.sourcePod)" \
		| yq "del($prefix.backup.schedule)" \
		| yq "del($prefix.backup.backoffLimit)" \
		| yq "del($prefix.backup.imagePullSecrets)" \
		| yq "del($prefix.backup.initContainer)" \
		| yq "del($prefix.backup.containerSecurityContext)" \
		| yq "del($prefix.backup.resources)" \
		| yq "del($prefix.backup.serviceAccountName)" \
		| yq "del($prefix.backup.storages.azure-blob)" \
		| yq "del($prefix.backup.storages.s3-us-west.resources)" \
		| yq "del($prefix.backup.storages.s3-us-west.topologySpreadConstraints)" \
		| yq "del($prefix.backup.storages.s3-us-west.tolerations)" \
		| yq "del($prefix.backup.storages.s3-us-west.containerSecurityContext)" \
		| yq "del($prefix.backup.storages.s3-us-west.labels)" \
		| yq "del($prefix.backup.storages.s3-us-west.nodeSelector)" \
		| yq "del($prefix.backup.storages.s3-us-west.podSecurityContext)" \
		| yq "del($prefix.backup.storages.s3-us-west.priorityClassName)" \
		| yq "del($prefix.backup.storages.s3-us-west.annotations)" \
		| yq "del($prefix.backup.storages.s3-us-west.containerOptions)" \
		| yq "del($prefix.backup.storages.s3-us-west.volumeSpec)" \
		| yq "del($prefix.backup.storages.s3-us-west.affinity)" \
		| yq "del($prefix.backup.storages.s3-us-west.s3.prefix)" \
		| yq "del($prefix.backup.storages.s3-us-west.s3.endpointUrl)" \
		| yq "del($prefix.backup.storages.s3-us-west.schedulerName)" \
		| yq "del($prefix.backup.storages.s3-us-west.runtimeClassName)" \
		| yq "del($prefix.toolkit.imagePullSecrets)" \
		| yq "del($prefix.toolkit.env)" \
		| yq "del($prefix.toolkit.envFrom)" \
		| yq "del($prefix.toolkit.resources)" \
		| yq "del($prefix.toolkit.containerSecurityContext)" \
		| yq "del($prefix.toolkit.startupProbe)" \
		| yq "del($prefix.toolkit.readinessProbe)" \
		| yq "del($prefix.toolkit.livenessProbe)"
}
