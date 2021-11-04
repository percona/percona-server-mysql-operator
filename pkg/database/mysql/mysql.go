package mysql

import (
	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ComponentName    = "mysql"
	DataVolumeName   = "datadir"
	DataMountPath    = "/var/lib/mysql"
	ConfigVolumeName = "config"
	ConfigMountPath  = "/etc/mysql/config"
	CredsVolumeName  = "users"
	CredsMountPath   = "/etc/mysql/mysql-users-secret"
	TLSVolumeName    = "tls"
	TLSMountPath     = "/etc/mysql/mysql-tls-secret"
	DefaultPort      = int32(3306)
	DefaultAdminPort = int32(33062)
)

type MySQL struct {
	v2.MySQLSpec

	cluster *v2.PerconaServerForMySQL
}

func New(cr *v2.PerconaServerForMySQL) *MySQL {
	return &MySQL{
		MySQLSpec: cr.Spec.MySQL,
		cluster:   cr,
	}
}

func (m *MySQL) Name() string {
	return m.cluster.Name + "-" + ComponentName
}

func (m *MySQL) Namespace() string {
	return m.cluster.Namespace
}

func (m *MySQL) GetSize() int32 {
	return m.Size
}

func IsMySQL(obj client.Object) bool {
	labels := obj.GetLabels()
	comp, ok := labels[v2.ComponentLabel]
	return ok && comp == ComponentName
}
