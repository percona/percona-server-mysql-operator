package ps

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

func clusterTypeCR(name, namespace string, clusterType apiv1.ClusterType) *apiv1.PerconaServerMySQL {
	return &apiv1.PerconaServerMySQL{
		Name:      name,
		Namespace: namespace,
		Spec: apiv1.PerconaServerMySQLSpec{
			MySQL: apiv1.MySQLSpec{
				ClusterType: clusterType,
				PodSpec:     apiv1.PodSpec{Size: 3},
			},
			// CheckNSetDefaults always populates these, and
			// reconcileClusterTypeChange dereferences them.
			Proxy: apiv1.ProxySpec{
				HAProxy: &apiv1.HAProxySpec{},
				Router:  &apiv1.MySQLRouterSpec{},
			},
		},
	}
}

// mysqlStsWithClusterType builds the MySQL StatefulSet the way the operator
// would have left it for the given cluster type.
func mysqlStsWithClusterType(cr *apiv1.PerconaServerMySQL, clusterType apiv1.ClusterType) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		Name:      mysql.Name(cr),
		Namespace: cr.Namespace,
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: mysql.AppName,
							Env: []corev1.EnvVar{
								{Name: naming.EnvMySQLClusterType, Value: string(clusterType)},
							},
						},
					},
				},
			},
		},
	}
}

func TestGetObservedClusterType(t *testing.T) {
	t.Run("falls back to the statefulset when the status has no type yet", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cr, sts).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		observed, err := r.getObservedClusterType(t.Context(), cr)
		require.NoError(t, err)
		assert.Equal(t, apiv1.ClusterTypeAsync, observed)
	})

	t.Run("prefers the status over the statefulset", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		cr.Status.ClusterType = apiv1.ClusterTypeAsync
		// The StatefulSet already carries the new type, which is exactly what
		// happens when reconcileDatabase re-applies it before the switch runs.
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeGR)

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cr, sts).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		observed, err := r.getObservedClusterType(t.Context(), cr)
		require.NoError(t, err)
		assert.Equal(t, apiv1.ClusterTypeAsync, observed)
	})
}

func TestReconcileClusterTypeChange(t *testing.T) {
	t.Run("records the observed type when the status has none", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeAsync)
		cr.Status.State = apiv1.StateReady
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		require.NoError(t, r.reconcileClusterTypeChange(t.Context(), cr))

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.Equal(t, apiv1.ClusterTypeAsync, stored.Status.ClusterType)
	})

	t.Run("defers the switch and records the running type when the cluster is not ready", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		cr.Status.State = apiv1.StateError
		cr.Spec.Orchestrator.Enabled = false
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		require.NoError(t, r.reconcileClusterTypeChange(t.Context(), cr))

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.Equal(t, apiv1.ClusterTypeAsync, stored.Status.ClusterType,
			"the type the cluster is running must be recorded before the switch is deferred")

		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(sts), &appsv1.StatefulSet{}),
			"the MySQL StatefulSet must not be torn down while the switch is deferred")
	})

	t.Run("records the running type when the cluster is paused", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		cr.Spec.Pause = true
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		require.NoError(t, r.reconcileClusterTypeChange(t.Context(), cr))

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.Equal(t, apiv1.ClusterTypeAsync, stored.Status.ClusterType)
	})

	t.Run("does not switch while the cluster is paused", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		cr.Spec.Pause = true
		cr.Status.ClusterType = apiv1.ClusterTypeAsync
		cr.Status.State = apiv1.StateReady
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		require.NoError(t, r.reconcileClusterTypeChange(t.Context(), cr))

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.Equal(t, apiv1.ClusterTypeAsync, stored.Status.ClusterType)

		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(sts), &appsv1.StatefulSet{}),
			"the MySQL StatefulSet must not be torn down while the cluster is paused")
	})

	t.Run("records the new type once the switch completes", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		cr.Status.ClusterType = apiv1.ClusterTypeAsync
		cr.Status.State = apiv1.StateReady
		cr.Spec.Orchestrator.Enabled = false
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)
		secret := &corev1.Secret{
			Name:      cr.InternalSecretName(),
			Namespace: cr.Namespace,
			Data:      map[string][]byte{string(apiv1.UserOperator): []byte("pass")},
		}

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts, secret).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		require.NoError(t, r.reconcileClusterTypeChange(t.Context(), cr))

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.Equal(t, apiv1.ClusterTypeGR, stored.Status.ClusterType)

		err := cli.Get(t.Context(), client.ObjectKeyFromObject(sts), &appsv1.StatefulSet{})
		assert.True(t, k8serrors.IsNotFound(err), "the MySQL StatefulSet must be recreated for the new type")
	})

	t.Run("removes orchestrator before tearing down async replication", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		cr.Status.ClusterType = apiv1.ClusterTypeAsync
		cr.Status.State = apiv1.StateReady
		cr.Spec.Orchestrator.Enabled = false
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)
		orcSts := &appsv1.StatefulSet{Name: orchestrator.Name(cr), Namespace: cr.Namespace}
		orcPod := &corev1.Pod{
			Name:      orchestrator.PodName(cr, 0),
			Namespace: cr.Namespace,
			Labels:    orchestrator.MatchLabels(cr),
		}

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts, orcSts, orcPod).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		require.NoError(t, r.reconcileClusterTypeChange(t.Context(), cr))

		err := cli.Get(t.Context(), client.ObjectKeyFromObject(orcSts), &appsv1.StatefulSet{})
		assert.True(t, k8serrors.IsNotFound(err), "the Orchestrator StatefulSet must be deleted first")

		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(sts), &appsv1.StatefulSet{}),
			"the teardown must wait for the Orchestrator pods to go away")

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.Equal(t, apiv1.ClusterTypeAsync, stored.Status.ClusterType,
			"the switch must not be recorded as done while it is still waiting")
		assert.True(t, meta.IsStatusConditionTrue(stored.Status.Conditions, apiv1.ConditionClusterTypeSwitchInProgress),
			"the in-progress marker keeps Orchestrator from being recreated while waiting")
	})

	t.Run("tears down async replication once orchestrator pods are gone", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		cr.Status.ClusterType = apiv1.ClusterTypeAsync
		cr.Status.State = apiv1.StateReady
		cr.Spec.Orchestrator.Enabled = false
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)
		secret := &corev1.Secret{
			Name:      cr.InternalSecretName(),
			Namespace: cr.Namespace,
			Data:      map[string][]byte{string(apiv1.UserOperator): []byte("pass")},
		}

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts, secret).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		require.NoError(t, r.reconcileClusterTypeChange(t.Context(), cr))

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.Equal(t, apiv1.ClusterTypeGR, stored.Status.ClusterType)
	})

	t.Run("keeps the switch pending when the teardown fails", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		cr.Status.ClusterType = apiv1.ClusterTypeAsync
		cr.Status.State = apiv1.StateReady
		cr.Spec.Orchestrator.Enabled = true
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		err := r.reconcileClusterTypeChange(t.Context(), cr)
		require.Error(t, err)
		require.ErrorContains(t, err,
			"cannot switch clusterType from async while orchestrator is enabled; set spec.orchestrator.enabled=false first")

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.Equal(t, apiv1.ClusterTypeAsync, stored.Status.ClusterType)

		assert.True(t,
			meta.IsStatusConditionTrue(stored.Status.Conditions, apiv1.ConditionClusterTypeSwitchInProgress),
			"the switch must be marked in progress before the teardown so the retry is not gated on readiness")
	})

	t.Run("retries an in-progress switch even when the cluster is not ready", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		cr.Status.ClusterType = apiv1.ClusterTypeAsync
		cr.Status.State = apiv1.StateError
		cr.Spec.Orchestrator.Enabled = false
		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:   apiv1.ConditionClusterTypeSwitchInProgress,
			Status: metav1.ConditionTrue,
			Reason: "TeardownStarted",
		})
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)
		secret := &corev1.Secret{
			Name:      cr.InternalSecretName(),
			Namespace: cr.Namespace,
			Data:      map[string][]byte{string(apiv1.UserOperator): []byte("pass")},
		}

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts, secret).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		require.NoError(t, r.reconcileClusterTypeChange(t.Context(), cr))

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.Equal(t, apiv1.ClusterTypeGR, stored.Status.ClusterType)
		assert.True(t,
			meta.IsStatusConditionTrue(stored.Status.Conditions, apiv1.ConditionClusterTypeSwitchInProgress),
			"the marker must survive the teardown until the cluster is ready under the new type")
	})

	t.Run("reverts a switch to GR that never bootstrapped", func(t *testing.T) {
		const operatorPass = "pass"

		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeAsync)
		cr.Status.ClusterType = apiv1.ClusterTypeGR
		cr.Status.State = apiv1.StateError
		cr.Spec.Orchestrator.Enabled = false
		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:   apiv1.ConditionClusterTypeSwitchInProgress,
			Status: metav1.ConditionTrue,
			Reason: "TeardownStarted",
		})
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeGR)
		secret := &corev1.Secret{
			Name:      cr.InternalSecretName(),
			Namespace: cr.Namespace,
			Data:      map[string][]byte{string(apiv1.UserOperator): []byte(operatorPass)},
		}

		objs := []client.Object{cr, sts, secret}
		pods := make([]*corev1.Pod, 3)
		for i := range pods {
			// GR never came up, so no pod ever passed its readiness probe.
			pods[i] = mysqlPod(cr, i, false)
			objs = append(objs, pods[i])
		}

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(objs...).WithStatusSubresource(cr).Build()
		fc := &fakeClient{scripts: []fakeClientScript{
			{cmd: mysqlCmd(operatorPass, mysql.PodFQDN(cr, pods[0]), persistedGRVarsQuery)},
			{cmd: mysqlCmd(operatorPass, mysql.PodFQDN(cr, pods[1]), persistedGRVarsQuery)},
			{cmd: mysqlCmd(operatorPass, mysql.PodFQDN(cr, pods[2]), persistedGRVarsQuery)},
		}}
		r := &PerconaServerMySQLReconciler{Client: cli, ClientCmd: fc}

		require.NoError(t, r.reconcileClusterTypeChange(t.Context(), cr))

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.Equal(t, apiv1.ClusterTypeAsync, stored.Status.ClusterType,
			"a switch to GR that never bootstrapped must be revertible without the cluster ever being ready")

		err := cli.Get(t.Context(), client.ObjectKeyFromObject(sts), &appsv1.StatefulSet{})
		assert.True(t, k8serrors.IsNotFound(err), "the MySQL StatefulSet must be recreated for the reverted type")
	})

	t.Run("clears the in-progress marker once the cluster is ready under the desired type", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeAsync)
		cr.Status.ClusterType = apiv1.ClusterTypeAsync
		cr.Status.State = apiv1.StateReady
		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:   apiv1.ConditionClusterTypeSwitchInProgress,
			Status: metav1.ConditionTrue,
			Reason: "TeardownStarted",
		})
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		require.NoError(t, r.reconcileClusterTypeChange(t.Context(), cr))

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.False(t,
			meta.IsStatusConditionTrue(stored.Status.Conditions, apiv1.ConditionClusterTypeSwitchInProgress))
	})

	t.Run("keeps the in-progress marker while the switched cluster is not ready", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		cr.Status.ClusterType = apiv1.ClusterTypeGR
		cr.Status.State = apiv1.StateInitializing
		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:   apiv1.ConditionClusterTypeSwitchInProgress,
			Status: metav1.ConditionTrue,
			Reason: "TeardownStarted",
		})
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeGR)

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		require.NoError(t, r.reconcileClusterTypeChange(t.Context(), cr))

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.True(t,
			meta.IsStatusConditionTrue(stored.Status.Conditions, apiv1.ConditionClusterTypeSwitchInProgress),
			"a switch that never converged must stay marked so the revert is not gated on readiness")
	})
}

// mysqlPod builds a MySQL pod the way the StatefulSet would have created it.
func mysqlPod(cr *apiv1.PerconaServerMySQL, idx int, ready bool) *corev1.Pod {
	pod := &corev1.Pod{
		Name:      mysql.PodName(cr, idx),
		Namespace: cr.Namespace,
		Labels:    mysql.MatchLabels(cr),
		Status:    corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if ready {
		pod.Status.Conditions = []corev1.PodCondition{
			{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
		}
	}
	return pod
}

const (
	persistedGRVarsQuery = "SELECT VARIABLE_NAME as name FROM performance_schema.persisted_variables WHERE VARIABLE_NAME LIKE 'group_replication_%'"
	grPrimaryQuery       = "SELECT MEMBER_HOST as host FROM replication_group_members WHERE MEMBER_ROLE='PRIMARY' AND MEMBER_STATE='ONLINE'"
)

func mysqlCmd(pass, host, query string) []string {
	return []string{
		"mysql", "--database", "performance_schema",
		"-p" + pass, "-u", string(apiv1.UserOperator), "-h", host, "-e", query,
	}
}

func mysqlshCmd(uri, js string) []string {
	return []string{"mysqlsh", "--js", "--no-wizard", "--uri", uri, "-e", js}
}
