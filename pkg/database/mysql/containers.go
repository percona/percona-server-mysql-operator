package mysql

import (
	corev1 "k8s.io/api/core/v1"
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
		Ports:                    m.ports(),
		VolumeMounts:             m.volumeMounts(),
		Command:                  []string{"/var/lib/mysql/ps-entrypoint.sh"},
		Args:                     []string{"mysqld"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}
}

func (m *MySQL) SidecarContainers() []corev1.Container {
	return nil
}

func (m *MySQL) InitContainer(initImage string) corev1.Container {
	return corev1.Container{
		Name:                     Name + "-init",
		Image:                    initImage,
		ImagePullPolicy:          m.ImagePullPolicy,
		VolumeMounts:             m.volumeMounts(),
		Command:                  []string{"/ps-init-entrypoint.sh"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}
}
