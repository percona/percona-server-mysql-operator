package orchestrator

import (
	"path/filepath"

	corev1 "k8s.io/api/core/v1"

	v2 "github.com/percona/percona-server-mysql-operator/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func containerMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      dataVolumeName,
			MountPath: DataMountPath,
		},
		{
			Name:      tlsVolumeName,
			MountPath: tlsMountPath,
		},
		{
			Name:      credsVolumeName,
			MountPath: filepath.Join(CredsMountPath, v2.USERS_SECRET_KEY_ORCHESTRATOR),
			SubPath:   v2.USERS_SECRET_KEY_ORCHESTRATOR,
		},
	}
}

func volumes(cr *v2.PerconaServerForMySQL) (volumes []corev1.Volume) {
	return []corev1.Volume{
		{
			Name: credsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: secretsName(cr),
				},
			},
		},
		{
			Name: tlsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: sslSecretsName(cr),
				},
			},
		},
	}
}

func persistentVolumeClaims(cr *v2.PerconaServerForMySQL) (volumes []corev1.PersistentVolumeClaim) {
	return []corev1.PersistentVolumeClaim{
		k8s.PVC(dataVolumeName, cr.Spec.Orchestrator.VolumeSpec),
	}
}
