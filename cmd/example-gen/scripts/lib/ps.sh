#!/usr/bin/env bash

# shellcheck source=cmd/example-gen/scripts/lib/util.sh
. "$SCRIPT_DIR/lib/util.sh"

export RESOURCE_PATH="deploy/cr.yaml"

sort_yaml() {
	GENERAL_ORDER='"metadata", "unsafeFlags", "pause", "crVersion", "enableVolumeExpansion", "secretsName", "sslSecretName", "updateStrategy", "upgradeOptions", "initContainer", "ignoreAnnotations", "ignoreLabels", "tls", "mysql", "proxy", "orchestrator", "pmm", "backup", "toolkit"'

	POD_SPEC_ORDER='"size", "image", "imagePullPolicy","imagePullSecrets", "runtimeClassName", "tolerations", "annotations", "labels", "nodeSelector", "priorityClassName", "schedulerName", "serviceAccountName","gracePeriod", "initContainer", "env", "envFrom", "podDisruptionBudget", "resources","startupProbe", "readinessProbe", "livenessProbe", "affinity", "topologySpreadConstraints", "containerSecurityContext", "podSecurityContext"'
	MYSQL_ORDER='"clusterType", "autoRecovery", "vaultSecretName", '"$POD_SPEC_ORDER"',"exposePrimary", "expose", "volumeSpec", "configuration", "sidecars", "sidecarVolumes", "sidecarPVCs"'
	HAPROXY_ORDER='"enabled", "expose", '"$POD_SPEC_ORDER"
	ROUTER_ORDER='"enabled", "expose", '"$POD_SPEC_ORDER"', "ports"'
	ORCHESTRATOR_ORDER='"enabled", "expose", '"$POD_SPEC_ORDER"

	PMM_ORDER='"enabled","image","imagePullPolicy","serverHost","mysqlParams","containerSecurityContext", "resources", "readinessProbes", "livenessProbes"'
	BACKUP_ORDER='"enabled","pitr","sourcePod","image","imagePullPolicy","imagePullSecrets","schedule","backoffLimit", "serviceAccountName", "initContainer", "containerSecurityContext", "resources","storages","pitr"'
	TOOLKIT_ORDER='"image","imagePullPolicy","imagePullSecrets","env","envFrom","resources","containerSecurityContext", "startupProbe", "readinessProbe", "livenessProbe"'

	yq - \
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
	# - removing gcp-cs fields to reduce size
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
		| yq 'del(.spec.backup.storages.gcp-cs.affinity)' \
		| yq 'del(.spec.backup.storages.gcp-cs.azure)' \
		| yq 'del(.spec.backup.storages.gcp-cs.annotations)' \
		| yq 'del(.spec.backup.storages.gcp-cs.labels)' \
		| yq 'del(.spec.backup.storages.gcp-cs.nodeSelector)' \
		| yq 'del(.spec.backup.storages.gcp-cs.priorityClassName)' \
		| yq 'del(.spec.backup.storages.gcp-cs.runtimeClassName)' \
		| yq 'del(.spec.backup.storages.gcp-cs.s3)' \
		| yq 'del(.spec.backup.storages.gcp-cs.schedulerName)' \
		| yq 'del(.spec.backup.storages.gcp-cs.volumeSpec)' \
		| yq 'del(.spec.backup.storages.gcp-cs.verifyTLS)' \
		| yq 'del(.spec.backup.storages.s3-us-west.azure)' \
		| yq 'del(.spec.backup.storages.s3-us-west.gcs)'
}

del_fields_to_comment() {
	yq - \
		| yq "del(.metadata.finalizers[1])" \
		| yq "del(.metadata.finalizers[1])" \
		| yq "del(.spec.metadata)" \
		| yq "del(.spec.unsafeFlags)" \
		| yq "del(.spec.pause)" \
		| yq "del(.spec.enableVolumeExpansion)" \
		| yq "del(.spec.initContainer)" \
		| yq "del(.spec.ignoreAnnotations)" \
		| yq "del(.spec.ignoreLabels)" \
		| yq "del(.spec.tls)" \
		| yq "del(.spec.mysql.runtimeClassName)" \
		| yq "del(.spec.mysql.tolerations)" \
		| yq "del(.spec.mysql.annotations)" \
		| yq "del(.spec.mysql.labels)" \
		| yq "del(.spec.mysql.nodeSelector)" \
		| yq "del(.spec.mysql.priorityClassName)" \
		| yq "del(.spec.mysql.schedulerName)" \
		| yq "del(.spec.mysql.serviceAccountName)" \
		| yq "del(.spec.mysql.imagePullSecrets)" \
		| yq "del(.spec.mysql.initContainer)" \
		| yq "del(.spec.mysql.vaultSecretName)" \
		| yq "del(.spec.mysql.env)" \
		| yq "del(.spec.mysql.envFrom)" \
		| yq "del(.spec.mysql.podDisruptionBudget.minAvailable)" \
		| yq "del(.spec.mysql.startupProbe)" \
		| yq "del(.spec.mysql.readinessProbe)" \
		| yq "del(.spec.mysql.livenessProbe)" \
		| yq "del(.spec.mysql.affinity.advanced)" \
		| yq "del(.spec.mysql.topologySpreadConstraints)" \
		| yq "del(.spec.mysql.expose)" \
		| yq "del(.spec.mysql.exposePrimary.annotations)" \
		| yq "del(.spec.mysql.exposePrimary.labels)" \
		| yq "del(.spec.mysql.exposePrimary.loadBalancerSourceRanges)" \
		| yq "del(.spec.mysql.exposePrimary.type)" \
		| yq "del(.spec.mysql.exposePrimary.internalTrafficPolicy)" \
		| yq "del(.spec.mysql.exposePrimary.externalTrafficPolicy)" \
		| yq "del(.spec.mysql.containerSecurityContext)" \
		| yq "del(.spec.mysql.podSecurityContext)" \
		| yq "del(.spec.mysql.configuration)" \
		| yq "del(.spec.mysql.sidecars)" \
		| yq "del(.spec.mysql.sidecarVolumes)" \
		| yq "del(.spec.mysql.sidecarPVCs)" \
		| yq "del(.spec.mysql.volumeSpec.emptyDir)" \
		| yq "del(.spec.mysql.volumeSpec.hostPath)" \
		| yq "del(.spec.mysql.volumeSpec.persistentVolumeClaim.storageClassName)" \
		| yq "del(.spec.mysql.volumeSpec.persistentVolumeClaim.accessModes)" \
		| yq "del(.spec.proxy.haproxy.runtimeClassName)" \
		| yq "del(.spec.proxy.haproxy.tolerations)" \
		| yq "del(.spec.proxy.haproxy.annotations)" \
		| yq "del(.spec.proxy.haproxy.labels)" \
		| yq "del(.spec.proxy.haproxy.nodeSelector)" \
		| yq "del(.spec.proxy.haproxy.priorityClassName)" \
		| yq "del(.spec.proxy.haproxy.schedulerName)" \
		| yq "del(.spec.proxy.haproxy.serviceAccountName)" \
		| yq "del(.spec.proxy.haproxy.imagePullSecrets)" \
		| yq "del(.spec.proxy.haproxy.podDisruptionBudget.minAvailable)" \
		| yq "del(.spec.proxy.haproxy.resources.limits)" \
		| yq "del(.spec.proxy.haproxy.env)" \
		| yq "del(.spec.proxy.haproxy.envFrom)" \
		| yq "del(.spec.proxy.haproxy.startupProbe)" \
		| yq "del(.spec.proxy.haproxy.readinessProbe)" \
		| yq "del(.spec.proxy.haproxy.livenessProbe)" \
		| yq "del(.spec.proxy.haproxy.affinity.advanced)" \
		| yq "del(.spec.proxy.haproxy.expose)" \
		| yq "del(.spec.proxy.haproxy.topologySpreadConstraints)" \
		| yq "del(.spec.proxy.haproxy.initContainer)" \
		| yq "del(.spec.proxy.haproxy.containerSecurityContext)" \
		| yq "del(.spec.proxy.haproxy.podSecurityContext)" \
		| yq "del(.spec.proxy.haproxy.configuration)" \
		| yq "del(.spec.proxy.router.runtimeClassName)" \
		| yq "del(.spec.proxy.router.tolerations)" \
		| yq "del(.spec.proxy.router.annotations)" \
		| yq "del(.spec.proxy.router.labels)" \
		| yq "del(.spec.proxy.router.nodeSelector)" \
		| yq "del(.spec.proxy.router.priorityClassName)" \
		| yq "del(.spec.proxy.router.schedulerName)" \
		| yq "del(.spec.proxy.router.serviceAccountName)" \
		| yq "del(.spec.proxy.router.imagePullSecrets)" \
		| yq "del(.spec.proxy.router.podDisruptionBudget.minAvailable)" \
		| yq "del(.spec.proxy.router.env)" \
		| yq "del(.spec.proxy.router.envFrom)" \
		| yq "del(.spec.proxy.router.startupProbe)" \
		| yq "del(.spec.proxy.router.readinessProbe)" \
		| yq "del(.spec.proxy.router.livenessProbe)" \
		| yq "del(.spec.proxy.router.affinity.advanced)" \
		| yq "del(.spec.proxy.router.expose)" \
		| yq "del(.spec.proxy.router.topologySpreadConstraints)" \
		| yq "del(.spec.proxy.router.initContainer)" \
		| yq "del(.spec.proxy.router.containerSecurityContext)" \
		| yq "del(.spec.proxy.router.podSecurityContext)" \
		| yq "del(.spec.proxy.router.configuration)" \
		| yq "del(.spec.proxy.router.ports)" \
		| yq "del(.spec.orchestrator.runtimeClassName)" \
		| yq "del(.spec.orchestrator.tolerations)" \
		| yq "del(.spec.orchestrator.annotations)" \
		| yq "del(.spec.orchestrator.labels)" \
		| yq "del(.spec.orchestrator.nodeSelector)" \
		| yq "del(.spec.orchestrator.priorityClassName)" \
		| yq "del(.spec.orchestrator.schedulerName)" \
		| yq "del(.spec.orchestrator.serviceAccountName)" \
		| yq "del(.spec.orchestrator.imagePullSecrets)" \
		| yq "del(.spec.orchestrator.podDisruptionBudget.minAvailable)" \
		| yq "del(.spec.orchestrator.env)" \
		| yq "del(.spec.orchestrator.envFrom)" \
		| yq "del(.spec.orchestrator.startupProbe)" \
		| yq "del(.spec.orchestrator.readinessProbe)" \
		| yq "del(.spec.orchestrator.livenessProbe)" \
		| yq "del(.spec.orchestrator.affinity.advanced)" \
		| yq "del(.spec.orchestrator.expose)" \
		| yq "del(.spec.orchestrator.topologySpreadConstraints)" \
		| yq "del(.spec.orchestrator.initContainer)" \
		| yq "del(.spec.orchestrator.containerSecurityContext)" \
		| yq "del(.spec.orchestrator.podSecurityContext)" \
		| yq "del(.spec.pmm.mysqlParams)" \
		| yq "del(.spec.pmm.readinessProbes)" \
		| yq "del(.spec.pmm.livenessProbes)" \
		| yq "del(.spec.pmm.containerSecurityContext)" \
		| yq "del(.spec.pmm.resources.limits)" \
		| yq "del(.spec.backup.sourcePod)" \
		| yq "del(.spec.backup.schedule)" \
		| yq "del(.spec.backup.backoffLimit)" \
		| yq "del(.spec.backup.imagePullSecrets)" \
		| yq "del(.spec.backup.initContainer)" \
		| yq "del(.spec.backup.containerSecurityContext)" \
		| yq "del(.spec.backup.resources)" \
		| yq "del(.spec.backup.serviceAccountName)" \
		| yq "del(.spec.backup.storages.azure-blob)" \
		| yq "del(.spec.backup.storages.gcp-cs)" \
		| yq "del(.spec.backup.storages.s3-us-west.resources)" \
		| yq "del(.spec.backup.storages.s3-us-west.topologySpreadConstraints)" \
		| yq "del(.spec.backup.storages.s3-us-west.tolerations)" \
		| yq "del(.spec.backup.storages.s3-us-west.containerSecurityContext)" \
		| yq "del(.spec.backup.storages.s3-us-west.labels)" \
		| yq "del(.spec.backup.storages.s3-us-west.nodeSelector)" \
		| yq "del(.spec.backup.storages.s3-us-west.podSecurityContext)" \
		| yq "del(.spec.backup.storages.s3-us-west.priorityClassName)" \
		| yq "del(.spec.backup.storages.s3-us-west.annotations)" \
		| yq "del(.spec.backup.storages.s3-us-west.containerOptions)" \
		| yq "del(.spec.backup.storages.s3-us-west.volumeSpec)" \
		| yq "del(.spec.backup.storages.s3-us-west.affinity)" \
		| yq "del(.spec.backup.storages.s3-us-west.s3.prefix)" \
		| yq "del(.spec.backup.storages.s3-us-west.s3.endpointUrl)" \
		| yq "del(.spec.backup.storages.s3-us-west.schedulerName)" \
		| yq "del(.spec.backup.storages.s3-us-west.runtimeClassName)" \
		| yq "del(.spec.toolkit.imagePullSecrets)" \
		| yq "del(.spec.toolkit.env)" \
		| yq "del(.spec.toolkit.envFrom)" \
		| yq "del(.spec.toolkit.resources)" \
		| yq "del(.spec.toolkit.containerSecurityContext)" \
		| yq "del(.spec.toolkit.startupProbe)" \
		| yq "del(.spec.toolkit.readinessProbe)" \
		| yq "del(.spec.toolkit.livenessProbe)"
}
