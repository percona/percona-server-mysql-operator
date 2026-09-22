package k8s

import (
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func PVC(cr *apiv1.PerconaServerMySQL, name string, spec *apiv1.VolumeSpec) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		Name: name,
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: spec.PersistentVolumeClaim.StorageClassName,
			AccessModes:      spec.PersistentVolumeClaim.AccessModes,
			Resources:        spec.PersistentVolumeClaim.Resources,
		},
	}
}
