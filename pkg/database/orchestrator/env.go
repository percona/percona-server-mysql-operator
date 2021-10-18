package orchestrator

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
)

func (o *Orchestrator) env() []corev1.EnvVar {
	m := mysql.New(o.cluster)

	return []corev1.EnvVar{
		{
			Name:  "ORC_SERVICE",
			Value: o.ServiceName(),
		},
		{
			Name:  "MYSQL_SERVICE",
			Value: m.ServiceName(),
		},
	}
}
