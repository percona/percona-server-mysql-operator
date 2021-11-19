package mysql

import (
	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/sidecar"
	corev1 "k8s.io/api/core/v1"
)

func (m *MySQL) Containers() []corev1.Container {
	containers := []corev1.Container{m.Container()}
	containers = append(containers, m.SidecarContainers()...)
	return containers
}

func (m *MySQL) Container() corev1.Container {
	return corev1.Container{
		Name:                     ComponentName,
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
		LivenessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				Exec: &corev1.ExecAction{
					Command: []string{"/var/lib/mysql/healthcheck", "liveness"},
				},
			},
			InitialDelaySeconds:           m.LivenessProbe.InitialDelaySeconds,
			TimeoutSeconds:                m.LivenessProbe.TimeoutSeconds,
			PeriodSeconds:                 m.LivenessProbe.PeriodSeconds,
			FailureThreshold:              m.LivenessProbe.FailureThreshold,
			SuccessThreshold:              m.LivenessProbe.SuccessThreshold,
			TerminationGracePeriodSeconds: m.LivenessProbe.TerminationGracePeriodSeconds,
		},
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				Exec: &corev1.ExecAction{
					Command: []string{"/var/lib/mysql/healthcheck", "readiness"},
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
	if m.cluster.Spec.PMM == nil || !m.cluster.Spec.PMM.Enabled {
		return nil
	}

	pmmC := sidecar.PMMContainer(m.cluster)
	pmmC.VolumeMounts = []corev1.VolumeMount{
		{
			Name:      DataVolumeName,
			MountPath: DataMountPath,
		},
	}
	pmmEnv := []corev1.EnvVar{
		{
			Name:  "DB_CLUSTER",
			Value: m.Name(),
		},
		{
			Name:  "DB_TYPE",
			Value: ComponentName,
		},
		{
			Name:  "DB_HOST",
			Value: "localhost",
		},
		{
			Name:  "DB_PORT",
			Value: "33062",
		},
		{
			Name:  "DB_USER",
			Value: v2.USERS_SECRET_KEY_MONITOR,
		},
		{
			Name: "DB_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(m.SecretsName(), v2.USERS_SECRET_KEY_MONITOR),
			},
		},
		{
			Name:  "DB_ARGS",
			Value: "--query-source=perfschema",
		},
	}
	pmmC.Env = append(pmmC.Env, pmmEnv...)

	return []corev1.Container{pmmC}
}

func (m *MySQL) InitContainers(initImage string) []corev1.Container {
	return []corev1.Container{
		{
			Name:                     ComponentName + "-init",
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
