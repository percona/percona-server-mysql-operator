package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestGetReadyPod(t *testing.T) {
	scheme := runtime.NewScheme()
	err := corev1.AddToScheme(scheme)
	require.NoError(t, err)
	err = apiv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	cluster := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
		},
	}

	t.Run("returns ready pod when available", func(t *testing.T) {
		readyPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "orchestrator-ready",
				Namespace: "test-ns",
				Labels: map[string]string{
					naming.LabelInstance:  "test-cluster",
					naming.LabelComponent: naming.ComponentOrchestrator,
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.ContainersReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(readyPod).
			Build()

		pod, err := GetReadyPod(context.Background(), fakeClient, cluster)
		require.NoError(t, err)
		assert.NotNil(t, pod)
		assert.Equal(t, "orchestrator-ready", pod.Name)
	})

	t.Run("returns first ready pod when multiple available", func(t *testing.T) {
		readyPod1 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "orchestrator-ready-1",
				Namespace: "test-ns",
				Labels: map[string]string{
					naming.LabelInstance:  "test-cluster",
					naming.LabelComponent: naming.ComponentOrchestrator,
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.ContainersReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}

		readyPod2 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "orchestrator-ready-2",
				Namespace: "test-ns",
				Labels: map[string]string{
					naming.LabelInstance:  "test-cluster",
					naming.LabelComponent: naming.ComponentOrchestrator,
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.ContainersReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(readyPod1, readyPod2).
			Build()

		pod, err := GetReadyPod(context.Background(), fakeClient, cluster)
		require.NoError(t, err)
		assert.NotNil(t, pod)
		assert.Contains(t, []string{"orchestrator-ready-1", "orchestrator-ready-2"}, pod.Name)
	})

	t.Run("skips non-ready pods and returns ready one", func(t *testing.T) {
		notReadyPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "orchestrator-not-ready",
				Namespace: "test-ns",
				Labels: map[string]string{
					naming.LabelInstance:  "test-cluster",
					naming.LabelComponent: naming.ComponentOrchestrator,
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
			},
		}

		readyPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "orchestrator-ready",
				Namespace: "test-ns",
				Labels: map[string]string{
					naming.LabelInstance:  "test-cluster",
					naming.LabelComponent: naming.ComponentOrchestrator,
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.ContainersReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(notReadyPod, readyPod).
			Build()

		pod, err := GetReadyPod(context.Background(), fakeClient, cluster)
		require.NoError(t, err)
		assert.NotNil(t, pod)
		assert.Equal(t, "orchestrator-ready", pod.Name)
	})

	t.Run("returns error when no ready pods found", func(t *testing.T) {
		notReadyPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "orchestrator-not-ready",
				Namespace: "test-ns",
				Labels: map[string]string{
					naming.LabelInstance:  "test-cluster",
					naming.LabelComponent: naming.ComponentOrchestrator,
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(notReadyPod).
			Build()

		pod, err := GetReadyPod(context.Background(), fakeClient, cluster)
		require.Error(t, err)
		assert.Nil(t, pod)
		assert.Contains(t, err.Error(), "no ready pods")
	})

	t.Run("returns error when no orchestrator pods found", func(t *testing.T) {
		otherPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "other-pod",
				Namespace: "test-ns",
				Labels: map[string]string{
					naming.LabelInstance:  "test-cluster",
					naming.LabelComponent: "mysql",
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.ContainersReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(otherPod).
			Build()

		pod, err := GetReadyPod(context.Background(), fakeClient, cluster)
		require.Error(t, err)
		assert.Nil(t, pod)
		assert.Contains(t, err.Error(), "no ready pods")
	})

	t.Run("skips pods with deletion timestamp", func(t *testing.T) {
		now := metav1.Now()
		deletingPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "orchestrator-deleting",
				Namespace:         "test-ns",
				Finalizers:        []string{"percona.com/test"},
				DeletionTimestamp: &now,
				Labels: map[string]string{
					naming.LabelInstance:  "test-cluster",
					naming.LabelComponent: naming.ComponentOrchestrator,
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.ContainersReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(deletingPod).
			Build()

		pod, err := GetReadyPod(context.Background(), fakeClient, cluster)
		require.Error(t, err)
		assert.Nil(t, pod)
		assert.Contains(t, err.Error(), "no ready pods")
	})
}
