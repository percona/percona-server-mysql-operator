package mysql

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
)

func TestGetReadyPod(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, apiv1alpha1.AddToScheme(scheme))

	cluster := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
		},
	}

	t.Run("returns ready pod", func(t *testing.T) {
		readyPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mysql-ready",
				Namespace: "test-ns",
				Labels:    MatchLabels(cluster),
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
				},
			},
		}

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(readyPod).Build()

		pod, err := GetReadyPod(context.Background(), cl, cluster)
		require.NoError(t, err)
		assert.NotNil(t, pod)
		assert.Equal(t, "mysql-ready", pod.Name)
	})

	t.Run("returns first ready pod when multiple available", func(t *testing.T) {
		readyPod1 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mysql-ready-0",
				Namespace: "test-ns",
				Labels:    MatchLabels(cluster),
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
				},
			},
		}
		readyPod2 := readyPod1.DeepCopy()
		readyPod2.Name = "mysql-ready-1"

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(readyPod1, readyPod2).Build()

		pod, err := GetReadyPod(context.Background(), cl, cluster)
		require.NoError(t, err)
		assert.NotNil(t, pod)
		assert.Contains(t, []string{"mysql-ready-0", "mysql-ready-1"}, pod.Name)
	})

	t.Run("skips non-ready pods and returns ready one", func(t *testing.T) {
		notReady := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mysql-not-ready",
				Namespace: "test-ns",
				Labels:    MatchLabels(cluster),
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionFalse},
					{Type: corev1.ContainersReady, Status: corev1.ConditionFalse},
				},
			},
		}
		ready := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mysql-ready",
				Namespace: "test-ns",
				Labels:    MatchLabels(cluster),
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
				},
			},
		}

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(notReady, ready).Build()

		pod, err := GetReadyPod(context.Background(), cl, cluster)
		require.NoError(t, err)
		assert.NotNil(t, pod)
		assert.Equal(t, "mysql-ready", pod.Name)
	})

	t.Run("returns error when no ready pods found", func(t *testing.T) {
		notReady := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mysql-not-ready",
				Namespace: "test-ns",
				Labels:    MatchLabels(cluster),
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionFalse},
					{Type: corev1.ContainersReady, Status: corev1.ConditionFalse},
				},
			},
		}

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(notReady).Build()

		pod, err := GetReadyPod(context.Background(), cl, cluster)
		require.Error(t, err)
		assert.Nil(t, pod)
		assert.Contains(t, err.Error(), "no ready pods")
	})

	t.Run("skips pods with deletion timestamp", func(t *testing.T) {
		now := metav1.Now()
		deleting := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "mysql-deleting",
				Namespace:         "test-ns",
				Labels:            MatchLabels(cluster),
				DeletionTimestamp: &now,
				Finalizers:        []string{"test/finalizer"},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
				},
			},
		}

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deleting).Build()

		pod, err := GetReadyPod(context.Background(), cl, cluster)

		require.Error(t, err)
		assert.Nil(t, pod)
		assert.Contains(t, err.Error(), "no ready pods")

	})
}

func TestGetMySQLPod(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, apiv1alpha1.AddToScheme(scheme))

	cluster := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
		},
	}

	t.Run("gets pod by index (0)", func(t *testing.T) {
		name := PodName(cluster, 0)
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "test-ns",
			},
		}

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).Build()

		got, err := GetPod(context.Background(), cl, cluster, 0)
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Equal(t, name, got.Name)
		assert.Equal(t, "test-ns", got.Namespace)
	})

	t.Run("returns not found for missing pod", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()

		got, err := GetPod(context.Background(), cl, cluster, 0)
		require.Error(t, err)
		assert.Nil(t, got)
	})

	t.Run("different index -> not found", func(t *testing.T) {
		name0 := PodName(cluster, 0)
		p0 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name0,
				Namespace: "test-ns",
			},
		}

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p0).Build()

		got, err := GetPod(context.Background(), cl, cluster, 1)
		require.Error(t, err)
		assert.Nil(t, got)
	})
}
