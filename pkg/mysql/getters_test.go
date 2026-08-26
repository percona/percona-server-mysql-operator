package mysql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestGetReadyPod(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	cluster := &apiv1.PerconaServerMySQL{
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

		pod, err := GetReadyPod(t.Context(), cl, cluster)
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

		pod, err := GetReadyPod(t.Context(), cl, cluster)
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

		pod, err := GetReadyPod(t.Context(), cl, cluster)
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

		pod, err := GetReadyPod(t.Context(), cl, cluster)
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

		pod, err := GetReadyPod(t.Context(), cl, cluster)

		require.Error(t, err)
		assert.Nil(t, pod)
		assert.Contains(t, err.Error(), "no ready pods")

	})
}

func TestGetMySQLPod(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	cluster := &apiv1.PerconaServerMySQL{
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

		got, err := GetPod(t.Context(), cl, cluster, 0)
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Equal(t, name, got.Name)
		assert.Equal(t, "test-ns", got.Namespace)
	})

	t.Run("returns not found for missing pod", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()

		got, err := GetPod(t.Context(), cl, cluster, 0)
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

		got, err := GetPod(t.Context(), cl, cluster, 1)
		require.Error(t, err)
		assert.Nil(t, got)
	})
}

func TestGetAppliedCRVersion(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	cluster := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
		},
	}

	const updateRevision = "rev-2"

	newStatefulSet := func(crVersion string) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:       NamespacedName(cluster).Name,
				Namespace:  cluster.Namespace,
				Generation: 2,
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: new(int32(3)),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "sidecar",
								Env:  []corev1.EnvVar{{Name: crVersionEnvVar, Value: "0.0.0"}},
							},
							{
								Name: AppName,
								Env:  []corev1.EnvVar{{Name: crVersionEnvVar, Value: crVersion}},
							},
						},
					},
				},
			},
			Status: appsv1.StatefulSetStatus{
				ObservedGeneration: 2,
				UpdateRevision:     updateRevision,
			},
		}
	}

	newPod := func(idx int, revision string, ready bool) *corev1.Pod {
		labels := MatchLabels(cluster)
		labels[appsv1.StatefulSetRevisionLabel] = revision

		condition := corev1.ConditionFalse
		if ready {
			condition = corev1.ConditionTrue
		}

		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      PodName(cluster, idx),
				Namespace: cluster.Namespace,
				Labels:    labels,
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: condition},
					{Type: corev1.ContainersReady, Status: condition},
				},
			},
		}
	}

	t.Run("returns version when all pods are rolled out", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			newStatefulSet("1.3.0"),
			newPod(0, updateRevision, true),
			newPod(1, updateRevision, true),
			newPod(2, updateRevision, true),
		).Build()

		version, err := GetAppliedCRVersion(t.Context(), cl, cluster)
		require.NoError(t, err)
		assert.Equal(t, "1.3.0", version)
	})

	t.Run("returns ErrRolloutInProgress when a pod runs the old revision", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			newStatefulSet("1.3.0"),
			newPod(0, updateRevision, true),
			newPod(1, updateRevision, true),
			newPod(2, "rev-1", true),
		).Build()

		version, err := GetAppliedCRVersion(t.Context(), cl, cluster)
		require.ErrorIs(t, err, ErrRolloutInProgress)
		assert.Empty(t, version)
	})

	t.Run("returns ErrRolloutInProgress when an updated pod is not ready", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			newStatefulSet("1.3.0"),
			newPod(0, updateRevision, true),
			newPod(1, updateRevision, true),
			newPod(2, updateRevision, false),
		).Build()

		version, err := GetAppliedCRVersion(t.Context(), cl, cluster)
		require.ErrorIs(t, err, ErrRolloutInProgress)
		assert.Empty(t, version)
	})

	t.Run("returns ErrRolloutInProgress when a pod is missing", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			newStatefulSet("1.3.0"),
			newPod(0, updateRevision, true),
			newPod(1, updateRevision, true),
		).Build()

		version, err := GetAppliedCRVersion(t.Context(), cl, cluster)
		require.ErrorIs(t, err, ErrRolloutInProgress)
		assert.Empty(t, version)
	})

	t.Run("returns ErrRolloutInProgress when the statefulset status is stale", func(t *testing.T) {
		sfs := newStatefulSet("1.3.0")
		sfs.Status.ObservedGeneration = sfs.Generation - 1

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			sfs,
			newPod(0, updateRevision, true),
			newPod(1, updateRevision, true),
			newPod(2, updateRevision, true),
		).Build()

		version, err := GetAppliedCRVersion(t.Context(), cl, cluster)
		require.ErrorIs(t, err, ErrRolloutInProgress)
		assert.Empty(t, version)
	})

	t.Run("returns empty string when the container has no CR version", func(t *testing.T) {
		sfs := newStatefulSet("1.3.0")
		for i, c := range sfs.Spec.Template.Spec.Containers {
			if c.Name == AppName {
				sfs.Spec.Template.Spec.Containers[i].Env = nil
			}
		}

		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			sfs,
			newPod(0, updateRevision, true),
			newPod(1, updateRevision, true),
			newPod(2, updateRevision, true),
		).Build()

		version, err := GetAppliedCRVersion(t.Context(), cl, cluster)
		require.NoError(t, err)
		assert.Empty(t, version)
	})

	t.Run("returns error when the statefulset is missing", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()

		version, err := GetAppliedCRVersion(t.Context(), cl, cluster)
		require.Error(t, err)
		assert.True(t, k8serrors.IsNotFound(err))
		assert.NotErrorIs(t, err, ErrRolloutInProgress)
		assert.Empty(t, version)
	})
}
