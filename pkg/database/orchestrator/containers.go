package orchestrator

import (
	corev1 "k8s.io/api/core/v1"
)

func (o *Orchestrator) Containers() []corev1.Container {
	containers := []corev1.Container{o.Container()}
	containers = append(containers, o.SidecarContainers()...)
	return containers
}

func (o *Orchestrator) Container() corev1.Container {
	return corev1.Container{
		Name:                     Name,
		Image:                    o.Image,
		ImagePullPolicy:          o.ImagePullPolicy,
		Env:                      o.env(),
		Ports:                    o.ports(),
		VolumeMounts:             o.volumeMounts(),
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          o.ContainerSecurityContext,
	}
}

func (o *Orchestrator) SidecarContainers() []corev1.Container {
	return nil
}

func (o *Orchestrator) InitContainer(initImage string) *corev1.Container {
	return nil
}
