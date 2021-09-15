package orchestrator

import (
	corev1 "k8s.io/api/core/v1"
)

func (o *Orchestrator) ports() map[string]int32 {
	return map[string]int32{
		"web":  int32(3000),
		"raft": int32(10008),
	}
}

func (o *Orchestrator) containerPorts() []corev1.ContainerPort {
	containerPorts := make([]corev1.ContainerPort, 0, len(o.ports()))

	for name, port := range o.ports() {
		containerPorts = append(containerPorts, corev1.ContainerPort{Name: name, ContainerPort: port})
	}

	return containerPorts
}

func (o *Orchestrator) servicePorts() []corev1.ServicePort {
	servicePorts := make([]corev1.ServicePort, 0, len(o.ports()))

	for name, port := range o.ports() {
		servicePorts = append(servicePorts, corev1.ServicePort{Name: name, Port: port})
	}

	return servicePorts
}
