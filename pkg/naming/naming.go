package naming

const (
	perconaPrefix      = "percona.com/"
	mysqlPerconaPrefix = "mysql.percona.com/"
)

const (
	LabelCluster = perconaPrefix + "cluster"
)

const (
	LabelMySQLPrimary = mysqlPerconaPrefix + "primary"
	LabelExposed      = perconaPrefix + "exposed"
)

const (
	LabelBackupType     = perconaPrefix + "backup-type"
	LabelBackupAncestor = perconaPrefix + "backup-ancestor"
)

const (
	FinalizerDeleteSSL            = perconaPrefix + "delete-ssl"
	FinalizerDeletePodsInOrder    = perconaPrefix + "delete-mysql-pods-in-order"
	FinalizerDeleteBackup         = perconaPrefix + "delete-backup"
	FinalizerDeleteMySQLPvc       = perconaPrefix + "delete-mysql-pvc"
	FinalizerClusterSetProtection = perconaPrefix + "clusterset-protection"
)

const (
	FinalizerClusterSetDissolve = perconaPrefix + "clusterset-dissolve"
)

type AnnotationKey string

func (s AnnotationKey) String() string {
	return string(s)
}

const (
	AnnotationSecretHash               AnnotationKey = perconaPrefix + "last-applied-secret"
	AnnotationConfigHash               AnnotationKey = perconaPrefix + "configuration-hash"
	AnnotationTLSHash                  AnnotationKey = perconaPrefix + "last-applied-tls"
	AnnotationPasswordsUpdated         AnnotationKey = perconaPrefix + "passwords-updated"
	AnnotationLastConfigHash           AnnotationKey = perconaPrefix + "last-config-hash"
	AnnotationRescanNeeded             AnnotationKey = perconaPrefix + "rescan-needed"
	AnnotationPVCResizeInProgress      AnnotationKey = perconaPrefix + "pvc-resize-in-progress"
	AnnotationBaseBackupName           AnnotationKey = perconaPrefix + "base-backup-name"
	AnnotationClusterSetRecoveryNeeded AnnotationKey = perconaPrefix + "clusterset-recovery-needed"
	AnnotationClusterSetRejoinCluster  AnnotationKey = perconaPrefix + "clusterset-rejoin-cluster"
	AnnotationLastAppliedConfig        AnnotationKey = perconaPrefix + "last-applied-config"
	AnnotationLastReloadedTLS          AnnotationKey = perconaPrefix + "last-reloaded-tls"
)

const (
	TLSCAKey   = "ca.crt"
	TLSCertKey = "tls.crt"
	TLSKeyKey  = "tls.key"

	// AnnotationLogCollectorConfigHash rolls MySQL pods when the log collector
	// configuration changes. That config is mounted from ConfigMaps by a stable
	// name, so content changes do not alter the pod template on their own.
	AnnotationLogCollectorConfigHash AnnotationKey = perconaPrefix + "logcollector-config-hash"

	// AnnotationLogCollectorDefaulted records the one-time decision made for an
	// unset `.spec.logcollector.enabled`: on for new clusters, off for clusters
	// that predate the feature.
	AnnotationLogCollectorDefaulted AnnotationKey = perconaPrefix + "logcollector-defaulted"
)

const ClusterSetRecoveryFile = "/var/lib/mysql/clusterset-recovery"

func InternalHAProxyConfigMapName(clusterName string) string {
	return "internal-haproxy-config-" + clusterName
}
