package ps

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
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

	t.Run("a switch deferred by a not-ready cluster survives the statefulset being re-applied", func(t *testing.T) {
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

	t.Run("keeps the switch pending when the teardown fails", func(t *testing.T) {
		cr := clusterTypeCR("cluster1", "ns", apiv1.ClusterTypeGR)
		cr.Status.ClusterType = apiv1.ClusterTypeAsync
		cr.Status.State = apiv1.StateReady
		cr.Spec.Orchestrator.Enabled = true
		sts := mysqlStsWithClusterType(cr, apiv1.ClusterTypeAsync)

		cli := fake.NewClientBuilder().WithScheme(newScheme(t)).
			WithObjects(cr, sts).WithStatusSubresource(cr).Build()
		r := &PerconaServerMySQLReconciler{Client: cli}

		require.Error(t, r.reconcileClusterTypeChange(t.Context(), cr))

		stored := new(apiv1.PerconaServerMySQL)
		require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(cr), stored))
		assert.Equal(t, apiv1.ClusterTypeAsync, stored.Status.ClusterType)
	})
}
