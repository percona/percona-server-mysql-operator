package k8s

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestAcquireRestoreLease(t *testing.T) {
	t.Run("creates lease for restore", func(t *testing.T) {
		scheme := runtime.NewScheme()
		require.NoError(t, clientgoscheme.AddToScheme(scheme))
		require.NoError(t, apiv1.AddToScheme(scheme))
		cl := fake.NewClientBuilder().WithScheme(scheme).Build()
		restore := &apiv1.PerconaServerMySQLRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore1",
				Namespace: "ns",
			},
			Spec: apiv1.PerconaServerMySQLRestoreSpec{
				ClusterName: "cluster1",
			},
		}

		lease, err := AcquireRestoreLease(t.Context(), cl, restore)
		require.NoError(t, err)
		require.NotNil(t, lease)
		require.NotNil(t, lease.Spec.HolderIdentity)
		assert.Equal(t, restore.Name, *lease.Spec.HolderIdentity)

		got, err := GetRestoreLease(t.Context(), cl, restore.Namespace, restore.Spec.ClusterName)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.NotNil(t, got.Spec.HolderIdentity)
		assert.Equal(t, restore.Name, *got.Spec.HolderIdentity)
	})

	t.Run("keeps active lease owned by another restore", func(t *testing.T) {
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
				HolderIdentity:       ptr.To("restore1"),
				LeaseDurationSeconds: ptr.To(int32(30)),
				AcquireTime:          &metav1.MicroTime{Time: now},
				RenewTime:            &metav1.MicroTime{Time: now},
			},
		}).Build()

		restore := &apiv1.PerconaServerMySQLRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "restore2",
				Namespace: "ns",
			},
			Spec: apiv1.PerconaServerMySQLRestoreSpec{
				ClusterName: "cluster1",
			},
		}

		lease, err := AcquireRestoreLease(t.Context(), cl, restore)
		require.NoError(t, err)
		require.NotNil(t, lease)
		require.NotNil(t, lease.Spec.HolderIdentity)
		assert.Equal(t, "restore1", *lease.Spec.HolderIdentity)
	})
}

func TestReleaseRestoreLease(t *testing.T) {
	now := time.Now()
	restore := &apiv1.PerconaServerMySQLRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore1",
			Namespace: "ns",
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
			HolderIdentity:       ptr.To("restore1"),
			LeaseDurationSeconds: ptr.To(int32(30)),
			AcquireTime:          &metav1.MicroTime{Time: now},
			RenewTime:            &metav1.MicroTime{Time: now},
		},
	}).Build()

	require.NoError(t, ReleaseRestoreLease(t.Context(), cl, restore))

	lease, err := GetRestoreLease(t.Context(), cl, restore.Namespace, restore.Spec.ClusterName)
	require.NoError(t, err)
	assert.Nil(t, lease)
}

func TestIsLeaseActive(t *testing.T) {
	now := time.Now()

	active := &coordv1.Lease{
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       ptr.To("restore1"),
			LeaseDurationSeconds: ptr.To(int32(30)),
			RenewTime:            &metav1.MicroTime{Time: now},
		},
	}
	assert.True(t, IsLeaseActive(active))

	expired := &coordv1.Lease{
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       ptr.To("restore1"),
			LeaseDurationSeconds: ptr.To(int32(30)),
			RenewTime:            &metav1.MicroTime{Time: now.Add(-time.Minute)},
		},
	}
	assert.False(t, IsLeaseActive(expired))
}
