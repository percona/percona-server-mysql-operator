package mysql

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (m *MySQL) Containers() []corev1.Container {
	containers := []corev1.Container{m.Container()}
	containers = append(containers, m.SidecarContainers()...)
	return containers
}

func (m *MySQL) Container() corev1.Container {
	return corev1.Container{
		Name:                     Name,
		Image:                    m.Image,
		ImagePullPolicy:          m.ImagePullPolicy,
		Env:                      m.env(),
		Ports:                    m.containerPorts(),
		VolumeMounts:             m.volumeMounts(),
		Command:                  []string{"/var/lib/mysql/ps-entrypoint.sh"},
		Args:                     []string{"mysqld"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          m.ContainerSecurityContext,
		StartupProbe: &corev1.Probe{
			Handler: corev1.Handler{
				Exec: &corev1.ExecAction{
					Command: []string{"/var/lib/mysql/bootstrap"},
				},
			},
			TimeoutSeconds: int32(300),
			PeriodSeconds:  int32(10),
		},
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(3306),
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

func (m *MySQL) SidecarContainers() []corev1.Container {
	return nil
}

func (m *MySQL) InitContainer(initImage string) *corev1.Container {
	return &corev1.Container{
		Name:                     Name + "-init",
		Image:                    initImage,
		ImagePullPolicy:          m.ImagePullPolicy,
		VolumeMounts:             m.volumeMounts(),
		Command:                  []string{"/ps-init-entrypoint.sh"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          m.ContainerSecurityContext,
	}
}
