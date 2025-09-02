package k8s

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/testutil"
)

func TestAnnotateObject(t *testing.T) {
	ctx := context.Background()

	tests := []client.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "test-namespace",
			},
		},
		&psv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
			},
		},
	}

	for _, tt := range tests {
		name := reflect.TypeOf(tt).Elem().Name()
		t.Run(name, func(t *testing.T) {
			cl := testutil.BuildFakeClient(tt)

			annotations := map[naming.AnnotationKey]string{
				naming.AnnotationConfigHash: "hash",
			}

			err := AnnotateObject(ctx, cl, tt, annotations)
			require.NoError(t, err)

			freshObj := tt.DeepCopyObject().(client.Object)
			err = cl.Get(ctx, client.ObjectKeyFromObject(tt), freshObj)
			require.NoError(t, err)

			val, ok := freshObj.GetAnnotations()[string(naming.AnnotationConfigHash)]
			assert.True(t, ok)
			assert.Equal(t, "hash", val)
		})
	}
}

func TestDeannotateObject(t *testing.T) {
	ctx := context.Background()

	tests := []client.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "test-namespace",
				Annotations: map[string]string{
					string(naming.AnnotationConfigHash): "hash",
					"other-annotation":                  "value",
				},
			},
		},
		&psv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "test-namespace",
				Annotations: map[string]string{
					string(naming.AnnotationConfigHash): "hash",
					"other-annotation":                  "value",
				},
			},
		},
	}

	for _, tt := range tests {
		name := reflect.TypeOf(tt).Elem().Name()
		t.Run(name, func(t *testing.T) {
			cl := testutil.BuildFakeClient(tt)

			err := DeannotateObject(ctx, cl, tt, naming.AnnotationConfigHash)
			require.NoError(t, err)

			freshObj := tt.DeepCopyObject().(client.Object)
			err = cl.Get(ctx, client.ObjectKeyFromObject(tt), freshObj)
			require.NoError(t, err)

			_, ok := freshObj.GetAnnotations()[string(naming.AnnotationConfigHash)]
			assert.False(t, ok, "annotation should be removed")

			val, ok := freshObj.GetAnnotations()["other-annotation"]
			assert.True(t, ok, "other annotations should remain")
			assert.Equal(t, "value", val)
		})
	}
}
