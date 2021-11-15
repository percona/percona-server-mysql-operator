package orchestrator

import (
	corev1 "k8s.io/api/core/v1"
)

func containerPorts() []corev1.ContainerPort {
	return []corev1.ContainerPort{
		{
			Name:          "web",
			ContainerPort: int32(defaultWebPort),
		},
		{
			Name:          "raft",
			ContainerPort: int32(defaultRaftPort),
		},
	}
}
