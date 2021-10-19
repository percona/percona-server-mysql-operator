package orchestrator

import (
	"path/filepath"

	corev1 "k8s.io/api/core/v1"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func (o *Orchestrator) volumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      DataVolumeName,
			MountPath: DataMountPath,
		},
		{
			Name:      TLSVolumeName,
			MountPath: TLSMountPath,
		},
		{
			Name:      CredsVolumeName,
			MountPath: filepath.Join(CredsMountPath, v2.USERS_SECRET_KEY_ORCHESTRATOR),
			SubPath:   v2.USERS_SECRET_KEY_ORCHESTRATOR,
		},
	}
}

func (o *Orchestrator) volumes() (volumes []corev1.Volume) {
	return []corev1.Volume{
		{
			Name: CredsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: o.SecretsName(),
				},
			},
		},
		{
			Name: TLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: o.SSLSecretsName(),
				},
			},
		},
	}
}

func (o *Orchestrator) persistentVolumeClaims() (volumes []corev1.PersistentVolumeClaim) {
	return []corev1.PersistentVolumeClaim{k8s.PVC(DataVolumeName, o.VolumeSpec)}
}
