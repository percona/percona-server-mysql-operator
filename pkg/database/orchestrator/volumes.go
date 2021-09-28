package orchestrator

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func (o *Orchestrator) volumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      DataVolumeName,
			MountPath: DataMountPath,
		},
		{
			Name:      CredsVolumeName,
			MountPath: CredsMountPath,
		},
		{
			Name:      TLSVolumeName,
			MountPath: TLSMountPath,
		},
	}
}

func (o *Orchestrator) volumes() (volumes []corev1.Volume) {
	return []corev1.Volume{
		{
			Name: CredsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: o.Name,
				},
			},
		},
		{
			Name: TLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: o.sslSecretName,
				},
			},
		},
	}
}

func (o *Orchestrator) persistentVolumeClaims() (volumes []corev1.PersistentVolumeClaim) {
	return []corev1.PersistentVolumeClaim{k8s.PVC(DataVolumeName, o.VolumeSpec)}
}
