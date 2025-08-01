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
	FinalizerDeleteSSL         = perconaPrefix + "delete-ssl"
	FinalizerDeletePodsInOrder = perconaPrefix + "delete-mysql-pods-in-order"
	FinalizerDeleteBackup      = perconaPrefix + "delete-backup"
	FinalizerDeleteMySQLPvc    = perconaPrefix + "delete-mysql-pvc"
)

type AnnotationKey string

func (s AnnotationKey) String() string {
	return string(s)
}

const (
	AnnotationSecretHash          AnnotationKey = perconaPrefix + "last-applied-secret"
	AnnotationConfigHash          AnnotationKey = perconaPrefix + "configuration-hash"
	AnnotationTLSHash             AnnotationKey = perconaPrefix + "last-applied-tls"
	AnnotationPasswordsUpdated    AnnotationKey = perconaPrefix + "passwords-updated"
	AnnotationLastConfigHash      AnnotationKey = perconaPrefix + "last-config-hash"
	AnnotationRescanNeeded        AnnotationKey = perconaPrefix + "rescan-needed"
	AnnotationPVCResizeInProgress AnnotationKey = perconaPrefix + "pvc-resize-in-progress"
)
