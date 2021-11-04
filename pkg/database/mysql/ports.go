package mysql

import (
	corev1 "k8s.io/api/core/v1"
)

func (m *MySQL) containerPorts() []corev1.ContainerPort {
	return []corev1.ContainerPort{
		{
			Name:          ComponentName,
			ContainerPort: DefaultPort,
		},
	}
}

func (m *MySQL) servicePorts() []corev1.ServicePort {
	return []corev1.ServicePort{
		{
			Name: ComponentName,
			Port: DefaultPort,
		},
	}
}
