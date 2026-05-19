package ps

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

func TestStartAsyncReplication_PropagatesGetReadyOrcPodError(t *testing.T) {
	ctx := context.Background()
	ns := "test-ns"
	crName := "test-cluster"

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crName,
			Namespace: ns,
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			MySQL: apiv1.MySQLSpec{
				ClusterType: apiv1.ClusterTypeAsync,
			},
		},
	}

	// Fake client with NO orchestrator pods — getReadyOrcPod will return ErrNoReadyPods
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cr).
		Build()

	r := &PerconaServerMySQLReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	primary := &orchestrator.Instance{
		Key: orchestrator.InstanceKey{
			Hostname: "test-cluster-mysql-0.test-cluster-mysql." + ns + ".svc.cluster.local",
			Port:     mysql.DefaultPort,
		},
		Replicas: []orchestrator.InstanceKey{
			{
				Hostname: "test-cluster-mysql-1.test-cluster-mysql." + ns + ".svc.cluster.local",
				Port:     mysql.DefaultPort,
			},
		},
	}

	err := r.startAsyncReplication(ctx, cr, "replica-password", primary)

	// With the bug (return nil), this would be nil — silently succeeding.
	// With the fix (return err), getReadyOrcPod error is properly propagated.
	require.Error(t, err, "startAsyncReplication should return an error when getReadyOrcPod fails")
	require.ErrorIs(t, err, mysql.ErrNoReadyPods, "error should wrap ErrNoReadyPods from getReadyOrcPod")
}

func TestStopAsyncReplication_PropagatesGetReadyOrcPodError(t *testing.T) {
	ctx := context.Background()
	ns := "test-ns"
	crName := "test-cluster"

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crName,
			Namespace: ns,
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			MySQL: apiv1.MySQLSpec{
				ClusterType: apiv1.ClusterTypeAsync,
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cr).
		Build()

	r := &PerconaServerMySQLReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	primary := &orchestrator.Instance{
		Key: orchestrator.InstanceKey{
			Hostname: "test-cluster-mysql-0.test-cluster-mysql." + ns + ".svc.cluster.local",
			Port:     mysql.DefaultPort,
		},
		Replicas: []orchestrator.InstanceKey{
			{
				Hostname: "test-cluster-mysql-1.test-cluster-mysql." + ns + ".svc.cluster.local",
				Port:     mysql.DefaultPort,
			},
		},
	}

	err := r.stopAsyncReplication(ctx, cr, primary)

	// stopAsyncReplication correctly propagates the error — baseline comparison
	require.Error(t, err, "stopAsyncReplication should return an error when getReadyOrcPod fails")
	require.ErrorIs(t, err, mysql.ErrNoReadyPods)
}
