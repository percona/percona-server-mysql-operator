package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestStatefulSet(t *testing.T) {
	tlsHash := "123abc"
	initImage := "percona/init:latest"

	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster1",
			Namespace: "test-ns",
		},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			Orchestrator: apiv1alpha1.OrchestratorSpec{
				Enabled: true,
				PodSpec: apiv1alpha1.PodSpec{
					Size:                          3,
					TerminationGracePeriodSeconds: pointerInt64(30),
				},
			},
		},
	}

	sts := StatefulSet(cr, initImage, tlsHash)

	assert.NotNil(t, sts)
	assert.Equal(t, "cluster1-orc", sts.Name)
	assert.Equal(t, "test-ns", sts.Namespace)
	assert.Equal(t, int32(3), *sts.Spec.Replicas)

	val, ok := sts.Spec.Template.Annotations[string(naming.AnnotationTLSHash)]
	assert.True(t, ok)
	assert.Equal(t, tlsHash, val)

	initContainers := sts.Spec.Template.Spec.InitContainers
	assert.Len(t, initContainers, 1)
	assert.Equal(t, initImage, initContainers[0].Image)

	assert.Equal(t, pointerInt64(30), sts.Spec.Template.Spec.TerminationGracePeriodSeconds)
}

func pointerInt64(i int64) *int64 {
	return &i
}
