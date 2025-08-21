package k8s

import (
	"testing"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestPVC(t *testing.T) {
	storageClassName := "standard"
	accessModes := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	requests := corev1.ResourceList{
		corev1.ResourceStorage: resource.MustParse("2Gi"),
	}

	spec := &apiv1.VolumeSpec{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
			StorageClassName: &storageClassName,
			AccessModes:      accessModes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: requests,
			},
		},
	}

	pvcName := "pvc-name"

	pvc := PVC(pvcName, spec)

	assert.Equal(t, pvcName, pvc.Name)
	assert.NotNil(t, pvc.Spec.StorageClassName)
	assert.Equal(t, storageClassName, *pvc.Spec.StorageClassName)
	assert.Equal(t, accessModes, pvc.Spec.AccessModes)
	assert.Equal(t, requests, pvc.Spec.Resources.Requests)
}
