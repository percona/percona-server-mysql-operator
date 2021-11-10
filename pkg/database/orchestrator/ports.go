package orchestrator

import (
	corev1 "k8s.io/api/core/v1"
)

func (o *Orchestrator) containerPorts() []corev1.ContainerPort {
	return []corev1.ContainerPort{
		{
			Name:          "web",
			ContainerPort: int32(DefaultWebPort),
		},
		{
			Name:          "raft",
			ContainerPort: int32(DefaultRaftPort),
		},
	}
}
