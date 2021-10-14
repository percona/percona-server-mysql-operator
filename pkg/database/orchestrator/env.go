package orchestrator

import (
	corev1 "k8s.io/api/core/v1"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func (o *Orchestrator) env() []corev1.EnvVar {
	m := mysql.New(o.cluster)

	return []corev1.EnvVar{
		{
			Name:  "ORC_TOPOLOGY_USER",
			Value: v2.USERS_SECRET_KEY_OPERATOR,
		},
		{
			Name: "ORC_TOPOLOGY_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(o.SecretsName(), v2.USERS_SECRET_KEY_ORCHESTRATOR),
			},
		},
		{
			Name: "POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "status.podIP",
				},
			},
		},
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
