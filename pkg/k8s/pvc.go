package k8s

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/percona/percona-server-mysql-operator/pkg/api/v2"
)

func PVC(name string, vspec *v2.VolumeSpec) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: vspec.PersistentVolumeClaim.StorageClassName,
			AccessModes:      vspec.PersistentVolumeClaim.AccessModes,
			Resources:        vspec.PersistentVolumeClaim.Resources,
		},
	}
}
