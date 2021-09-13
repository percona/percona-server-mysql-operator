package mysql

import (
	corev1 "k8s.io/api/core/v1"
)

func (m *MySQL) ports() []corev1.ContainerPort {
	return []corev1.ContainerPort{
		{
			ContainerPort: 3306,
			Name:          "mysql",
		},
	}
}
