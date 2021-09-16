package mysql

import (
	v2 "github.com/percona/percona-mysql/pkg/api/v2"
)

const (
	Name           = "mysql"
	DataVolumeName = "datadir"
)

type MySQL struct {
	v2.MySQLSpec

	Name          string
	Namespace     string
	secretsName   string
	clusterLabels map[string]string
}

func New(cr *v2.PerconaServerForMySQL) *MySQL {
	return &MySQL{
		MySQLSpec:     cr.Spec.MySQL,
		Name:          cr.Name + "-" + Name,
		Namespace:     cr.Namespace,
		secretsName:   cr.Spec.SecretsName,
		clusterLabels: cr.Labels(),
	}
}
