package psclusterset

import (
	"testing"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
	psmock "github.com/percona/percona-server-mysql-operator/pkg/controller/psclusterset/mock"
	"github.com/stretchr/testify/mock"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var baseClusterSet = &apiv1.PerconaServerMySQLClusterSet{
	ObjectMeta: metav1.ObjectMeta{
		Name: "test-cluster-set",
	},
	Spec: apiv1.PerconaServerMySQLClusterSetSpec{
		PrimaryCluster: "dc1",
		Clusters: []apiv1.ClusterSetCluster{
			{
				Name: "dc1",
				Endpoints: []apiv1.ClusterSetClusterEndpoint{
					{
						Host: "dc1-mysql-primary.test-cluster-set.svc.cluster.local",
					},
				},
			},
			{
				Name: "dc2",
				Endpoints: []apiv1.ClusterSetClusterEndpoint{
					{
						Host: "dc2-mysql-0.test-cluster-set.svc.cluster.local",
					},
				},
			},
		},
	},
}

func TestReconciler_reconcileStatus(t *testing.T) {
	testCases := []struct {
		desc           string
		clusterSet     func() *apiv1.PerconaServerMySQLClusterSet
		observedStatus clusterset.Status
		asserts        func(t *testing.T, cl client.Client)
		events         func(recorder *psmock.EventRecorder)
	}{
		{
			desc: "healthy cluster set",
			clusterSet: func() *apiv1.PerconaServerMySQLClusterSet {
				return baseClusterSet.DeepCopy()
			},
			observedStatus: clusterset.Status{
				Clusters: apiv1.ClusterSetStatus{
					"dc1": {
						ClusterRole: clusterset.ClusterRolePrimary,
					},
					"dc2": {
						ClusterRole: clusterset.ClusterRoleReplica,
					},
				},
				DomainName:            "test-cluster-set.svc.cluster.local",
				GlobalPrimaryInstance: "dc1-mysql-primary.test-cluster-set.svc.cluster.local",
				PrimaryCluster:        "dc1",
				Status:                clusterset.StatusHealthy,
				StatusText:            "Cluster set is healthy",
			},
			events: func(recorder *psmock.EventRecorder) {
				recorder.On("Eventf", mock.IsType(&apiv1.PerconaServerMySQLClusterSet{}), nil, corev1.EventTypeNormal,
					apiv1.EventTypeClusterSetMemberAdded, apiv1.EventTypeClusterSetMemberAdded,
					"Cluster %s added to ClusterSet with role %s", "dc1", clusterset.ClusterRolePrimary).Return()
				recorder.On("Eventf", mock.IsType(&apiv1.PerconaServerMySQLClusterSet{}), nil, corev1.EventTypeNormal,
					apiv1.EventTypeClusterSetMemberAdded, apiv1.EventTypeClusterSetMemberAdded,
					"Cluster %s added to ClusterSet with role %s", "dc2", clusterset.ClusterRoleReplica).Return()
			},
			asserts: func(t *testing.T, cl client.Client) {
				observed := &apiv1.PerconaServerMySQLClusterSet{}
				err := cl.Get(t.Context(), client.ObjectKeyFromObject(baseClusterSet), observed)
				require.NoError(t, err)

				cond := meta.FindStatusCondition(observed.Status.Conditions, apiv1.ConditionClusterSetReady)
				assert.NotNil(t, cond)
				assert.Equal(t, metav1.ConditionTrue, cond.Status)
				assert.Equal(t, "ClusterSetHealthy", cond.Reason)
				assert.Equal(t, "Cluster set is healthy", cond.Message)

			},
		},
		{
			desc: "unhealthy cluster set",
			clusterSet: func() *apiv1.PerconaServerMySQLClusterSet {
				clusterSet := baseClusterSet.DeepCopy()
				clusterSet.Status.PrimaryCluster = "dc1"
				clusterSet.Status.Clusters = apiv1.ClusterSetStatus{
					"dc1": {
						ClusterRole: clusterset.ClusterRolePrimary,
					},
					"dc2": {
						ClusterRole: clusterset.ClusterRoleReplica,
					},
				}
				clusterSet.Status.Conditions = []metav1.Condition{
					{
						Type:   apiv1.ConditionClusterSetReady,
						Status: metav1.ConditionTrue,
					},
				}
				return clusterSet
			},
			observedStatus: clusterset.Status{
				Clusters: apiv1.ClusterSetStatus{
					"dc1": {
						ClusterRole: clusterset.ClusterRolePrimary,
					},
					"dc2": {
						ClusterRole: clusterset.ClusterRoleReplica,
					},
				},
				GlobalPrimaryInstance: "dc1-mysql-primary.test-cluster-set.svc.cluster.local",
				PrimaryCluster:        "dc1",
				Status:                "UNHEALTHY",
				StatusText:            "Cluster set is not healthy",
			},
			events: func(recorder *psmock.EventRecorder) {
				recorder.On("Eventf", mock.IsType(&apiv1.PerconaServerMySQLClusterSet{}), nil, corev1.EventTypeWarning,
					apiv1.EventTypeClusterSetUnhealthy, apiv1.EventTypeClusterSetUnhealthy,
					"ClusterSet health degraded: %s", "Cluster set is not healthy").Return()
			},
			asserts: func(t *testing.T, cl client.Client) {
				observed := &apiv1.PerconaServerMySQLClusterSet{}
				err := cl.Get(t.Context(), client.ObjectKeyFromObject(baseClusterSet), observed)
				require.NoError(t, err)

				cond := meta.FindStatusCondition(observed.Status.Conditions, apiv1.ConditionClusterSetReady)
				assert.NotNil(t, cond)
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Equal(t, "ClusterSetNotHealthy", cond.Reason)
				assert.Equal(t, "Cluster set is not healthy", cond.Message)
				assert.Equal(t, "dc1", observed.Status.PrimaryCluster)
				assert.Equal(t, "dc1-mysql-primary.test-cluster-set.svc.cluster.local", observed.Status.PrimaryClusterEndpoint)
			},
		},
		{
			desc: "removed member",
			clusterSet: func() *apiv1.PerconaServerMySQLClusterSet {
				clusterSet := baseClusterSet.DeepCopy()
				clusterSet.Status.PrimaryCluster = "dc1"
				clusterSet.Status.Clusters = apiv1.ClusterSetStatus{
					"dc1": {
						ClusterRole: clusterset.ClusterRolePrimary,
					},
					"dc2": {
						ClusterRole: clusterset.ClusterRoleReplica,
					},
					"dc3": {
						ClusterRole: clusterset.ClusterRoleReplica,
					},
				}
				return clusterSet
			},
			observedStatus: clusterset.Status{
				Clusters: apiv1.ClusterSetStatus{
					"dc1": {
						ClusterRole: clusterset.ClusterRolePrimary,
					},
					"dc2": {
						ClusterRole: clusterset.ClusterRoleReplica,
					},
				},
				GlobalPrimaryInstance: "dc1-mysql-primary.test-cluster-set.svc.cluster.local",
				PrimaryCluster:        "dc1",
				Status:                clusterset.StatusHealthy,
				StatusText:            "Cluster set is healthy",
			},
			events: func(recorder *psmock.EventRecorder) {
				recorder.On("Eventf", mock.IsType(&apiv1.PerconaServerMySQLClusterSet{}), nil, corev1.EventTypeNormal,
					apiv1.EventTypeClusterSetMemberRemoved, apiv1.EventTypeClusterSetMemberRemoved,
					"Cluster %s removed from ClusterSet", "dc3").Return()
			},
			asserts: func(t *testing.T, cl client.Client) {
				observed := &apiv1.PerconaServerMySQLClusterSet{}
				err := cl.Get(t.Context(), client.ObjectKeyFromObject(baseClusterSet), observed)
				require.NoError(t, err)

				assert.NotContains(t, observed.Status.Clusters, "dc3")

				cond := meta.FindStatusCondition(observed.Status.Conditions, apiv1.ConditionClusterSetReady)
				assert.NotNil(t, cond)
				assert.Equal(t, metav1.ConditionTrue, cond.Status)
				assert.Equal(t, "ClusterSetHealthy", cond.Reason)
			},
		},
		{
			desc: "primary cluster switched",
			clusterSet: func() *apiv1.PerconaServerMySQLClusterSet {
				clusterSet := baseClusterSet.DeepCopy()
				clusterSet.Status.PrimaryCluster = "dc1"
				clusterSet.Status.Clusters = apiv1.ClusterSetStatus{
					"dc1": {
						ClusterRole: clusterset.ClusterRoleReplica,
					},
					"dc2": {
						ClusterRole: clusterset.ClusterRolePrimary,
					},
				}
				return clusterSet
			},
			observedStatus: clusterset.Status{
				Clusters: apiv1.ClusterSetStatus{
					"dc1": {
						ClusterRole: clusterset.ClusterRoleReplica,
					},
					"dc2": {
						ClusterRole: clusterset.ClusterRolePrimary,
					},
				},
				GlobalPrimaryInstance: "dc2-mysql-primary.test-cluster-set.svc.cluster.local",
				PrimaryCluster:        "dc2",
				Status:                clusterset.StatusHealthy,
				StatusText:            "Cluster set is healthy",
			},
			events: func(recorder *psmock.EventRecorder) {
				recorder.On("Eventf", mock.IsType(&apiv1.PerconaServerMySQLClusterSet{}), nil, corev1.EventTypeNormal,
					apiv1.EventTypeClusterSetPrimarySwitched, apiv1.EventTypeClusterSetPrimarySwitched,
					"Primary cluster switched from %s to %s", "dc1", "dc2").Return()
			},
			asserts: func(t *testing.T, cl client.Client) {
				observed := &apiv1.PerconaServerMySQLClusterSet{}
				err := cl.Get(t.Context(), client.ObjectKeyFromObject(baseClusterSet), observed)
				require.NoError(t, err)

				assert.Equal(t, "dc2", observed.Status.PrimaryCluster)
				assert.Equal(t, "dc2-mysql-primary.test-cluster-set.svc.cluster.local", observed.Status.PrimaryClusterEndpoint)

				cond := meta.FindStatusCondition(observed.Status.Conditions, apiv1.ConditionClusterSetReady)
				assert.NotNil(t, cond)
				assert.Equal(t, metav1.ConditionTrue, cond.Status)
				assert.Equal(t, "ClusterSetHealthy", cond.Reason)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			recorder := &psmock.EventRecorder{}
			manager := &psmock.ClusterSetManager{}

			manager.On("Status", t.Context()).Return(tc.observedStatus, nil)
			tc.events(recorder)

			scheme := runtime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				t.Fatal(err, "failed to add client-go scheme")
			}
			if err := apiv1.AddToScheme(scheme); err != nil {
				t.Fatal(err, "failed to add apis scheme")
			}

			clusterSet := tc.clusterSet()
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(clusterSet).
				WithStatusSubresource(clusterSet).
				Build()

			r := &PerconaServerMySQLClusterSetReconciler{
				Client:   cl,
				Scheme:   scheme,
				Recorder: recorder,
			}
			err := r.reconcileStatus(t.Context(), clusterSet, manager)
			require.NoError(t, err)
			tc.asserts(t, cl)
		})
	}
}

func TestReconciler_reconcileSwitchover(t *testing.T) {
	testCases := []struct {
		desc       string
		clusterSet func() *apiv1.PerconaServerMySQLClusterSet
		asserts    func(t *testing.T, cl client.Client)
	}{
		{
			desc: "no primary reported",
			clusterSet: func() *apiv1.PerconaServerMySQLClusterSet {
				return baseClusterSet.DeepCopy()
			},
			asserts: func(t *testing.T, cl client.Client) {
				jobs := &batchv1.JobList{}
				matchLabels := map[string]string{
					"command": clusterset.CmdSetPrimary,
				}

				err := cl.List(t.Context(), jobs, client.MatchingLabels(matchLabels))
				require.NoError(t, err)
				assert.Empty(t, jobs.Items)
			},
		},
		{
			desc: "no switchover",
			clusterSet: func() *apiv1.PerconaServerMySQLClusterSet {
				pcs := baseClusterSet.DeepCopy()
				pcs.Spec.PrimaryCluster = "dc1"
				pcs.Status.PrimaryCluster = "dc1"
				return pcs
			},
			asserts: func(t *testing.T, cl client.Client) {
				jobs := &batchv1.JobList{}
				matchLabels := map[string]string{
					"command": clusterset.CmdSetPrimary,
				}

				err := cl.List(t.Context(), jobs, client.MatchingLabels(matchLabels))
				require.NoError(t, err)
				assert.Empty(t, jobs.Items)
			},
		}, {
			desc: "switchover requested",
			clusterSet: func() *apiv1.PerconaServerMySQLClusterSet {
				pcs := baseClusterSet.DeepCopy()
				pcs.Spec.PrimaryCluster = "dc2"
				pcs.Status.PrimaryCluster = "dc1"
				return pcs
			},
			asserts: func(t *testing.T, cl client.Client) {
				jobs := &batchv1.JobList{}
				matchLabels := map[string]string{
					"command": clusterset.CmdSetPrimary,
				}

				err := cl.List(t.Context(), jobs, client.MatchingLabels(matchLabels))
				require.NoError(t, err)
				assert.Len(t, jobs.Items, 1)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			recorder := &psmock.EventRecorder{}

			scheme := runtime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				t.Fatal(err, "failed to add client-go scheme")
			}
			if err := apiv1.AddToScheme(scheme); err != nil {
				t.Fatal(err, "failed to add apis scheme")
			}

			clusterSet := tc.clusterSet()
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(clusterSet).
				WithStatusSubresource(clusterSet).
				Build()

			r := &PerconaServerMySQLClusterSetReconciler{
				Client:   cl,
				Scheme:   scheme,
				Recorder: recorder,
			}
			err := r.reconcileSwitchover(t.Context(), clusterSet)
			require.NoError(t, err)
			tc.asserts(t, cl)
		})
	}
}

func TestReconciler_reconcileReplicas(t *testing.T) {
	testCases := []struct {
		desc       string
		clusterSet func() *apiv1.PerconaServerMySQLClusterSet
		asserts    func(t *testing.T, cl client.Client)
	}{
		{
			desc: "nothing to do",
			clusterSet: func() *apiv1.PerconaServerMySQLClusterSet {
				pcs := baseClusterSet.DeepCopy()
				pcs.Spec.PrimaryCluster = "dc1"
				pcs.Status.Clusters = apiv1.ClusterSetStatus{
					"dc1": {
						ClusterRole: clusterset.ClusterRolePrimary,
					},
					"dc2": {
						ClusterRole: clusterset.ClusterRoleReplica,
					},
				}
				return pcs
			},

			asserts: func(t *testing.T, cl client.Client) {
				jobs := &batchv1.JobList{}
				matchLabels := map[string]string{
					"command": clusterset.CmdAddReplica,
				}

				err := cl.List(t.Context(), jobs, client.MatchingLabels(matchLabels))
				require.NoError(t, err)
				assert.Empty(t, jobs.Items)

				jobs = &batchv1.JobList{}
				matchLabels = map[string]string{
					"command": clusterset.CmdRemoveReplica,
				}

				err = cl.List(t.Context(), jobs, client.MatchingLabels(matchLabels))
				require.NoError(t, err)
				assert.Empty(t, jobs.Items)
			},
		},
		{
			desc: "adds replicas to the clusterset",
			clusterSet: func() *apiv1.PerconaServerMySQLClusterSet {
				pcs := baseClusterSet.DeepCopy()
				pcs.Spec.PrimaryCluster = "dc1"

				pcs.Spec.Clusters = append(pcs.Spec.Clusters, apiv1.ClusterSetCluster{
					Name: "dc3",
					Endpoints: []apiv1.ClusterSetClusterEndpoint{
						{
							Host: "dc3-mysql-0.test-cluster-set.svc.cluster.local",
							Port: new(int32(3306)),
						},
					},
				})
				pcs.Status.Clusters = apiv1.ClusterSetStatus{
					"dc1": {
						ClusterRole: clusterset.ClusterRolePrimary,
					},
					"dc2": {
						ClusterRole: clusterset.ClusterRoleReplica,
					},
				}
				return pcs
			},

			asserts: func(t *testing.T, cl client.Client) {
				jobs := &batchv1.JobList{}
				matchLabels := map[string]string{
					"command": clusterset.CmdAddReplica,
				}

				err := cl.List(t.Context(), jobs, client.MatchingLabels(matchLabels))
				require.NoError(t, err)
				assert.Len(t, jobs.Items, 1)
				assert.Equal(t, "dc3", jobs.Items[0].GetLabels()["cluster-name"])
			},
		},
		{
			desc: "removes replicas from the clusterset",
			clusterSet: func() *apiv1.PerconaServerMySQLClusterSet {
				pcs := baseClusterSet.DeepCopy()
				pcs.Spec.PrimaryCluster = "dc1"

				pcs.Spec.Clusters = []apiv1.ClusterSetCluster{
					{
						Name: "dc1",
						Endpoints: []apiv1.ClusterSetClusterEndpoint{
							{
								Host: "dc1-mysql-primary.test-cluster-set.svc.cluster.local",
							},
						},
					},
				}
				pcs.Status.Clusters = apiv1.ClusterSetStatus{
					"dc1": {
						ClusterRole: clusterset.ClusterRolePrimary,
					},
					"dc2": {
						ClusterRole: clusterset.ClusterRoleReplica,
					},
				}
				return pcs
			},

			asserts: func(t *testing.T, cl client.Client) {
				jobs := &batchv1.JobList{}
				matchLabels := map[string]string{
					"command": clusterset.CmdRemoveReplica,
				}

				err := cl.List(t.Context(), jobs, client.MatchingLabels(matchLabels))
				require.NoError(t, err)
				assert.Len(t, jobs.Items, 1)
				assert.Equal(t, "dc2", jobs.Items[0].GetLabels()["cluster-name"])
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			recorder := &psmock.EventRecorder{}

			scheme := runtime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				t.Fatal(err, "failed to add client-go scheme")
			}
			if err := apiv1.AddToScheme(scheme); err != nil {
				t.Fatal(err, "failed to add apis scheme")
			}

			clusterSet := tc.clusterSet()
			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(clusterSet).
				WithStatusSubresource(clusterSet).
				Build()

			r := &PerconaServerMySQLClusterSetReconciler{
				Client:   cl,
				Scheme:   scheme,
				Recorder: recorder,
			}

			clusterSet.SetDefaults()
			err := r.reconcileReplicas(t.Context(), clusterSet)
			require.NoError(t, err)
			tc.asserts(t, cl)
		})
	}
}
