package orchestrator

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/percona/percona-mysql/pkg/k8s"
)

func (o *Orchestrator) volumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      DataVolumeName,
			MountPath: "/var/lib/orchestrator",
		},
	}
}

func (o *Orchestrator) volumes() (volumes []corev1.Volume) {
	return nil
}

func (o *Orchestrator) persistentVolumeClaims() (volumes []corev1.PersistentVolumeClaim) {
	return []corev1.PersistentVolumeClaim{k8s.PVC(DataVolumeName, o.VolumeSpec)}
}
