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
	return []corev1.ContainerPort{
		{
			Name:          "web",
			ContainerPort: int32(3000),
		},
		{
			Name:          "raft",
			ContainerPort: int32(10008),
		},
	}
}

func (o *Orchestrator) servicePorts() []corev1.ServicePort {
	return []corev1.ServicePort{
		{
			Name: "web",
			Port: int32(3000),
		},
		{
			Name: "raft",
			Port: int32(10008),
		},
	}
}
