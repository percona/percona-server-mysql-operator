package mysql

import (
	corev1 "k8s.io/api/core/v1"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func (m *MySQL) env() []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:  "MONITOR_HOST",
			Value: "%",
		},
		{
			Name: "MONITOR_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(m.secretsName, v2.USERS_SECRET_KEY_MONITOR),
			},
		},
		{
			Name: "XTRABACKUP_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(m.secretsName, v2.USERS_SECRET_KEY_XTRABACKUP),
			},
		},
		{
			Name: "MYSQL_ROOT_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(m.secretsName, v2.USERS_SECRET_KEY_ROOT),
			},
		},
		{
			Name: "OPERATOR_ADMIN_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(m.secretsName, v2.USERS_SECRET_KEY_OPERATOR),
			},
		},
		{
			Name: "ORC_TOPOLOGY_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(m.secretsName, v2.USERS_SECRET_KEY_ORCHESTRATOR),
			},
		},
		{
			Name:  "MY_NAMESPACE",
			Value: m.Namespace,
		},
		{
			Name:  "MY_SERVICE_NAME",
			Value: m.Name,
		},
		{
			Name: "MY_POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.name",
				},
			},
		},
		{
			Name:  "MY_FQDN",
			Value: "$(MY_POD_NAME).$(MY_SERVICE_NAME).$(MY_NAMESPACE)",
		},
	}
}
