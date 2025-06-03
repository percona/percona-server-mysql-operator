package mysql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestStatefulSet(t *testing.T) {
	configHash := "123abc"
	tlsHash := "123abc"
	initImage := "percona/init:latest"

	q, err := resource.ParseQuantity("1Gi")
	assert.NoError(t, err)

	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster1",
			Namespace: "test-ns",
		},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			MySQL: apiv1alpha1.MySQLSpec{
				PodSpec: apiv1alpha1.PodSpec{
					Size:                          3,
					TerminationGracePeriodSeconds: pointerInt64(30),
					VolumeSpec: &apiv1alpha1.VolumeSpec{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: map[corev1.ResourceName]resource.Quantity{
									corev1.ResourceStorage: q,
								},
							},
						},
					},
				},
			},
		},
	}

	sts := StatefulSet(cr, initImage, configHash, tlsHash, nil)

	assert.NotNil(t, sts)
	assert.Equal(t, "cluster1-mysql", sts.Name)
	assert.Equal(t, "test-ns", sts.Namespace)
	assert.Equal(t, int32(3), *sts.Spec.Replicas)

	actualTLSAnn, ok := sts.Spec.Template.Annotations[string(naming.AnnotationTLSHash)]
	assert.True(t, ok)
	assert.Equal(t, tlsHash, actualTLSAnn)

	actualConfigAnn, ok := sts.Spec.Template.Annotations[string(naming.AnnotationConfigHash)]
	assert.True(t, ok)
	assert.Equal(t, configHash, actualConfigAnn)

	initContainers := sts.Spec.Template.Spec.InitContainers
	assert.Len(t, initContainers, 1)
	assert.Equal(t, initImage, initContainers[0].Image)

	assert.Equal(t, pointerInt64(30), sts.Spec.Template.Spec.TerminationGracePeriodSeconds)

	expectedPVC := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "datadir",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					"storage": resource.MustParse("1Gi"),
				},
			},
		},
	}

	assert.Equal(t, expectedPVC, sts.Spec.VolumeClaimTemplates[0])
}

func pointerInt64(i int64) *int64 {
	return &i
}
