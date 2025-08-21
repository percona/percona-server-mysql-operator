package util

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
)

func MergeEnvLists(envLists ...[]corev1.EnvVar) []corev1.EnvVar {
	resultList := make([]corev1.EnvVar, 0)
	for _, list := range envLists {
		for _, env := range list {
			idx := slices.IndexFunc(resultList, func(e corev1.EnvVar) bool {
				return e.Name == env.Name
			})
			if idx == -1 {
				resultList = append(resultList, env)
				continue
			}
			resultList[idx] = env
		}
	}
	return resultList
}
