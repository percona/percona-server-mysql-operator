package util

import (
	corev1 "k8s.io/api/core/v1"
)

func AppendUniqueContainers(containers []corev1.Container, more ...corev1.Container) []corev1.Container {
	if len(more) == 0 {
		return containers
	}

	exists := make(map[string]bool)
	for i := range containers {
		exists[containers[i].Name] = true
	}

	for i := range more {
		name := more[i].Name
		if exists[name] {
			continue
		}

		containers = append(containers, more[i])
		exists[name] = true
	}

	return containers
}
