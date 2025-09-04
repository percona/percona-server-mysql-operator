package k8s

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

func PVC(cr *apiv1alpha1.PerconaServerMySQL, name string, spec *apiv1alpha1.VolumeSpec) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: spec.PersistentVolumeClaim.StorageClassName,
			AccessModes:      spec.PersistentVolumeClaim.AccessModes,
			Resources:        spec.PersistentVolumeClaim.Resources,
			DataSource:       spec.PersistentVolumeClaim.DataSource,
			DataSourceRef:    spec.PersistentVolumeClaim.DataSourceRef,
		},
	}
}
