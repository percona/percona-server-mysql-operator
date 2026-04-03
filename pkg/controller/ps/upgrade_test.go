package ps

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
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

		err := r.switchOverGR(context.Background(), cr, primary, target)
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

		err := r.switchOverGR(context.Background(), cr, primary, target)
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
					cmd:    []string{"curl", fmt.Sprintf("localhost:3000/api/master/%s", clusterHint)},
					stdout: clusterPrimaryResp,
				},
				{
					cmd:    []string{"curl", fmt.Sprintf("localhost:3000/api/graceful-master-takeover-auto/%s/%s/%d", clusterHint, target.GetName(), mysql.DefaultPort)},
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

		err := r.switchOverAsync(context.Background(), cr, primary, target)
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

		err := r.switchOverAsync(context.Background(), cr, primary, target)
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
					cmd:    []string{"curl", fmt.Sprintf("localhost:3000/api/master/%s", clusterHint)},
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

		err := r.switchOverAsync(context.Background(), cr, primary, target)
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

		err := r.switchOverAsync(context.Background(), cr, primary, target)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ensure node is primary")
	})
}
