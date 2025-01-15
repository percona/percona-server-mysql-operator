package naming

const (
	perconaPrefix      = "percona.com/"
	mysqlPerconaPrefix = "mysql.percona.com/"
)

const (
	LabelName      = "app.kubernetes.io/name"
	LabelInstance  = "app.kubernetes.io/instance"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelPartOf    = "app.kubernetes.io/part-of"
	LabelComponent = "app.kubernetes.io/component"
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
	AnnotationSecretHash       AnnotationKey = perconaPrefix + "last-applied-secret"
	AnnotationConfigHash       AnnotationKey = perconaPrefix + "configuration-hash"
	AnnotationTLSHash          AnnotationKey = perconaPrefix + "last-applied-tls"
	AnnotationPasswordsUpdated AnnotationKey = perconaPrefix + "passwords-updated"
	AnnotationLastConfigHash   AnnotationKey = perconaPrefix + "last-config-hash"
)
