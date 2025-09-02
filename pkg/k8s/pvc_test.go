package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

func TestPVC(t *testing.T) {
	storageClassName := "standard"
	accessModes := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	requests := corev1.ResourceList{
		corev1.ResourceStorage: resource.MustParse("2Gi"),
	}
	dataSource := &corev1.TypedLocalObjectReference{
		APIGroup: ptr.To("some-api-group"),
		Kind:     "some-kind",
		Name:     "some-name",
	}
	dataSourceRef := &corev1.TypedObjectReference{
		APIGroup:  ptr.To("some-api-group-2"),
		Kind:      "some-kind-2",
		Name:      "some-name-2",
		Namespace: ptr.To("namespace"),
	}

	spec := &apiv1alpha1.VolumeSpec{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
			StorageClassName: &storageClassName,
			AccessModes:      accessModes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: requests,
			},
			DataSource:    dataSource,
			DataSourceRef: dataSourceRef,
		},
	}

	pvcName := "pvc-name"

	pvc := PVC(new(apiv1alpha1.PerconaServerMySQL), pvcName, spec)

	assert.Equal(t, pvcName, pvc.Name)
	assert.NotNil(t, pvc.Spec.StorageClassName)
	assert.Equal(t, storageClassName, *pvc.Spec.StorageClassName)
	assert.Equal(t, accessModes, pvc.Spec.AccessModes)
	assert.Equal(t, requests, pvc.Spec.Resources.Requests)
	assert.Equal(t, dataSource, pvc.Spec.DataSource)
	assert.Equal(t, dataSourceRef, pvc.Spec.DataSourceRef)
}
