package ps

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql/metrics"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/testutil"
)

type mockExecClient struct {
	execFunc func(ctx context.Context, pod *corev1.Pod, containerName string, command []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error
}

func (m *mockExecClient) Exec(ctx context.Context, pod *corev1.Pod, containerName string, command []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
	if m.execFunc != nil {
		return m.execFunc(ctx, pod, containerName, command, stdin, stdout, stderr, tty)
	}
	return nil
}

func (m *mockExecClient) REST() restclient.Interface {
	return nil
}

func autoscalingCR(t *testing.T) *apiv1.PerconaServerMySQL {
	t.Helper()

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			MySQL: apiv1.MySQLSpec{
				VolumeSpec: &apiv1.VolumeSpec{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("2Gi"),
							},
						},
					},
				},
				PodSpec: apiv1.PodSpec{
					Size: 3,
				},
			},
			StorageScaling: &apiv1.StorageScalingSpec{
				EnableVolumeScaling: true,
				Autoscaling: &apiv1.AutoscalingSpec{
					Enabled:                 true,
					TriggerThresholdPercent: 80,
					GrowthStep:              resource.MustParse("2Gi"),
					MaxSize:                 resource.MustParse("10Gi"),
				},
			},
		},
	}

	return cr
}

func autoscalingPVC(cr *apiv1.PerconaServerMySQL, idx string, capacity string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "datadir-" + mysql.Name(cr) + "-" + idx,
			Namespace: cr.Namespace,
			Labels:    mysql.MatchLabels(cr),
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse(capacity),
			},
		},
	}
}

func autoscalingPod(cr *apiv1.PerconaServerMySQL, idx string, running bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mysql.Name(cr) + "-" + idx,
			Namespace: cr.Namespace,
			Labels:    mysql.MatchLabels(cr),
		},
	}
	if running {
		pod.Status = corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  mysql.AppName,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
			},
		}
	}
	return pod
}

func autoscalingSTS(cr *apiv1.PerconaServerMySQL) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mysql.Name(cr),
			Namespace: cr.Namespace,
		},
	}
}

func TestExtractPodNameFromPVC(t *testing.T) {
	tests := map[string]struct {
		pvcName  string
		stsName  string
		expected string
	}{
		"valid datadir PVC": {
			pvcName:  "datadir-test-cluster-mysql-0",
			stsName:  "test-cluster-mysql",
			expected: "test-cluster-mysql-0",
		},
		"valid datadir PVC with high index": {
			pvcName:  "datadir-test-cluster-mysql-12",
			stsName:  "test-cluster-mysql",
			expected: "test-cluster-mysql-12",
		},
		"not a datadir PVC": {
			pvcName:  "other-test-cluster-mysql-0",
			stsName:  "test-cluster-mysql",
			expected: "",
		},
		"different statefulset": {
			pvcName:  "datadir-other-cluster-mysql-0",
			stsName:  "test-cluster-mysql",
			expected: "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.expected, extractPodNameFromPVC(tt.pvcName, tt.stsName))
		})
	}
}

func TestFindPodByName(t *testing.T) {
	podList := &corev1.PodList{
		Items: []corev1.Pod{
			{ObjectMeta: metav1.ObjectMeta{Name: "pod-0"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "pod-1"}},
		},
	}

	assert.NotNil(t, findPodByName(podList, "pod-0"))
	assert.NotNil(t, findPodByName(podList, "pod-1"))
	assert.Nil(t, findPodByName(podList, "pod-2"))
}

func TestShouldTriggerResize(t *testing.T) {
	tests := map[string]struct {
		usagePercent int
		capacity     string
		maxSize      string
		conditions   []corev1.PersistentVolumeClaimCondition
		expected     bool
	}{
		"usage below threshold": {
			usagePercent: 50,
			capacity:     "2Gi",
			expected:     false,
		},
		"usage at threshold": {
			usagePercent: 80,
			capacity:     "2Gi",
			expected:     true,
		},
		"usage above threshold": {
			usagePercent: 90,
			capacity:     "2Gi",
			expected:     true,
		},
		"already at maxSize": {
			usagePercent: 90,
			capacity:     "10Gi",
			maxSize:      "10Gi",
			expected:     false,
		},
		"above maxSize": {
			usagePercent: 90,
			capacity:     "11Gi",
			maxSize:      "10Gi",
			expected:     false,
		},
		"resize already in progress": {
			usagePercent: 90,
			capacity:     "2Gi",
			conditions: []corev1.PersistentVolumeClaimCondition{
				{
					Type:   corev1.PersistentVolumeClaimResizing,
					Status: corev1.ConditionTrue,
				},
			},
			expected: false,
		},
		"filesystem resize pending": {
			usagePercent: 90,
			capacity:     "2Gi",
			conditions: []corev1.PersistentVolumeClaimCondition{
				{
					Type:   corev1.PersistentVolumeClaimFileSystemResizePending,
					Status: corev1.ConditionTrue,
				},
			},
			expected: false,
		},
		"resize condition not true": {
			usagePercent: 90,
			capacity:     "2Gi",
			conditions: []corev1.PersistentVolumeClaimCondition{
				{
					Type:   corev1.PersistentVolumeClaimResizing,
					Status: corev1.ConditionFalse,
				},
			},
			expected: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr := autoscalingCR(t)
			if tt.maxSize != "" {
				cr.Spec.StorageScaling.Autoscaling.MaxSize = resource.MustParse(tt.maxSize)
			}

			pvc := autoscalingPVC(cr, "0", tt.capacity)
			pvc.Status.Conditions = tt.conditions

			r := &PerconaServerMySQLReconciler{}
			usage := &metrics.PVCUsage{UsagePercent: tt.usagePercent}

			assert.Equal(t, tt.expected, r.shouldTriggerResize(context.Background(), cr, pvc, usage))
		})
	}
}

func TestCalculateNewSize(t *testing.T) {
	tests := map[string]struct {
		capacity   string
		growthStep string
		maxSize    string
		expected   string
	}{
		"normal growth": {
			capacity:   "2Gi",
			growthStep: "2Gi",
			maxSize:    "10Gi",
			expected:   "4Gi",
		},
		"growth capped at maxSize": {
			capacity:   "9Gi",
			growthStep: "2Gi",
			maxSize:    "10Gi",
			expected:   "10Gi",
		},
		"no maxSize": {
			capacity:   "9Gi",
			growthStep: "2Gi",
			maxSize:    "0",
			expected:   "11Gi",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr := autoscalingCR(t)
			cr.Spec.StorageScaling.Autoscaling.GrowthStep = resource.MustParse(tt.growthStep)
			cr.Spec.StorageScaling.Autoscaling.MaxSize = resource.MustParse(tt.maxSize)

			pvc := autoscalingPVC(cr, "0", tt.capacity)

			r := &PerconaServerMySQLReconciler{}
			newSize := r.calculateNewSize(cr, pvc)

			expected := resource.MustParse(tt.expected)
			assert.Equal(t, 0, newSize.Cmp(expected), "expected %s, got %s", expected.String(), newSize.String())
		})
	}
}

func TestReconcileStorageAutoscalingSkips(t *testing.T) {
	dfOutput := `Filesystem       1B-blocks       Used   Available Use% Mounted on
/dev/sdb        2147483648 1932735283   214748365  90% /var/lib/mysql`

	tests := map[string]struct {
		updateCR func(cr *apiv1.PerconaServerMySQL)
	}{
		"no storageScaling": {
			updateCR: func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.StorageScaling = nil
			},
		},
		"autoscaling disabled": {
			updateCR: func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.StorageScaling.Autoscaling.Enabled = false
			},
		},
		"external autoscaling enabled": {
			updateCR: func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.StorageScaling.VolumeExternalAutoscaling = true
			},
		},
		"volume expansion disabled": {
			updateCR: func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.StorageScaling.EnableVolumeScaling = false
			},
		},
		"no PVC spec": {
			updateCR: func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.MySQL.VolumeSpec = nil
			},
		},
		"resize already in progress": {
			updateCR: func(cr *apiv1.PerconaServerMySQL) {
				cr.Annotations = map[string]string{
					"percona.com/pvc-resize-in-progress": "2026-06-11T00:00:00Z",
				}
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr := autoscalingCR(t)
			tt.updateCR(cr)

			cl := testutil.BuildFakeClient(
				cr,
				autoscalingSTS(cr),
				autoscalingPVC(cr, "0", "2Gi"),
				autoscalingPod(cr, "0", true),
			)

			r := &PerconaServerMySQLReconciler{
				Client:        cl,
				ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
				ClientCmd: &mockExecClient{
					execFunc: func(ctx context.Context, pod *corev1.Pod, containerName string, command []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
						_, _ = stdout.Write([]byte(dfOutput))
						return nil
					},
				},
			}

			err := r.reconcileStorageAutoscaling(context.Background(), cr)
			require.NoError(t, err)

			// the CR storage request must not be touched
			if cr.Spec.MySQL.VolumeSpec != nil {
				storage := cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
				assert.Equal(t, 0, storage.Cmp(resource.MustParse("2Gi")))
			}
		})
	}
}

func TestReconcileStorageAutoscalingTriggersResize(t *testing.T) {
	// 90% usage of a 2Gi volume
	dfOutput := `Filesystem       1B-blocks       Used   Available Use% Mounted on
/dev/sdb        2147483648 1932735283   214748365  90% /var/lib/mysql`

	cr := autoscalingCR(t)

	cl := testutil.BuildFakeClient(
		cr,
		autoscalingSTS(cr),
		autoscalingPVC(cr, "0", "2Gi"),
		autoscalingPod(cr, "0", true),
	)

	r := &PerconaServerMySQLReconciler{
		Client:        cl,
		ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
		ClientCmd: &mockExecClient{
			execFunc: func(ctx context.Context, pod *corev1.Pod, containerName string, command []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
				_, _ = stdout.Write([]byte(dfOutput))
				return nil
			},
		},
		Recorder: record.NewFakeRecorder(100),
	}

	ctx := context.Background()
	require.NoError(t, r.reconcileStorageAutoscaling(ctx, cr))

	updatedCR := &apiv1.PerconaServerMySQL{}
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(cr), updatedCR))

	storage := updatedCR.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
	assert.Equal(t, 0, storage.Cmp(resource.MustParse("4Gi")), "expected 4Gi, got %s", storage.String())

	status := updatedCR.Status.StorageAutoscaling["datadir-"+mysql.Name(cr)+"-0"]
	assert.Equal(t, resource.NewQuantity(2147483648, resource.BinarySI).String(), status.CurrentSize)
	assert.Empty(t, status.LastError)
}

func TestReconcileStorageAutoscalingBelowThreshold(t *testing.T) {
	// 50% usage of a 2Gi volume
	dfOutput := `Filesystem       1B-blocks       Used   Available Use% Mounted on
/dev/sdb        2147483648 1073741824  1073741824  50% /var/lib/mysql`

	cr := autoscalingCR(t)

	cl := testutil.BuildFakeClient(
		cr,
		autoscalingSTS(cr),
		autoscalingPVC(cr, "0", "2Gi"),
		autoscalingPod(cr, "0", true),
	)

	r := &PerconaServerMySQLReconciler{
		Client:        cl,
		ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
		ClientCmd: &mockExecClient{
			execFunc: func(ctx context.Context, pod *corev1.Pod, containerName string, command []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
				_, _ = stdout.Write([]byte(dfOutput))
				return nil
			},
		},
	}

	ctx := context.Background()
	require.NoError(t, r.reconcileStorageAutoscaling(ctx, cr))

	updatedCR := &apiv1.PerconaServerMySQL{}
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(cr), updatedCR))

	storage := updatedCR.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
	assert.Equal(t, 0, storage.Cmp(resource.MustParse("2Gi")), "expected 2Gi, got %s", storage.String())

	// usage status is still tracked
	status := updatedCR.Status.StorageAutoscaling["datadir-"+mysql.Name(cr)+"-0"]
	assert.Equal(t, resource.NewQuantity(2147483648, resource.BinarySI).String(), status.CurrentSize)
}

func TestReconcileStorageAutoscalingPodNotRunning(t *testing.T) {
	cr := autoscalingCR(t)

	cl := testutil.BuildFakeClient(
		cr,
		autoscalingSTS(cr),
		autoscalingPVC(cr, "0", "2Gi"),
		autoscalingPod(cr, "0", false),
	)

	execCalled := false
	r := &PerconaServerMySQLReconciler{
		Client:        cl,
		ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
		ClientCmd: &mockExecClient{
			execFunc: func(ctx context.Context, pod *corev1.Pod, containerName string, command []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
				execCalled = true
				return nil
			},
		},
	}

	ctx := context.Background()
	require.NoError(t, r.reconcileStorageAutoscaling(ctx, cr))
	assert.False(t, execCalled, "df should not be executed when pod is not running")
}

func TestUpdateAutoscalingStatusResizeCount(t *testing.T) {
	cr := autoscalingCR(t)
	cl := testutil.BuildFakeClient(cr)

	r := &PerconaServerMySQLReconciler{
		Client:        cl,
		ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
	}

	ctx := context.Background()
	pvcName := "datadir-" + mysql.Name(cr) + "-0"

	// first observation
	r.updateAutoscalingStatus(ctx, cr, pvcName, &metrics.PVCUsage{TotalBytes: 2147483648}, nil)

	updatedCR := &apiv1.PerconaServerMySQL{}
	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(cr), updatedCR))
	status := updatedCR.Status.StorageAutoscaling[pvcName]
	assert.Equal(t, int32(0), status.ResizeCount)
	assert.True(t, status.LastResizeTime.IsZero())

	// same size observed again: no resize recorded
	r.updateAutoscalingStatus(ctx, cr, pvcName, &metrics.PVCUsage{TotalBytes: 2147483648}, nil)

	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(cr), updatedCR))
	status = updatedCR.Status.StorageAutoscaling[pvcName]
	assert.Equal(t, int32(0), status.ResizeCount)

	// larger size observed: resize recorded
	r.updateAutoscalingStatus(ctx, cr, pvcName, &metrics.PVCUsage{TotalBytes: 4294967296}, nil)

	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(cr), updatedCR))
	status = updatedCR.Status.StorageAutoscaling[pvcName]
	assert.Equal(t, int32(1), status.ResizeCount)
	assert.False(t, status.LastResizeTime.IsZero())

	// error observed: last error is recorded, current size is kept
	r.updateAutoscalingStatus(ctx, cr, pvcName, nil, assert.AnError)

	require.NoError(t, cl.Get(ctx, client.ObjectKeyFromObject(cr), updatedCR))
	status = updatedCR.Status.StorageAutoscaling[pvcName]
	assert.Equal(t, assert.AnError.Error(), status.LastError)
	assert.Equal(t, resource.NewQuantity(4294967296, resource.BinarySI).String(), status.CurrentSize)
}
