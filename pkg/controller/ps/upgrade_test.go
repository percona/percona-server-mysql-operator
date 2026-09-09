package ps

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

func readDefaultCRForUpgrade(name, namespace string) *apiv1.PerconaServerMySQL {
	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			MySQL: apiv1.MySQLSpec{
				ClusterType: apiv1.ClusterTypeGR,
				PodSpec: apiv1.PodSpec{
					Size: 3,
				},
			},
		},
	}
	return cr
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, apiv1.AddToScheme(s))
	return s
}

func readyPod(name, namespace string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
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
}

func notReadyPod(name, namespace string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}
}

const (
	oldRev = "rev-1"
	newRev = "rev-2"
)

func newRevisionPod(name, namespace, revision string, selector map[string]string) *corev1.Pod {
	pod := readyPod(name, namespace)
	pod.Labels = map[string]string{controllerRevisionHash: revision}
	maps.Copy(pod.Labels, selector)
	return &pod
}

func TestSelectPrimaryCandidate(t *testing.T) {
	now := metav1.Now()

	tests := []struct {
		name      string
		pods      []corev1.Pod
		wantName  string
		wantError bool
	}{
		{
			name:     "single ready pod",
			pods:     []corev1.Pod{readyPod("pod-0", "ns")},
			wantName: "pod-0",
		},
		{
			name: "first not ready, second ready",
			pods: []corev1.Pod{
				notReadyPod("pod-0", "ns"),
				readyPod("pod-1", "ns"),
			},
			wantName: "pod-1",
		},
		{
			name:      "no pods",
			pods:      []corev1.Pod{},
			wantError: true,
		},
		{
			name: "all not ready",
			pods: []corev1.Pod{
				notReadyPod("pod-0", "ns"),
				notReadyPod("pod-1", "ns"),
			},
			wantError: true,
		},
		{
			name: "pod being deleted",
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "pod-0",
						Namespace:         "ns",
						DeletionTimestamp: &now,
						Finalizers:        []string{"test"}, // required for DeletionTimestamp to be set
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
						},
					},
				},
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectPrimaryCandidate(tt.pods)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, got.Name)
		})
	}
}

func TestStsChanged(t *testing.T) {
	makePod := func(revision string) corev1.Pod {
		labels := map[string]string{}
		if revision != "" {
			labels["controller-revision-hash"] = revision
		}
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
		}
	}

	tests := []struct {
		name string
		sts  *appsv1.StatefulSet
		pods []corev1.Pod
		want bool
	}{
		{
			name: "all pods match revision",
			sts:  &appsv1.StatefulSet{Status: appsv1.StatefulSetStatus{UpdateRevision: "rev-2"}},
			pods: []corev1.Pod{makePod("rev-2"), makePod("rev-2")},
			want: false,
		},
		{
			name: "one pod differs",
			sts:  &appsv1.StatefulSet{Status: appsv1.StatefulSetStatus{UpdateRevision: "rev-2"}},
			pods: []corev1.Pod{makePod("rev-1"), makePod("rev-2")},
			want: true,
		},
		{
			name: "no pods",
			sts:  &appsv1.StatefulSet{Status: appsv1.StatefulSetStatus{UpdateRevision: "rev-2"}},
			pods: []corev1.Pod{},
			want: false,
		},
		{
			name: "pod missing label",
			sts:  &appsv1.StatefulSet{Status: appsv1.StatefulSetStatus{UpdateRevision: "rev-2"}},
			pods: []corev1.Pod{makePod("")},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stsChanged(tt.sts, tt.pods)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDeleteOutdatedStuckPods(t *testing.T) {
	s := newScheme(t)

	newPod := func(revision string, phase corev1.PodPhase, isCrashLoopBackOff bool) corev1.Pod {
		pod := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mysql-0",
				Namespace: "test-ns",
				Labels: map[string]string{
					controllerRevisionHash: revision,
				},
			},
			Status: corev1.PodStatus{Phase: phase},
		}
		if isCrashLoopBackOff {
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: crashLoopBackOffReason},
				},
			}}
		}
		return pod
	}

	terminatingPod := newPod("rev-1", corev1.PodPending, false)
	terminatingPod.DeletionTimestamp = new(metav1.Now())
	terminatingPod.Finalizers = []string{"test"}

	tests := []struct {
		name           string
		pod            corev1.Pod
		updateRevision string
		wantDeleted    bool
	}{
		{
			name:           "empty update revision",
			pod:            newPod("rev-1", corev1.PodPending, false),
			updateRevision: "",
			wantDeleted:    false,
		},
		{
			name:           "outdated pending pod",
			pod:            newPod("rev-1", corev1.PodPending, false),
			updateRevision: "rev-2",
			wantDeleted:    true,
		},
		{
			name:           "updated pending pod",
			pod:            newPod("rev-2", corev1.PodPending, false),
			updateRevision: "rev-2",
			wantDeleted:    false,
		},
		{
			name:           "outdated running pod",
			pod:            newPod("rev-1", corev1.PodRunning, false),
			updateRevision: "rev-2",
			wantDeleted:    false,
		},
		{
			name:           "outdated pod in CrashLoopBackOff",
			pod:            newPod("rev-1", corev1.PodRunning, true),
			updateRevision: "rev-2",
			wantDeleted:    true,
		},
		{
			name:           "updated pod in CrashLoopBackOff",
			pod:            newPod("rev-2", corev1.PodRunning, true),
			updateRevision: "rev-2",
			wantDeleted:    false,
		},
		{
			name:           "outdated pending pod already terminating",
			pod:            terminatingPod,
			updateRevision: "rev-2",
			wantDeleted:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := tt.pod.DeepCopy()
			cli := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()

			err := deleteOutdatedStuckPods(t.Context(), cli, []corev1.Pod{*pod}, tt.updateRevision)
			require.NoError(t, err)

			err = cli.Get(t.Context(), types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, &corev1.Pod{})
			if tt.wantDeleted {
				assert.True(t, k8serrors.IsNotFound(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSmartUpdateDeletesOutdatedPendingPod(t *testing.T) {
	cr := readDefaultCRForUpgrade("test-cluster", "test-ns")

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mysql",
			Namespace: cr.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "mysql"},
			},
		},
		Status: appsv1.StatefulSetStatus{
			Replicas:       1,
			ReadyReplicas:  0,
			UpdateRevision: "rev-2",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mysql-0",
			Namespace: cr.Namespace,
			Labels: map[string]string{
				"app":                  "mysql",
				controllerRevisionHash: "rev-1",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}

	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(sts, pod).Build()
	r := &PerconaServerMySQLReconciler{Client: cli}

	require.NoError(t, r.smartUpdate(t.Context(), sts, cr))
	err := cli.Get(t.Context(), client.ObjectKeyFromObject(pod), &corev1.Pod{})
	assert.True(t, k8serrors.IsNotFound(err))
}

func TestSwitchOverGR(t *testing.T) {
	cr := readDefaultCRForUpgrade("test-cluster", "test-ns")
	cr.Spec.MySQL.ClusterType = apiv1.ClusterTypeGR
	s := newScheme(t)

	primary := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: mysql.PodName(cr, 0), Namespace: cr.Namespace},
	}
	target := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: mysql.PodName(cr, 1), Namespace: cr.Namespace},
	}

	operatorPassword := "test-pass"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.InternalSecretName(),
			Namespace: cr.Namespace,
		},
		Data: map[string][]byte{
			string(apiv1.UserOperator): []byte(operatorPassword),
		},
	}

	primaryFQDN := mysql.PodFQDN(cr, primary)
	targetFQDN := mysql.PodFQDN(cr, target)
	expectedURI := fmt.Sprintf("%s:%s@%s", apiv1.UserOperator, operatorPassword, primaryFQDN)
	expectedCmd := fmt.Sprintf("dba.getCluster('%s').setPrimaryInstance('%s')", cr.InnoDBClusterName(), targetFQDN)

	t.Run("success", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(s).WithObjects(secret).Build()
		fc := &fakeClient{
			disableCheck: false,
			scripts: []fakeClientScript{
				{
					cmd: []string{"mysqlsh", "--js", "--no-wizard", "--uri", expectedURI, "-e", expectedCmd},
				},
			},
		}
		r := &PerconaServerMySQLReconciler{Client: cli, Scheme: s, ClientCmd: fc}

		err := r.switchOverGR(t.Context(), cr, primary, target)
		require.NoError(t, err)
		assert.Equal(t, 1, fc.execCount)
	})

	t.Run("mysqlsh exec fails", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(s).WithObjects(secret).Build()
		fc := &fakeClient{
			disableCheck: true,
			scripts: []fakeClientScript{
				{err: fmt.Errorf("exec failed")},
			},
		}
		r := &PerconaServerMySQLReconciler{Client: cli, Scheme: s, ClientCmd: fc}

		err := r.switchOverGR(t.Context(), cr, primary, target)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "set primary instance")
	})
}

func TestSwitchOverAsync(t *testing.T) {
	cr := readDefaultCRForUpgrade("test-cluster", "test-ns")
	cr.Spec.MySQL.ClusterType = apiv1.ClusterTypeAsync
	s := newScheme(t)

	target := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: mysql.PodName(cr, 1), Namespace: cr.Namespace},
	}
	primary := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: mysql.PodName(cr, 0), Namespace: cr.Namespace},
	}

	makeOrcPod := func(ready bool) *corev1.Pod {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      orchestrator.PodName(cr, 0),
				Namespace: cr.Namespace,
				Labels:    orchestrator.MatchLabels(cr),
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		}
		if ready {
			pod.Status.Conditions = []corev1.PodCondition{
				{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
			}
		}
		return pod
	}

	// EnsureNodeIsPrimary first calls ClusterPrimary which does:
	//   curl localhost:3000/api/master/<clusterHint>
	// If primary.Alias != target, it then calls:
	//   curl localhost:3000/api/graceful-master-takeover-auto/<clusterHint>/<targetName>/<port>
	clusterHint := cr.ClusterHint()

	// ClusterPrimary response: return an Instance where Alias != target (so switchover is triggered)
	clusterPrimaryResp, _ := json.Marshal(orchestrator.Instance{
		Key:   orchestrator.InstanceKey{Hostname: primary.Name},
		Alias: primary.Name,
	})

	// Graceful takeover response
	takeoverResp, _ := json.Marshal(orchestrator.Instance{
		Key:   orchestrator.InstanceKey{Hostname: target.Name},
		Alias: target.Name,
	})

	t.Run("success", func(t *testing.T) {
		orcPod := makeOrcPod(true)
		cli := fake.NewClientBuilder().WithScheme(s).WithObjects(orcPod).Build()
		fc := &fakeClient{
			disableCheck: false,
			scripts: []fakeClientScript{
				{
					cmd:    []string{"sh", "-c", fmt.Sprintf(`curl -s -u "%s:$(cat %s/%s)" "localhost:3000/api/master/%s"`, apiv1.UserOrchestrator, orchestrator.CredsMountPath, apiv1.UserOrchestrator, clusterHint)},
					stdout: clusterPrimaryResp,
				},
				{
					cmd:    []string{"sh", "-c", fmt.Sprintf(`curl -s -u "%s:$(cat %s/%s)" "localhost:3000/api/graceful-master-takeover-auto/%s/%s/%d"`, apiv1.UserOrchestrator, orchestrator.CredsMountPath, apiv1.UserOrchestrator, clusterHint, target.GetName(), mysql.DefaultPort)},
					stdout: takeoverResp,
				},
			},
		}
		r := &PerconaServerMySQLReconciler{
			Client:    cli,
			Scheme:    s,
			ClientCmd: fc,
			ServerVersion: &platform.ServerVersion{
				Platform: platform.PlatformKubernetes,
			},
			Recorder: new(record.FakeRecorder),
		}

		err := r.switchOverAsync(t.Context(), cr, target)
		require.NoError(t, err)
		assert.Equal(t, 2, fc.execCount)
	})

	t.Run("no ready orc pods", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(s).Build()
		r := &PerconaServerMySQLReconciler{
			Client: cli,
			Scheme: s,
			ServerVersion: &platform.ServerVersion{
				Platform: platform.PlatformKubernetes,
			},
			Recorder: new(record.FakeRecorder),
		}

		err := r.switchOverAsync(t.Context(), cr, target)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get ready orchestrator pod")
	})

	t.Run("target is already primary", func(t *testing.T) {
		// ClusterPrimary returns target as the current primary — no takeover needed
		alreadyPrimaryResp, _ := json.Marshal(orchestrator.Instance{
			Key:   orchestrator.InstanceKey{Hostname: target.Name},
			Alias: target.Name,
		})

		orcPod := makeOrcPod(true)
		cli := fake.NewClientBuilder().WithScheme(s).WithObjects(orcPod).Build()
		fc := &fakeClient{
			disableCheck: false,
			scripts: []fakeClientScript{
				{
					cmd:    []string{"sh", "-c", fmt.Sprintf(`curl -s -u "%s:$(cat %s/%s)" "localhost:3000/api/master/%s"`, apiv1.UserOrchestrator, orchestrator.CredsMountPath, apiv1.UserOrchestrator, clusterHint)},
					stdout: alreadyPrimaryResp,
				},
			},
		}
		r := &PerconaServerMySQLReconciler{
			Client:    cli,
			Scheme:    s,
			ClientCmd: fc,
			ServerVersion: &platform.ServerVersion{
				Platform: platform.PlatformKubernetes,
			},
			Recorder: new(record.FakeRecorder),
		}

		err := r.switchOverAsync(t.Context(), cr, target)
		require.NoError(t, err)
		assert.Equal(t, 1, fc.execCount) // only ClusterPrimary was called
	})

	t.Run("exec fails", func(t *testing.T) {
		orcPod := makeOrcPod(true)
		cli := fake.NewClientBuilder().WithScheme(s).WithObjects(orcPod).Build()
		fc := &fakeClient{
			disableCheck: true,
			scripts: []fakeClientScript{
				{err: fmt.Errorf("connection refused")},
			},
		}
		r := &PerconaServerMySQLReconciler{
			Client:    cli,
			Scheme:    s,
			ClientCmd: fc,
			ServerVersion: &platform.ServerVersion{
				Platform: platform.PlatformKubernetes,
			},
			Recorder: new(record.FakeRecorder),
		}

		err := r.switchOverAsync(t.Context(), cr, target)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ensure node is primary")
	})
}

func TestSwitchOverAndWait(t *testing.T) {
	s := newScheme(t)

	makeSecret := func(cr *apiv1.PerconaServerMySQL) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cr.InternalSecretName(),
				Namespace: cr.Namespace,
			},
			Data: map[string][]byte{
				string(apiv1.UserOperator): []byte("test-pass"),
			},
		}
	}

	makeReadyOrcPod := func(cr *apiv1.PerconaServerMySQL) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      orchestrator.PodName(cr, 0),
				Namespace: cr.Namespace,
				Labels:    orchestrator.MatchLabels(cr),
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
				},
			},
		}
	}

	t.Run("GR errPrimaryNotTheLowest falls back without waiting", func(t *testing.T) {
		cr := readDefaultCRForUpgrade("test-cluster", "test-ns")
		cr.Spec.MySQL.ClusterType = apiv1.ClusterTypeGR

		primary := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: mysql.PodName(cr, 0), Namespace: cr.Namespace}}
		target := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: mysql.PodName(cr, 1), Namespace: cr.Namespace}}

		cli := fake.NewClientBuilder().WithScheme(s).WithObjects(makeSecret(cr)).Build()
		fc := &fakeClient{
			disableCheck: true,
			scripts: []fakeClientScript{
				{err: fmt.Errorf("ERROR: The appointed primary member is not the lowest version in the group.")},
			},
		}
		r := &PerconaServerMySQLReconciler{Client: cli, Scheme: s, ClientCmd: fc}

		err := r.switchOverAndWait(t.Context(), cr, primary, target)
		require.NoError(t, err)
		// Only the switchOverGR call should run; the wait loop and label
		// reconcile must be skipped on the failover fallback path.
		assert.Equal(t, 1, fc.execCount)
	})

	t.Run("Async waits for new primary", func(t *testing.T) {
		cr := readDefaultCRForUpgrade("test-cluster", "test-ns")
		cr.Spec.MySQL.ClusterType = apiv1.ClusterTypeAsync

		primary := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: mysql.PodName(cr, 0), Namespace: cr.Namespace}}
		target := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: mysql.PodName(cr, 1), Namespace: cr.Namespace}}

		clusterHint := cr.ClusterHint()

		oldPrimaryResp, _ := json.Marshal(orchestrator.Instance{
			Key:   orchestrator.InstanceKey{Hostname: primary.Name},
			Alias: primary.Name,
		})
		takeoverResp, _ := json.Marshal(orchestrator.Instance{
			Key:   orchestrator.InstanceKey{Hostname: target.Name},
			Alias: target.Name,
		})
		newPrimaryResp, _ := json.Marshal(orchestrator.Instance{
			Key:   orchestrator.InstanceKey{Hostname: target.Name},
			Alias: target.Name,
		})

		cli := fake.NewClientBuilder().WithScheme(s).WithObjects(makeReadyOrcPod(cr)).Build()
		fc := &fakeClient{
			scripts: []fakeClientScript{
				{
					cmd:    []string{"sh", "-c", fmt.Sprintf(`curl -s -u "%s:$(cat %s/%s)" "localhost:3000/api/master/%s"`, apiv1.UserOrchestrator, orchestrator.CredsMountPath, apiv1.UserOrchestrator, clusterHint)},
					stdout: oldPrimaryResp,
				},
				{
					cmd:    []string{"sh", "-c", fmt.Sprintf(`curl -s -u "%s:$(cat %s/%s)" "localhost:3000/api/graceful-master-takeover-auto/%s/%s/%d"`, apiv1.UserOrchestrator, orchestrator.CredsMountPath, apiv1.UserOrchestrator, clusterHint, target.GetName(), mysql.DefaultPort)},
					stdout: takeoverResp,
				},
				{
					cmd:    []string{"sh", "-c", fmt.Sprintf(`curl -s -u "%s:$(cat %s/%s)" "localhost:3000/api/master/%s"`, apiv1.UserOrchestrator, orchestrator.CredsMountPath, apiv1.UserOrchestrator, clusterHint)},
					stdout: newPrimaryResp,
				},
			},
		}
		r := &PerconaServerMySQLReconciler{
			Client:    cli,
			Scheme:    s,
			ClientCmd: fc,
			ServerVersion: &platform.ServerVersion{
				Platform: platform.PlatformKubernetes,
			},
			Recorder: new(record.FakeRecorder),
		}

		err := r.switchOverAndWait(t.Context(), cr, primary, target)
		require.NoError(t, err)
		// 2 calls for switchOverAsync + 1 call for getPrimaryHost in the wait loop.
		assert.Equal(t, 3, fc.execCount)
	})

	t.Run("GR assigns primary label to target", func(t *testing.T) {
		cr := readDefaultCRForUpgrade("test-cluster", "test-ns")
		cr.Spec.MySQL.ClusterType = apiv1.ClusterTypeGR

		mysqlLabels := mysql.MatchLabels(cr)
		makeMysqlPod := func(name string) *corev1.Pod {
			return &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: cr.Namespace,
					Labels:    mysqlLabels,
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
					},
				},
			}
		}
		primary := makeMysqlPod(mysql.PodName(cr, 0))
		target := makeMysqlPod(mysql.PodName(cr, 1))

		// CSV result for the GetGroupReplicationPrimary query — a single
		// "host" column whose value is the target pod's FQDN. The wait loop
		// splits this on "." and compares the first segment to target.Name.
		targetFQDN := mysql.PodFQDN(cr, target)
		primaryCSV := []byte("host\n" + targetFQDN + "\n")

		cli := fake.NewClientBuilder().WithScheme(s).
			WithObjects(makeSecret(cr), primary, target).Build()
		fc := &fakeClient{
			disableCheck: true,
			scripts: []fakeClientScript{
				// 1) switchOverGR -> mysqlsh setPrimaryInstance
				{},
				// 2) wait-loop getPrimaryHost -> ReplicationManager.GetGroupReplicationPrimary
				{stdout: primaryCSV},
				// 3) reconcileGRMySQLPrimaryLabel -> topology.GroupReplication ->
				//    GetGroupReplicationReplicas. Empty stdout is reported as
				//    sql.ErrNoRows by query() and tolerated by the caller.
				{},
				// 4) topology.GroupReplication -> GetGroupReplicationPrimary
				{stdout: primaryCSV},
			},
		}
		r := &PerconaServerMySQLReconciler{Client: cli, Scheme: s, ClientCmd: fc}

		err := r.switchOverAndWait(t.Context(), cr, primary, target)
		require.NoError(t, err)
		// 1 mysqlsh switchover + 1 wait-loop primary query
		// + 2 topology queries (replicas, primary) for label reconcile.
		assert.Equal(t, 4, fc.execCount)

		// reconcileGRMySQLPrimaryLabel should have stamped the target pod with the primary label.
		updated := &corev1.Pod{}
		require.NoError(t, cli.Get(t.Context(), types.NamespacedName{Name: target.Name, Namespace: target.Namespace}, updated))
		assert.Equal(t, "true", updated.Labels[naming.LabelMySQLPrimary])

		// reconcileGRMySQLPrimaryLabel should have removed the primary label from the old primary.
		oldPrimary := &corev1.Pod{}
		require.NoError(t, cli.Get(t.Context(), types.NamespacedName{Name: primary.Name, Namespace: primary.Namespace}, oldPrimary))
		assert.NotContains(t, oldPrimary.Labels, naming.LabelMySQLPrimary)
	})

	t.Run("Async wait loop fails when getPrimaryHost errors non-retriably", func(t *testing.T) {
		cr := readDefaultCRForUpgrade("test-cluster", "test-ns")
		cr.Spec.MySQL.ClusterType = apiv1.ClusterTypeAsync

		primary := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: mysql.PodName(cr, 0), Namespace: cr.Namespace}}
		target := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: mysql.PodName(cr, 1), Namespace: cr.Namespace}}

		oldPrimaryResp, _ := json.Marshal(orchestrator.Instance{
			Key:   orchestrator.InstanceKey{Hostname: primary.Name},
			Alias: primary.Name,
		})
		takeoverResp, _ := json.Marshal(orchestrator.Instance{
			Key:   orchestrator.InstanceKey{Hostname: target.Name},
			Alias: target.Name,
		})

		cli := fake.NewClientBuilder().WithScheme(s).WithObjects(makeReadyOrcPod(cr)).Build()
		fc := &fakeClient{
			disableCheck: true,
			scripts: []fakeClientScript{
				{stdout: oldPrimaryResp},
				{stdout: takeoverResp},
				// Wait-loop ClusterPrimary call fails with a non-retriable
				// error so the loop exits immediately instead of polling.
				{err: fmt.Errorf("connection refused")},
			},
		}
		r := &PerconaServerMySQLReconciler{
			Client:    cli,
			Scheme:    s,
			ClientCmd: fc,
			ServerVersion: &platform.ServerVersion{
				Platform: platform.PlatformKubernetes,
			},
			Recorder: new(record.FakeRecorder),
		}

		err := r.switchOverAndWait(t.Context(), cr, primary, target)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "wait for new primary")
		assert.Equal(t, 3, fc.execCount)
	})
}

func TestOrchestratorRaftLeader(t *testing.T) {
	cr := readDefaultCRForUpgrade("test-cluster", "test-ns")

	raftStateCmd := []string{"sh", "-c", fmt.Sprintf(`curl -s -u "%s:$(cat %s/%s)" "localhost:3000/api/raft-state"`, apiv1.UserOrchestrator, orchestrator.CredsMountPath, apiv1.UserOrchestrator)}
	leaderResp, _ := json.Marshal(orchestrator.RaftStateLeader)
	followerResp, _ := json.Marshal("Follower")

	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: orchestrator.PodName(cr, 0), Namespace: cr.Namespace}},
		{ObjectMeta: metav1.ObjectMeta{Name: orchestrator.PodName(cr, 1), Namespace: cr.Namespace}},
		{ObjectMeta: metav1.ObjectMeta{Name: orchestrator.PodName(cr, 2), Namespace: cr.Namespace}},
	}

	tests := []struct {
		name        string
		scripts     []fakeClientScript
		expected    string
		expectedErr string
	}{
		{
			name: "single leader",
			scripts: []fakeClientScript{
				{cmd: raftStateCmd, stdout: followerResp},
				{cmd: raftStateCmd, stdout: leaderResp},
				{cmd: raftStateCmd, stdout: followerResp},
			},
			expected: orchestrator.PodName(cr, 1),
		},
		{
			name: "no leader",
			scripts: []fakeClientScript{
				{cmd: raftStateCmd, stdout: followerResp},
				{cmd: raftStateCmd, stdout: followerResp},
				{cmd: raftStateCmd, stdout: followerResp},
			},
		},
		{
			name: "unreachable pods are skipped",
			scripts: []fakeClientScript{
				{cmd: raftStateCmd, err: fmt.Errorf("connection refused")},
				{cmd: raftStateCmd, err: fmt.Errorf("connection refused")},
				{cmd: raftStateCmd, stdout: leaderResp},
			},
			expected: orchestrator.PodName(cr, 2),
		},
		{
			name: "multiple leaders",
			scripts: []fakeClientScript{
				{cmd: raftStateCmd, stdout: leaderResp},
				{cmd: raftStateCmd, stdout: leaderResp},
				{cmd: raftStateCmd, stdout: followerResp},
			},
			expectedErr: "multiple pods report being the Raft leader",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := &fakeClient{scripts: tt.scripts}

			leader, err := orchestratorRaftLeader(t.Context(), fc, pods)
			if tt.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
				return
			}
			require.NoError(t, err)
			if tt.expected == "" {
				assert.Nil(t, leader)
				return
			}
			require.NotNil(t, leader)
			assert.Equal(t, tt.expected, leader.Name)
		})
	}
}

func TestPodToUpdate(t *testing.T) {
	pod := func(name, revision string) corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{controllerRevisionHash: revision},
			},
		}
	}

	tests := []struct {
		name     string
		pods     []corev1.Pod
		lastName string
		expected string
	}{
		{
			name:     "all pods updated",
			pods:     []corev1.Pod{pod("p-0", "rev-2"), pod("p-1", "rev-2")},
			lastName: "p-0",
		},
		{
			name:     "first outdated pod is picked",
			pods:     []corev1.Pod{pod("p-0", "rev-1"), pod("p-1", "rev-1")},
			lastName: "p-1",
			expected: "p-0",
		},
		{
			name:     "last pod is skipped while others are outdated",
			pods:     []corev1.Pod{pod("p-0", "rev-1"), pod("p-1", "rev-1")},
			lastName: "p-0",
			expected: "p-1",
		},
		{
			name:     "last pod is picked when it's the only outdated one",
			pods:     []corev1.Pod{pod("p-0", "rev-1"), pod("p-1", "rev-2")},
			lastName: "p-0",
			expected: "p-0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := podToUpdate(tt.pods, tt.lastName, "rev-2")
			if tt.expected == "" {
				assert.Nil(t, p)
				return
			}
			require.NotNil(t, p)
			assert.Equal(t, tt.expected, p.Name)
		})
	}
}

// recreatingClient records deleted pods and recreates them at revision
type recreatingClient struct {
	client.WithWatch

	revision string
	deleted  []string
}

func (c *recreatingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if err := c.WithWatch.Delete(ctx, obj, opts...); err != nil {
		return err
	}

	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	recreated := pod.DeepCopy()
	recreated.ResourceVersion = ""
	recreated.UID = ""
	recreated.Labels[controllerRevisionHash] = c.revision
	recreated.Status.Phase = corev1.PodRunning
	recreated.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
	}
	if err := c.WithWatch.Create(ctx, recreated); err != nil {
		return fmt.Errorf("recreate pod %s: %w", pod.Name, err)
	}

	c.deleted = append(c.deleted, pod.Name)

	return nil
}

func TestSmartUpdateOrchestrator(t *testing.T) {
	cr := readDefaultCRForUpgrade("test-cluster", "test-ns")
	s := newScheme(t)

	raftState := func(state string) []byte {
		b, _ := json.Marshal(state)
		return b
	}

	newSts := func(component string) *appsv1.StatefulSet {
		stsName := component
		selector := map[string]string{"app": component}
		if component == "" {
			component = naming.ComponentOrchestrator
			stsName = orchestrator.Name(cr)
			selector = orchestrator.MatchLabels(cr)
		}

		return &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      stsName,
				Namespace: cr.Namespace,
				Labels:    map[string]string{naming.LabelComponent: component},
			},
			Spec: appsv1.StatefulSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: selector},
			},
			Status: appsv1.StatefulSetStatus{
				UpdateRevision: newRev,
			},
		}
	}

	newPod := func(sts *appsv1.StatefulSet, idx int, revision string) *corev1.Pod {
		return newRevisionPod(orchestrator.PodName(cr, idx), sts.Namespace, revision, sts.Spec.Selector.MatchLabels)
	}

	tests := []struct {
		name        string
		pause       bool
		sts         *appsv1.StatefulSet
		revisions   []string
		raftStates  []string
		wantErr     string
		wantDeleted []string
	}{
		{
			name:      "paused cluster is not updated",
			pause:     true,
			sts:       newSts(""),
			revisions: []string{oldRev},
		},
		{
			name:      "missing statefulset is ignored",
			revisions: []string{oldRev},
		},
		{
			name:      "up to date statefulset is not updated",
			sts:       newSts(""),
			revisions: []string{newRev},
		},
		{
			name:      "unsupported component",
			sts:       newSts("haproxy"),
			revisions: []string{oldRev},
			wantErr:   `smart update is not supported for component "haproxy"`,
		},
		{
			name:       "nothing is updated when the Raft leader is unknown",
			sts:        newSts(""),
			revisions:  []string{oldRev, oldRev, oldRev},
			raftStates: []string{"Follower", "Follower", "Follower"},
		},
		{
			name:        "the first outdated follower is updated before the Raft leader",
			sts:         newSts(""),
			revisions:   []string{oldRev, oldRev, oldRev},
			raftStates:  []string{orchestrator.RaftStateLeader, "Follower", "Follower"},
			wantDeleted: []string{orchestrator.PodName(cr, 1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := cr.DeepCopy()
			cr.Spec.Pause = tt.pause

			var objs []client.Object

			sts := newSts("")
			if tt.sts != nil {
				sts = tt.sts.DeepCopy()
				sts.Status.Replicas = int32(len(tt.revisions))
				sts.Status.ReadyReplicas = int32(len(tt.revisions))

				objs = append(objs, sts)
				for i, revision := range tt.revisions {
					objs = append(objs, newPod(sts, i, revision))
				}
			}

			fc := &fakeClient{disableCheck: true}
			for _, state := range tt.raftStates {
				fc.scripts = append(fc.scripts, fakeClientScript{stdout: raftState(state)})
			}

			cli := &recreatingClient{
				WithWatch: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(),
				revision:  newRev,
			}

			r := &PerconaServerMySQLReconciler{Client: cli, Scheme: s, ClientCmd: fc}

			err := r.smartUpdate(t.Context(), sts, cr)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantDeleted, cli.deleted)
		})
	}

	t.Run("the Raft leader is updated last", func(t *testing.T) {
		sts := newSts("")
		sts.Status.Replicas = 3
		sts.Status.ReadyReplicas = 3

		objs := []client.Object{
			sts,
			newPod(sts, 0, oldRev),
			newPod(sts, 1, oldRev),
			newPod(sts, 2, oldRev),
		}
		cli := &recreatingClient{
			WithWatch: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(),
			revision:  newRev,
		}

		fc := &fakeClient{
			disableCheck: true,
			scripts: []fakeClientScript{
				{stdout: raftState(orchestrator.RaftStateLeader)},
				{stdout: raftState("Follower")},
				{stdout: raftState("Follower")},
			},
		}
		r := &PerconaServerMySQLReconciler{Client: cli, Scheme: s, ClientCmd: fc}

		require.NoError(t, r.smartUpdate(t.Context(), sts, cr))

		pod := &corev1.Pod{}
		key := types.NamespacedName{Name: orchestrator.PodName(cr, 2), Namespace: cr.Namespace}
		require.NoError(t, cli.Get(t.Context(), key, pod))
		pod.Labels[controllerRevisionHash] = newRev
		require.NoError(t, cli.Update(t.Context(), pod))
		fc.execCount = 0

		require.NoError(t, r.smartUpdate(t.Context(), sts, cr))

		assert.Equal(t,
			[]string{orchestrator.PodName(cr, 1), orchestrator.PodName(cr, 0)},
			cli.deleted)
	})
}

func TestSmartUpdateMySQL(t *testing.T) {
	cr := readDefaultCRForUpgrade("test-cluster", "test-ns")
	s := newScheme(t)

	selector := mysql.MatchLabels(cr)

	newPod := func(idx int, revision string) *corev1.Pod {
		return newRevisionPod(mysql.PodName(cr, idx), cr.Namespace, revision, selector)
	}

	readyOrcPod := newRevisionPod(orchestrator.PodName(cr, 0), cr.Namespace, newRev, orchestrator.MatchLabels(cr))

	newSts := func(replicas int32) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mysql.Name(cr),
				Namespace: cr.Namespace,
				Labels:    map[string]string{naming.LabelComponent: naming.ComponentDatabase},
			},
			Spec: appsv1.StatefulSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: selector},
			},
			Status: appsv1.StatefulSetStatus{
				Replicas:       replicas,
				ReadyReplicas:  replicas,
				UpdateRevision: newRev,
			},
		}
	}

	operatorSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.InternalSecretName(),
			Namespace: cr.Namespace,
		},
		Data: map[string][]byte{
			string(apiv1.UserOperator): []byte("test-pass"),
		},
	}

	runningBackup := &apiv1.PerconaServerMySQLBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "backup1", Namespace: cr.Namespace},
		Spec:       apiv1.PerconaServerMySQLBackupSpec{ClusterName: cr.Name},
		Status:     apiv1.PerconaServerMySQLBackupStatus{State: apiv1.BackupRunning},
	}

	primaryFromOrchestrator := func(idx int) []byte {
		b, _ := json.Marshal(orchestrator.Instance{
			Key:   orchestrator.InstanceKey{Hostname: mysql.PodName(cr, idx)},
			Alias: mysql.PodName(cr, idx),
		})
		return b
	}

	primaryFromGR := func(idx int) []byte {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: mysql.PodName(cr, idx)}}
		return []byte("host\n" + mysql.PodFQDN(cr, pod) + "\n")
	}

	tests := []struct {
		name          string
		clusterType   apiv1.ClusterType
		backupRunning bool
		revisions     []string
		scripts       []fakeClientScript
		wantDeleted   []string
	}{
		{
			name:        "async: a secondary is updated without switchover",
			clusterType: apiv1.ClusterTypeAsync,
			revisions:   []string{oldRev, oldRev, oldRev},
			scripts: []fakeClientScript{
				{stdout: primaryFromOrchestrator(0)},
			},
			wantDeleted: []string{mysql.PodName(cr, 1)},
		},
		{
			name:        "async: primary is switched over before it's updated",
			clusterType: apiv1.ClusterTypeAsync,
			revisions:   []string{oldRev, newRev, newRev},
			scripts: []fakeClientScript{
				{stdout: primaryFromOrchestrator(0)},
				{stdout: primaryFromOrchestrator(0)},
				{stdout: primaryFromOrchestrator(1)},
				{stdout: primaryFromOrchestrator(1)},
			},
			wantDeleted: []string{mysql.PodName(cr, 0)},
		},
		{
			name:          "async: running backup blocks the update",
			clusterType:   apiv1.ClusterTypeAsync,
			backupRunning: true,
			revisions:     []string{oldRev, oldRev, oldRev},
		},
		{
			name:        "group replication: a secondary is updated without switchover",
			clusterType: apiv1.ClusterTypeGR,
			revisions:   []string{oldRev, oldRev, oldRev},
			scripts: []fakeClientScript{
				{stdout: primaryFromGR(0)},
			},
			wantDeleted: []string{mysql.PodName(cr, 1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := cr.DeepCopy()
			cr.Spec.MySQL.ClusterType = tt.clusterType

			sts := newSts(int32(len(tt.revisions)))

			objs := []client.Object{sts, readyOrcPod, operatorSecret}
			if tt.backupRunning {
				objs = append(objs, runningBackup)
			}
			for i, revision := range tt.revisions {
				objs = append(objs, newPod(i, revision))
			}

			cli := &recreatingClient{
				WithWatch: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(),
				revision:  newRev,
			}
			fc := &fakeClient{disableCheck: true, scripts: tt.scripts}
			r := &PerconaServerMySQLReconciler{
				Client:        cli,
				Scheme:        s,
				ClientCmd:     fc,
				ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
				Recorder:      new(record.FakeRecorder),
			}

			require.NoError(t, r.smartUpdate(t.Context(), sts, cr))

			assert.Equal(t, tt.wantDeleted, cli.deleted)
			assert.Equal(t, len(tt.scripts), fc.execCount)
		})
	}
}
