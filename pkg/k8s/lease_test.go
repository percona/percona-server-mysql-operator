package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordv1 "k8s.io/api/coordination/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestAcquireLease(t *testing.T) {
	t.Run("creates lease for holder", func(t *testing.T) {
		scheme := runtime.NewScheme()
		require.NoError(t, clientgoscheme.AddToScheme(scheme))
		require.NoError(t, apiv1.AddToScheme(scheme))
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		restore := &apiv1.PerconaServerMySQLRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore1",
				Namespace: "ns",
				UID:       "restore1-uid",
			},
			Spec: apiv1.PerconaServerMySQLRestoreSpec{
				ClusterName: "cluster1",
			},
		}

		holder := naming.LeaseHolderName(restore.Name, string(restore.UID))
		err := AcquireLease(t.Context(), cl, naming.RestoreLeaseName(restore.Spec.ClusterName), holder, restore.Namespace, nil)
		require.NoError(t, err)

		got, err := GetLease(t.Context(), cl, naming.RestoreLeaseName(restore.Spec.ClusterName), restore.Namespace)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.NotNil(t, got.Spec.HolderIdentity)
		assert.Equal(t, holder, *got.Spec.HolderIdentity)
		assert.Nil(t, got.Spec.LeaseDurationSeconds)
		assert.NotNil(t, got.Spec.AcquireTime)
	})

	t.Run("returns already held when lease is owned by another holder", func(t *testing.T) {
		now := time.Now()
		scheme := runtime.NewScheme()
		require.NoError(t, clientgoscheme.AddToScheme(scheme))
		require.NoError(t, apiv1.AddToScheme(scheme))
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(&coordv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-lock-cluster1",
				Namespace: "ns",
			},
			Spec: coordv1.LeaseSpec{
				HolderIdentity:       ptr.To(naming.LeaseHolderName("restore1", "restore1-uid")),
				LeaseDurationSeconds: ptr.To(int32(30)),
				AcquireTime:          &metav1.MicroTime{Time: now},
				RenewTime:            &metav1.MicroTime{Time: now},
			},
		}).Build()

		restore := &apiv1.PerconaServerMySQLRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore2",
				Namespace: "ns",
				UID:       "restore2-uid",
			},
			Spec: apiv1.PerconaServerMySQLRestoreSpec{
				ClusterName: "cluster1",
			},
		}

		holder := naming.LeaseHolderName(restore.Name, string(restore.UID))
		err := AcquireLease(t.Context(), cl, naming.RestoreLeaseName(restore.Spec.ClusterName), holder, restore.Namespace, func(_ context.Context, lease *coordv1.Lease) (bool, error) {
			require.NotNil(t, lease)
			return false, nil
		})
		require.ErrorIs(t, err, ErrLeaseAlreadyHeld)

		got, err := GetLease(t.Context(), cl, naming.RestoreLeaseName(restore.Spec.ClusterName), restore.Namespace)
		require.NoError(t, err)
		require.NotNil(t, got.Spec.HolderIdentity)
		assert.Equal(t, naming.LeaseHolderName("restore1", "restore1-uid"), *got.Spec.HolderIdentity)
	})

	t.Run("replaces stale lease holder", func(t *testing.T) {
		now := time.Now()
		scheme := runtime.NewScheme()
		require.NoError(t, clientgoscheme.AddToScheme(scheme))
		require.NoError(t, apiv1.AddToScheme(scheme))
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(&coordv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-lock-cluster1",
				Namespace: "ns",
			},
			Spec: coordv1.LeaseSpec{
				HolderIdentity:       ptr.To(naming.LeaseHolderName("restore1", "restore1-uid")),
				LeaseDurationSeconds: ptr.To(int32(30)),
				AcquireTime:          &metav1.MicroTime{Time: now.Add(-time.Minute)},
				RenewTime:            &metav1.MicroTime{Time: now.Add(-time.Minute)},
			},
		}).Build()

		err := AcquireLease(t.Context(), cl, naming.RestoreLeaseName("cluster1"), naming.LeaseHolderName("restore2", "restore2-uid"), "ns", func(_ context.Context, lease *coordv1.Lease) (bool, error) {
			require.NotNil(t, lease)
			return true, nil
		})
		require.NoError(t, err)

		got, err := GetLease(t.Context(), cl, naming.RestoreLeaseName("cluster1"), "ns")
		require.NoError(t, err)
		require.NotNil(t, got.Spec.HolderIdentity)
		assert.Equal(t, naming.LeaseHolderName("restore2", "restore2-uid"), *got.Spec.HolderIdentity)
	})
}

func TestReleaseLease(t *testing.T) {
	now := time.Now()
	restore := &apiv1.PerconaServerMySQLRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore1",
			Namespace: "ns",
			UID:       "restore1-uid",
		},
		Spec: apiv1.PerconaServerMySQLRestoreSpec{
			ClusterName: "cluster1",
		},
		Status: apiv1.PerconaServerMySQLRestoreStatus{
			State: apiv1.RestoreSucceeded,
		},
	}

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))
	cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(&coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-lock-cluster1",
			Namespace: "ns",
		},
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       ptr.To(naming.LeaseHolderName(restore.Name, string(restore.UID))),
			LeaseDurationSeconds: ptr.To(int32(30)),
			AcquireTime:          &metav1.MicroTime{Time: now},
			RenewTime:            &metav1.MicroTime{Time: now},
		},
	}).Build()

	require.NoError(t, ReleaseLease(t.Context(), cl, naming.RestoreLeaseName(restore.Spec.ClusterName), naming.LeaseHolderName(restore.Name, string(restore.UID)), restore.Namespace))

	lease, err := GetLease(t.Context(), cl, naming.RestoreLeaseName(restore.Spec.ClusterName), restore.Namespace)
	require.Error(t, err)
	assert.True(t, k8serrors.IsNotFound(err))
	assert.Nil(t, lease)

	t.Run("returns already held when holder differs", func(t *testing.T) {
		now := time.Now()
		scheme := runtime.NewScheme()
		require.NoError(t, clientgoscheme.AddToScheme(scheme))
		require.NoError(t, apiv1.AddToScheme(scheme))
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(&coordv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore-lock-cluster1",
				Namespace: "ns",
			},
			Spec: coordv1.LeaseSpec{
				HolderIdentity:       ptr.To(naming.LeaseHolderName("restore1", "restore1-uid")),
				LeaseDurationSeconds: ptr.To(int32(30)),
				AcquireTime:          &metav1.MicroTime{Time: now},
				RenewTime:            &metav1.MicroTime{Time: now},
			},
		}).Build()

		err := ReleaseLease(t.Context(), cl, naming.RestoreLeaseName("cluster1"), "restore2", "ns")
		require.ErrorIs(t, err, ErrLeaseAlreadyHeld)
	})
}
