package mysql

import (
	corev1 "k8s.io/api/core/v1"
)

func (m *MySQL) ports() map[string]int32 {
	return map[string]int32{
		"mysql": int32(3306),
	}
}

func (m *MySQL) containerPorts() []corev1.ContainerPort {
	return []corev1.ContainerPort{
		{
			Name:          "mysql",
			ContainerPort: int32(3306),
		},
	}
}

func (m *MySQL) servicePorts() []corev1.ServicePort {
	return []corev1.ServicePort{
		{
			Name: "mysql",
			Port: int32(3306),
		},
	}
}
