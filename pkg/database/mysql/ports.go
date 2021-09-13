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
	containerPorts := make([]corev1.ContainerPort, 0, len(m.ports()))

	for name, port := range m.ports() {
		containerPorts = append(containerPorts, corev1.ContainerPort{Name: name, ContainerPort: port})
	}

	return containerPorts
}

func (m *MySQL) servicePorts() []corev1.ServicePort {
	servicePorts := make([]corev1.ServicePort, 0, len(m.ports()))

	for name, port := range m.ports() {
		servicePorts = append(servicePorts, corev1.ServicePort{Name: name, Port: port})
	}

	return servicePorts
}
