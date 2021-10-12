package mysql

import (
	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
)

const (
	Name             = "mysql"
	DataVolumeName   = "datadir"
	DataMountPath    = "/var/lib/mysql"
	ConfigVolumeName = "config"
	ConfigMountPath  = "/etc/mysql/config"
	CredsVolumeName  = "users"
	CredsMountPath   = "/etc/mysql/mysql-users-secret"
	TLSVolumeName    = "tls"
	TLSMountPath     = "/etc/mysql/mysql-tls-secret"
)

type MySQL struct {
	v2.MySQLSpec

	Name      string
	Namespace string

	cluster *v2.PerconaServerForMySQL
}

func New(cr *v2.PerconaServerForMySQL) *MySQL {
	return &MySQL{
		MySQLSpec: cr.Spec.MySQL,
		Name:      cr.Name + "-" + Name,
		Namespace: cr.Namespace,
		cluster:   cr,
	}
}
