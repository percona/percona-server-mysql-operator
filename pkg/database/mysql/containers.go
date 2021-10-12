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
			InitialDelaySeconds:           m.StartupProbe.InitialDelaySeconds,
			TimeoutSeconds:                m.StartupProbe.TimeoutSeconds,
			PeriodSeconds:                 m.StartupProbe.PeriodSeconds,
			FailureThreshold:              m.StartupProbe.FailureThreshold,
			SuccessThreshold:              m.StartupProbe.SuccessThreshold,
			TerminationGracePeriodSeconds: m.StartupProbe.TerminationGracePeriodSeconds,
		},
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(3306),
				},
			},
			InitialDelaySeconds:           m.ReadinessProbe.InitialDelaySeconds,
			TimeoutSeconds:                m.ReadinessProbe.TimeoutSeconds,
			PeriodSeconds:                 m.ReadinessProbe.PeriodSeconds,
			FailureThreshold:              m.ReadinessProbe.FailureThreshold,
			SuccessThreshold:              m.ReadinessProbe.SuccessThreshold,
			TerminationGracePeriodSeconds: m.ReadinessProbe.TerminationGracePeriodSeconds,
		},
	}
}

func (m *MySQL) SidecarContainers() []corev1.Container {
	return nil
}

func (m *MySQL) InitContainers(initImage string) []corev1.Container {
	return []corev1.Container{
		{
			Name:                     Name + "-init",
			Image:                    initImage,
			ImagePullPolicy:          m.ImagePullPolicy,
			VolumeMounts:             m.volumeMounts(),
			Command:                  []string{"/ps-init-entrypoint.sh"},
			TerminationMessagePath:   "/dev/termination-log",
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			SecurityContext:          m.ContainerSecurityContext,
		},
	}
}
