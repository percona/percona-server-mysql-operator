package ps

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestGenerateBackupName(t *testing.T) {
	crMeta := func(name, ns string) *apiv1.PerconaServerMySQL {
		return &apiv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: apiv1.PerconaServerMySQLSpec{
				CRVersion: "1.0.0",
			},
		}
	}

	tn := time.Date(2022, time.February, 24, 1, 40, 0, 0, time.UTC)

	tests := []struct {
		name     string
		cr       *apiv1.PerconaServerMySQL
		schedule apiv1.BackupSchedule
		expected string
	}{
		{
			name: "short cr name and short storage name",
			cr:   crMeta("name", "ns"),
			schedule: apiv1.BackupSchedule{
				Name:        "schedule name",
				Schedule:    "*",
				Keep:        2,
				StorageName: "storage",
			},
			expected: "cron-name-storage-20220224014000-gpo1r",
		},
		{
			name: "truncated cr name and truncated storage name",
			cr:   crMeta("verylong-cr-name-truncated", "ns"),
			schedule: apiv1.BackupSchedule{
				Name:        "schedule name",
				Schedule:    "*",
				Keep:        2,
				StorageName: "verylong-storage-truncated",
			},
			expected: "cron-verylong-cr-name-verylong-storage-20220224014000-lct9h",
		},
		{
			name: "different namespace",
			cr:   crMeta("verylong-cr-name-truncated", "namespace"),
			schedule: apiv1.BackupSchedule{
				Name:        "schedule name",
				Schedule:    "*",
				Keep:        2,
				StorageName: "verylong-storage-truncated",
			},
			expected: "cron-verylong-cr-name-verylong-storage-20220224014000-qm8li",
		},
		{
			name: "different schedule name",
			cr:   crMeta("verylong-cr-name-truncated", "namespace"),
			schedule: apiv1.BackupSchedule{
				Name:        "schedule name2",
				Schedule:    "*",
				Keep:        2,
				StorageName: "verylong-storage-truncated",
			},
			expected: "cron-verylong-cr-name-verylong-storage-20220224014000-3n7s3",
		},
		{
			name: "different schedule",
			cr:   crMeta("verylong-cr-name-truncated", "namespace"),
			schedule: apiv1.BackupSchedule{
				Name:        "schedule name2",
				Schedule:    "* *",
				Keep:        2,
				StorageName: "verylong-storage-truncated",
			},
			expected: "cron-verylong-cr-name-verylong-storage-20220224014000-dkacu",
		},
		{
			name: "different keep",
			cr:   crMeta("verylong-cr-name-truncated", "namespace"),
			schedule: apiv1.BackupSchedule{
				Name:        "schedule name2",
				Schedule:    "* *",
				Keep:        3,
				StorageName: "verylong-storage-truncated",
			},
			expected: "cron-verylong-cr-name-verylong-storage-20220224014000-ko0ei",
		},
		{
			name: "different storage name",
			cr:   crMeta("verylong-cr-name-truncated", "namespace"),
			schedule: apiv1.BackupSchedule{
				Name:        "schedule name2",
				Schedule:    "* *",
				Keep:        3,
				StorageName: "verylong-storage-truncated-2",
			},
			expected: "cron-verylong-cr-name-verylong-storage-20220224014000-n6pdi",
		},
		{
			name: "different cr name",
			cr:   crMeta("verylong-cr-name-truncated-2", "namespace"),
			schedule: apiv1.BackupSchedule{
				Name:        "schedule name2",
				Schedule:    "* *",
				Keep:        3,
				StorageName: "verylong-storage-truncated-2",
			},
			expected: "cron-verylong-cr-name-verylong-storage-20220224014000-maqof",
		},
	}

	uniqueSuffixes := make(map[string]struct{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backupName, err := generateBackupName(tt.cr, tt.schedule, tn)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, backupName)

			suffix := backupName[len(backupName)-5:]
			assert.NotContains(t, uniqueSuffixes, suffix, "suffixes should be unique")
			uniqueSuffixes[suffix] = struct{}{}
		})
	}
}

func TestReconcileInternalEncryptionKeySecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster1",
			Namespace: "test-ns",
			UID:       types.UID("cluster1-uid"),
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			Backup: &apiv1.BackupSpec{
				EncryptionKeySecret: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "cluster-key",
					},
				},
				Storages: map[string]*apiv1.BackupStorageSpec{
					"s3": {
						EncryptionKeySecret: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "storage-key",
							},
							Key: "custom-key",
						},
					},
					"without-key": {},
					"nil":         nil,
				},
			},
		},
	}
	clusterKeySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-key",
			Namespace: cr.Namespace,
		},
		Data: map[string][]byte{
			"encryptionKey": []byte("cluster-secret-key"),
		},
	}
	storageKeySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "storage-key",
			Namespace: cr.Namespace,
		},
		Data: map[string][]byte{
			"custom-key": []byte("storage-secret-key"),
		},
	}

	r := &PerconaServerMySQLReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(cr, clusterKeySecret, storageKeySecret).
			Build(),
		Scheme: scheme,
	}

	require.NoError(t, r.reconcileInternalEncryptionKeySecret(t.Context(), cr))

	internalSecret := &corev1.Secret{}
	require.NoError(t, r.Get(t.Context(), client.ObjectKey{
		Name:      naming.EncryptionKeyInternalSecretName(cr.Name),
		Namespace: cr.Namespace,
	}, internalSecret))

	assert.Equal(t, map[string][]byte{
		naming.InternalEncryptionKeyFileName(cr.Name, ""):   []byte("cluster-secret-key"),
		naming.InternalEncryptionKeyFileName(cr.Name, "s3"): []byte("storage-secret-key"),
	}, internalSecret.Data)
	require.Len(t, internalSecret.OwnerReferences, 1)
	assert.Equal(t, cr.Name, internalSecret.OwnerReferences[0].Name)
	assert.Equal(t, cr.UID, internalSecret.OwnerReferences[0].UID)
	require.NotNil(t, internalSecret.OwnerReferences[0].Controller)
	assert.True(t, *internalSecret.OwnerReferences[0].Controller)
}
