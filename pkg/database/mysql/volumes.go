package mysql

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/percona/percona-mysql/pkg/k8s"
)

func (m *MySQL) volumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      DataVolumeName,
			MountPath: "/var/lib/mysql",
		},
	}
}

func (m *MySQL) volumes() (volumes []corev1.Volume) {
	return nil
}

func (m *MySQL) persistentVolumeClaims() (volumes []corev1.PersistentVolumeClaim) {
	return []corev1.PersistentVolumeClaim{k8s.PVC(DataVolumeName, m.VolumeSpec)}
}
