package psrestore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestGetBackupFromBackupSource(t *testing.T) {
	ctx := context.Background()

	const (
		restoreName = "restore1"
		clusterName = "cluster1"
		namespace   = "some-namespace"
	)

	storage := &apiv1.BackupStorageSpec{
		Type: apiv1.BackupStorageS3,
		S3: &apiv1.BackupStorageS3Spec{
			Bucket:            "some-bucket",
			CredentialsSecret: "some-secret",
			Region:            "us-west-2",
		},
	}

	newRestore := func(source *apiv1.RestoreBackupSource) *apiv1.PerconaServerMySQLRestore {
		return &apiv1.PerconaServerMySQLRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      restoreName,
				Namespace: namespace,
			},
			Spec: apiv1.PerconaServerMySQLRestoreSpec{
				ClusterName:  clusterName,
				BackupSource: source,
			},
		}
	}

	tests := []struct {
		name         string
		destination  apiv1.BackupDestination
		expectedType apiv1.BackupType
	}{
		{
			name:         "full backup",
			destination:  "s3://some-bucket/some-destination",
			expectedType: apiv1.BackupTypeFull,
		},
		{
			name:         "incremental backup",
			destination:  "s3://some-bucket/some-destination.incr/2026-03-17T000000",
			expectedType: apiv1.BackupTypeIncremental,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := newRestore(&apiv1.RestoreBackupSource{
				Destination: tt.destination,
				Storage:     storage.DeepCopy(),
			})

			backup, err := getBackup(ctx, buildFakeClient(t), cr, &apiv1.PerconaServerMySQL{})
			require.NoError(t, err)

			assert.Equal(t, restoreName, backup.Name)
			assert.Equal(t, namespace, backup.Namespace)
			assert.Equal(t, clusterName, backup.Spec.ClusterName)
			assert.Equal(t, tt.expectedType, backup.Spec.Type)
			assert.Equal(t, tt.expectedType, backup.Status.Type)
			assert.Equal(t, apiv1.BackupSucceeded, backup.Status.State)
			assert.Equal(t, tt.destination, backup.Status.Destination)
			assert.Equal(t, storage, backup.Status.Storage)
		})
	}

	t.Run("backup source is deep copied", func(t *testing.T) {
		cr := newRestore(&apiv1.RestoreBackupSource{
			Destination: "s3://some-bucket/some-destination",
			Storage:     storage.DeepCopy(),
		})

		backup, err := getBackup(ctx, buildFakeClient(t), cr, &apiv1.PerconaServerMySQL{})
		require.NoError(t, err)

		backup.Status.Storage.S3.Bucket = "modified-bucket"
		assert.Equal(t, apiv1.BucketWithPrefix("some-bucket"), cr.Spec.BackupSource.Storage.S3.Bucket)
	})

	t.Run("no backup name and no backup source", func(t *testing.T) {
		cr := newRestore(nil)

		_, err := getBackup(ctx, buildFakeClient(t), cr, &apiv1.PerconaServerMySQL{})
		assert.EqualError(t, err, "backupName and backupSource are empty")
	})
}

func TestGetBackupFromBackupName(t *testing.T) {
	ctx := context.Background()

	const (
		restoreName = "restore1"
		backupName  = "backup1"
		clusterName = "cluster1"
		namespace   = "some-namespace"
		storageName = "s3-us-west"
	)

	newRestore := func() *apiv1.PerconaServerMySQLRestore {
		return &apiv1.PerconaServerMySQLRestore{
			ObjectMeta: metav1.ObjectMeta{
				Name:      restoreName,
				Namespace: namespace,
			},
			Spec: apiv1.PerconaServerMySQLRestoreSpec{
				ClusterName: clusterName,
				BackupName:  backupName,
			},
		}
	}

	newBackup := func() *apiv1.PerconaServerMySQLBackup {
		return &apiv1.PerconaServerMySQLBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backupName,
				Namespace: namespace,
			},
			Spec: apiv1.PerconaServerMySQLBackupSpec{
				ClusterName: clusterName,
				StorageName: storageName,
			},
			Status: apiv1.PerconaServerMySQLBackupStatus{
				State:       apiv1.BackupSucceeded,
				Destination: "s3://some-bucket/some-destination",
			},
		}
	}

	newCluster := func(storages map[string]*apiv1.BackupStorageSpec) *apiv1.PerconaServerMySQL {
		return &apiv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: namespace,
			},
			Spec: apiv1.PerconaServerMySQLSpec{
				Backup: &apiv1.BackupSpec{
					Storages: storages,
				},
			},
		}
	}

	t.Run("backup not found", func(t *testing.T) {
		cr := newRestore()

		_, err := getBackup(ctx, buildFakeClient(t), cr, newCluster(nil))
		assert.EqualError(t, err, "PerconaServerMySQLBackup backup1 in namespace some-namespace is not found")
	})

	t.Run("storage is taken from cluster spec", func(t *testing.T) {
		cr := newRestore()
		cluster := newCluster(map[string]*apiv1.BackupStorageSpec{
			storageName: {
				Type: apiv1.BackupStorageS3,
				S3: &apiv1.BackupStorageS3Spec{
					Bucket: "some-bucket",
				},
			},
		})

		backup, err := getBackup(ctx, buildFakeClient(t, newBackup()), cr, cluster)
		require.NoError(t, err)

		require.NotNil(t, backup.Status.Storage)
		assert.Equal(t, apiv1.BackupStorageS3, backup.Status.Storage.Type)
		assert.Equal(t, apiv1.BucketWithPrefix("some-bucket"), backup.Status.Storage.S3.Bucket)

		backup.Status.Storage.S3.Bucket = "modified-bucket"
		assert.Equal(t, apiv1.BucketWithPrefix("some-bucket"), cluster.Spec.Backup.Storages[storageName].S3.Bucket)
	})

	t.Run("encryption key secret is inherited from cluster", func(t *testing.T) {
		cr := newRestore()
		cluster := newCluster(map[string]*apiv1.BackupStorageSpec{
			storageName: {
				Type: apiv1.BackupStorageS3,
				S3: &apiv1.BackupStorageS3Spec{
					Bucket: "some-bucket",
				},
			},
		})
		cluster.Spec.Backup.EncryptionKeySecret = &apiv1.EncryptionKeySecretSelector{
			Name: "encryption-key",
			Key:  "encryptionKey",
		}

		backup, err := getBackup(ctx, buildFakeClient(t, newBackup()), cr, cluster)
		require.NoError(t, err)

		require.NotNil(t, backup.Status.Storage)
		require.NotNil(t, backup.Status.Storage.EncryptionKeySecret)
		assert.Equal(t, "encryption-key", backup.Status.Storage.EncryptionKeySecret.Name)
	})

	t.Run("storage name not found in cluster spec", func(t *testing.T) {
		cr := newRestore()

		backup, err := getBackup(ctx, buildFakeClient(t, newBackup()), cr, newCluster(nil))
		require.NoError(t, err)
		assert.Nil(t, backup.Status.Storage)
	})
}
