package orchestrator

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (o *Orchestrator) Containers() []corev1.Container {
	containers := []corev1.Container{o.Container()}
	containers = append(containers, o.SidecarContainers()...)
	return containers
}

func (o *Orchestrator) Container() corev1.Container {
	return corev1.Container{
		Name:                     ComponentName,
		Image:                    o.Image,
		ImagePullPolicy:          o.ImagePullPolicy,
		Env:                      o.env(),
		Ports:                    o.containerPorts(),
		VolumeMounts:             o.volumeMounts(),
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          o.ContainerSecurityContext,
		LivenessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/api/health",
					Port: intstr.FromString("web"),
				},
			},
			InitialDelaySeconds: int32(10),
			TimeoutSeconds:      int32(3),
			PeriodSeconds:       int32(5),
			FailureThreshold:    int32(3),
			SuccessThreshold:    int32(1),
		},
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/api/raft-health",
					Port: intstr.FromString("web"),
				},
			},
			InitialDelaySeconds: int32(30),
			TimeoutSeconds:      int32(3),
			PeriodSeconds:       int32(5),
			FailureThreshold:    int32(3),
			SuccessThreshold:    int32(1),
		},
	}
}

func (o *Orchestrator) SidecarContainers() []corev1.Container {
	return nil
}

func (o *Orchestrator) InitContainers(initImage string) []corev1.Container {
	return nil
}
