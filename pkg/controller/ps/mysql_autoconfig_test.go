package ps

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

func TestReconcileMySQLAutoConfig(t *testing.T) {
	const (
		crName = "cluster1"
		ns     = "autoconfig-ns"
		// A key only the calculator produces, so its presence tells the
		// calculated configuration apart from the autotune fallback.
		calculatedKey = "innodb_redo_log_capacity="
	)

	newCR := func(enabled bool, mysqlVersion string) *apiv1.PerconaServerMySQL {
		cr := &apiv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns},
		}
		cr.Spec.CRVersion = version.Version()
		cr.Spec.MySQL.ClusterType = apiv1.ClusterTypeGR
		cr.Spec.MySQL.Size = 3
		cr.Spec.Orchestrator.Enabled = true
		cr.Spec.MySQL.Image = "percona/percona-server:8.4.6-6.1"
		cr.Spec.MySQL.AutoConfig.Enabled = &enabled
		cr.Spec.MySQL.AutoConfig.Version = mysqlVersion
		cr.Spec.MySQL.Resources = corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
		}
		return cr
	}

	autoConfig := func(t *testing.T, r *PerconaServerMySQLReconciler, cr *apiv1.PerconaServerMySQL) string {
		t.Helper()
		cm := new(corev1.ConfigMap)
		nn := types.NamespacedName{Name: mysql.AutoConfigMapName(cr), Namespace: cr.Namespace}
		require.NoError(t, r.Client.Get(context.Background(), nn, cm))
		return cm.Data[mysql.CustomConfigKey]
	}

	newReconciler := func(t *testing.T, cr *apiv1.PerconaServerMySQL) *PerconaServerMySQLReconciler {
		t.Helper()
		scheme := newScheme(t)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).Build()
		return &PerconaServerMySQLReconciler{Client: cl, Scheme: scheme, Recorder: record.NewFakeRecorder(100)}
	}

	tests := []struct {
		desc    string
		enabled bool
		version string

		wantCalculated bool
	}{
		{
			desc:           "disabled falls back to autotune",
			enabled:        false,
			version:        "8.4",
			wantCalculated: false,
		},
		{
			desc:           "enabled with a supported version",
			enabled:        true,
			version:        "8.4",
			wantCalculated: true,
		},
		{
			desc:           "an unsupported version falls back to autotune",
			enabled:        true,
			version:        "5.7",
			wantCalculated: false,
		},
		{
			// The CRD rejects this, so it only guards an outdated CRD.
			desc:           "no version falls back to autotune",
			enabled:        true,
			wantCalculated: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			cr := newCR(tt.enabled, tt.version)
			r := newReconciler(t, cr)

			require.NoError(t, r.reconcileMySQLAutoConfig(context.Background(), cr))

			config := autoConfig(t, r, cr)
			assert.Contains(t, config, "innodb_buffer_pool_size=", "both paths size the buffer pool")
			assert.Equal(t, tt.wantCalculated, strings.Contains(config, calculatedKey),
				"calculated configuration in:\n%s", config)
		})
	}

	// The redo log the calculator sizes from memory is preallocated on the data
	// volume at startup, and a node joining by clone needs free space for the
	// donor's estimate on top of its own. The reconcile trims it to fit rather
	// than leaving a cluster that cannot bootstrap.
	t.Run("a data volume smaller than the calculated redo log trims it", func(t *testing.T) {
		ctx := context.Background()
		cr := newCR(true, "8.4")
		withDataVolume(cr, "2Gi")
		r := newReconciler(t, cr)

		require.NoError(t, r.reconcileMySQLAutoConfig(ctx, cr))

		assert.Contains(t, autoConfig(t, r, cr), "innodb_redo_log_capacity=536870912")
	})

	// Trimming stops at the smallest redo log MySQL accepts, so a volume below
	// that leaves nothing to write: the reconcile fails until the user resizes.
	t.Run("a data volume too small for the minimum redo log fails the reconcile", func(t *testing.T) {
		ctx := context.Background()
		cr := newCR(true, "8.4")
		withDataVolume(cr, "16Mi")
		r := newReconciler(t, cr)

		err := r.reconcileMySQLAutoConfig(ctx, cr)
		assert.ErrorIs(t, err, mysql.ErrInsufficientStorage)
		// The message has to name the knob, since the cluster stays down until
		// the user acts on it.
		assert.Contains(t, err.Error(), "mysql.volumeSpec.persistentVolumeClaim")

		// No autotune fallback was written in its place.
		cm := new(corev1.ConfigMap)
		nn := types.NamespacedName{Name: mysql.AutoConfigMapName(cr), Namespace: cr.Namespace}
		assert.True(t, k8serrors.IsNotFound(r.Client.Get(ctx, nn, cm)),
			"no ConfigMap should be written when the calculated configuration does not fit")
	})

	// The ConfigMap is rebuilt from the spec on every pass, so a wrong version
	// costs nothing more than the fallback it caused.
	t.Run("correcting the version restores the calculated configuration", func(t *testing.T) {
		ctx := context.Background()
		cr := newCR(true, "5.7")
		r := newReconciler(t, cr)

		require.NoError(t, r.reconcileMySQLAutoConfig(ctx, cr))
		require.NotContains(t, autoConfig(t, r, cr), calculatedKey)

		cr.Spec.MySQL.AutoConfig.Version = "8.4"
		require.NoError(t, r.reconcileMySQLAutoConfig(ctx, cr))
		assert.Contains(t, autoConfig(t, r, cr), calculatedKey)
	})

	t.Run("correcting the version leaves no parameter of the wrong one behind", func(t *testing.T) {
		ctx := context.Background()
		// Removed in MySQL 8.4, still emitted for 8.0 — and emitted bare, so
		// mysqld would refuse to boot on it.
		const removedIn84 = "innodb_log_file_size="

		cr := newCR(true, "8.0.46")
		r := newReconciler(t, cr)

		require.NoError(t, r.reconcileMySQLAutoConfig(ctx, cr))
		require.Contains(t, autoConfig(t, r, cr), removedIn84, "precondition: the wrong version emits the removed parameter")

		cr.Spec.MySQL.AutoConfig.Version = "8.4.6"
		require.NoError(t, r.reconcileMySQLAutoConfig(ctx, cr))

		config := autoConfig(t, r, cr)
		assert.NotContains(t, config, removedIn84, "stale parameter survived the correction:\n%s", config)
		assert.Contains(t, config, calculatedKey, "corrected configuration in:\n%s", config)
	})
}

func withDataVolume(cr *apiv1.PerconaServerMySQL, size string) {
	cr.Spec.MySQL.VolumeSpec = &apiv1.VolumeSpec{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
		},
	}
}
