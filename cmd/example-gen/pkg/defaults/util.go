package defaults

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func resources(requestsMemory, requestsCPU, limitsMemory, limitsCPU string) corev1.ResourceRequirements {
	r := corev1.ResourceRequirements{}

	if requestsMemory != "" {
		if r.Requests == nil {
			r.Requests = make(corev1.ResourceList)
		}
		r.Requests[corev1.ResourceMemory] = resource.MustParse(requestsMemory)
	}
	if requestsCPU != "" {
		if r.Requests == nil {
			r.Requests = make(corev1.ResourceList)
		}
		r.Requests[corev1.ResourceCPU] = resource.MustParse(requestsCPU)
	}
	if limitsMemory != "" {
		if r.Limits == nil {
			r.Limits = make(corev1.ResourceList)
		}
		r.Limits[corev1.ResourceMemory] = resource.MustParse(limitsMemory)
	}
	if limitsCPU != "" {
		if r.Limits == nil {
			r.Limits = make(corev1.ResourceList)
		}
		r.Limits[corev1.ResourceCPU] = resource.MustParse(limitsCPU)
	}

	return r
}

func envList(name, value string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:  name,
			Value: value,
		},
	}
}

func envFromList(name string) []corev1.EnvFromSource {
	return []corev1.EnvFromSource{
		{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: name,
				},
			},
		},
	}
}
