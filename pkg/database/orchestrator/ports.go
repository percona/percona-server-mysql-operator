package orchestrator

import (
	corev1 "k8s.io/api/core/v1"
)

func (o *Orchestrator) ports() []corev1.ContainerPort {
	return []corev1.ContainerPort{
		{
			ContainerPort: 3000,
			Name:          "web",
		},
		{
			ContainerPort: 10008,
			Name:          "raft",
		},
	}
}
