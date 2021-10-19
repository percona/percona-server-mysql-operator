package mysql

import (
	corev1 "k8s.io/api/core/v1"
)

func (m *MySQL) env() []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:  "SERVICE_NAME",
			Value: m.ServiceName(),
		},
	}
}
