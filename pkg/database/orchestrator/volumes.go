package orchestrator

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/percona/percona-mysql/pkg/k8s"
)

func (o *Orchestrator) volumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      DataVolumeName,
			MountPath: DataMountPath,
		},
		{
			Name:      ConfigVolumeName,
			MountPath: ConfigMountPath,
		},
	}
}

func (o *Orchestrator) volumes() (volumes []corev1.Volume) {
	return []corev1.Volume{
		{
			Name: ConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ConfigMap: &corev1.ConfigMapProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: o.Name,
								},
							},
							Secret: &corev1.SecretProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: o.Name,
								},
							},
						},
					},
				},
			},
		},
	}
}

func (o *Orchestrator) persistentVolumeClaims() (volumes []corev1.PersistentVolumeClaim) {
	return []corev1.PersistentVolumeClaim{k8s.PVC(DataVolumeName, o.VolumeSpec)}
}
