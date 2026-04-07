package xtrabackup

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

func TestJob(t *testing.T) {
	const ns = "job-ns"
	const storageName = "some-storage"
	const destination = "prefix/destination"
	const initImage = "init-image"

	cr := readDefaultCluster(t, "cluster", ns)
	if err := cr.CheckNSetDefaults(t.Context(), &platform.ServerVersion{
		Platform: platform.PlatformKubernetes,
	}); err != nil {
		t.Fatal(err)
	}
	cr.Spec.Backup.Storages = map[string]*apiv1.BackupStorageSpec{
		storageName: {
			Annotations: map[string]string{
				"storage-annotation": "test",
			},
			Type: apiv1.BackupStorageS3,
			S3: &apiv1.BackupStorageS3Spec{
				Bucket:            "bucket",
				Prefix:            "prefix",
				CredentialsSecret: "secret",
				Region:            "region",
				EndpointURL:       "endpoint",
			},
		},
	}
	backup := readDefaultBackup(t, "backup", ns)

	t.Run("object meta", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cr := backup.DeepCopy()

		j, err := Job(cluster, cr, destination, initImage, cluster.Spec.Backup.Storages[storageName])
		if err != nil {
			t.Fatal(err)
		}

		assert.NotNil(t, j)
		assert.Equal(t, "xb-backup-minio", j.Name)
		assert.Equal(t, ns, j.Namespace)
		assert.Equal(t, map[string]string{
			"app.kubernetes.io/name":       "xtrabackup",
			"app.kubernetes.io/part-of":    "percona-server-backup",
			"app.kubernetes.io/instance":   "backup",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/component":  "backup",
		}, j.Labels)
		assert.Equal(t, map[string]string{"storage-annotation": "test"}, j.Annotations)
	})

	t.Run("image pull secrets", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cr := backup.DeepCopy()

		j, err := Job(cluster, cr, destination, initImage, cluster.Spec.Backup.Storages[storageName])
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, []corev1.LocalObjectReference(nil), j.Spec.Template.Spec.ImagePullSecrets)

		imagePullSecrets := []corev1.LocalObjectReference{
			{
				Name: "secret-1",
			},
			{
				Name: "secret-2",
			},
		}
		cluster.Spec.Backup.ImagePullSecrets = imagePullSecrets

		j, err = Job(cluster, cr, destination, initImage, cluster.Spec.Backup.Storages[storageName])
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, imagePullSecrets, j.Spec.Template.Spec.ImagePullSecrets)
	})

	t.Run("runtime class name", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cr := backup.DeepCopy()

		j, err := Job(cluster, cr, destination, initImage, cluster.Spec.Backup.Storages[storageName])
		if err != nil {
			t.Fatal(err)
		}
		var e *string
		assert.Equal(t, e, j.Spec.Template.Spec.RuntimeClassName)

		const runtimeClassName = "runtimeClassName"
		cluster.Spec.Backup.Storages[storageName].RuntimeClassName = ptr.To(runtimeClassName)

		j, err = Job(cluster, cr, destination, initImage, cluster.Spec.Backup.Storages[storageName])
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, runtimeClassName, *j.Spec.Template.Spec.RuntimeClassName)
	})

	t.Run("global labels and annotations", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cr := backup.DeepCopy()

		cluster.Spec.Metadata = &apiv1.Metadata{
			Labels: map[string]string{
				"custom-label": "label",
			},
			Annotations: map[string]string{
				"custom-annotation": "annotation",
			},
		}

		j, err := Job(cluster, cr, destination, initImage, cluster.Spec.Backup.Storages[storageName])
		if err != nil {
			t.Fatal(err)
		}

		expectedLabels := map[string]string{
			"app.kubernetes.io/component":  "backup",
			"app.kubernetes.io/instance":   "backup",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/name":       "xtrabackup",
			"app.kubernetes.io/part-of":    "percona-server-backup",
			"custom-label":                 "label",
		}
		expectedAnnotations := map[string]string{
			"storage-annotation": "test",
			"custom-annotation":  "annotation",
		}
		assert.Equal(t, expectedLabels, j.Labels)
		assert.Equal(t, expectedAnnotations, j.Annotations)
	})
	t.Run("container options", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cr := backup.DeepCopy()

		storage := cluster.Spec.Backup.Storages[storageName]
		storage.ContainerOptions = &apiv1.BackupContainerOptions{
			Env: []corev1.EnvVar{
				{
					Name:  "STORAGE_ENV_VAR",
					Value: "VALUE",
				},
			},
			Args: apiv1.BackupContainerArgs{
				Xtrabackup: []string{"storage-arg-xbcloud"},
				Xbcloud:    []string{"storage-arg-xtrabackup"},
				Xbstream:   []string{"storage-arg-xbcloud"},
			},
		}

		j, err := Job(cluster, cr, destination, initImage, storage)
		require.NoError(t, err)
		getEnv := func() string {
			xbContainer := j.Spec.Template.Spec.Containers[0]
			idx := slices.IndexFunc(xbContainer.Env, func(env corev1.EnvVar) bool {
				return env.Name == "CONTAINER_OPTIONS"
			})
			require.Positive(t, idx, "missing CONTAINER_OPTIONS env var")
			return xbContainer.Env[idx].Value
		}

		assert.Equal(t, `{"env":[{"name":"STORAGE_ENV_VAR","value":"VALUE"}],"args":{"xtrabackup":["storage-arg-xbcloud"],"xbcloud":["storage-arg-xtrabackup"],"xbstream":["storage-arg-xbcloud"]}}`, getEnv())

		cr.Spec.ContainerOptions = &apiv1.BackupContainerOptions{
			Env: []corev1.EnvVar{
				{
					Name:  "BACKUP_ENV_VAR",
					Value: "VALUE",
				},
			},
			Args: apiv1.BackupContainerArgs{
				Xtrabackup: []string{"backup-arg-xbcloud"},
				Xbcloud:    []string{"backup-arg-xtrabackup"},
				Xbstream:   []string{"backup-arg-xbcloud"},
			},
		}

		j, err = Job(cluster, cr, destination, initImage, storage)
		require.NoError(t, err)

		assert.Equal(t, `{"env":[{"name":"BACKUP_ENV_VAR","value":"VALUE"}],"args":{"xtrabackup":["backup-arg-xbcloud"],"xbcloud":["backup-arg-xtrabackup"],"xbstream":["backup-arg-xbcloud"]}}`, getEnv())

		cr.Spec.ContainerOptions = &apiv1.BackupContainerOptions{
			Env:  []corev1.EnvVar{},
			Args: apiv1.BackupContainerArgs{},
		}

		j, err = Job(cluster, cr, destination, initImage, storage)
		require.NoError(t, err)

		assert.Equal(t, `{"args":{}}`, getEnv())
	})
}

func TestDeleteJob(t *testing.T) {
	ctx := t.Context()

	const ns = "delete-job-ns"
	const storageName = "some-storage"

	cr := readDefaultCluster(t, "cluster", ns)
	if err := cr.CheckNSetDefaults(t.Context(), &platform.ServerVersion{
		Platform: platform.PlatformKubernetes,
	}); err != nil {
		t.Fatal(err)
	}
	cr.Spec.Backup.Storages = map[string]*apiv1.BackupStorageSpec{
		storageName: {
			Annotations: map[string]string{
				"storage-annotation": "test",
			},
			Type: apiv1.BackupStorageS3,
			S3: &apiv1.BackupStorageS3Spec{
				Bucket:            "bucket",
				Prefix:            "prefix",
				CredentialsSecret: "secret",
				Region:            "region",
				EndpointURL:       "endpoint",
			},
		},
	}
	backup := readDefaultBackup(t, "backup", ns)
	backup.Status.Storage = cr.Spec.Backup.Storages[storageName]

	t.Run("global labels and annotations", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cr := backup.DeepCopy()

		cluster.Spec.Metadata = &apiv1.Metadata{
			Labels: map[string]string{
				"custom-label": "label",
			},
			Annotations: map[string]string{
				"custom-annotation": "annotation",
			},
		}

		cfg, err := GetBackupConfig(ctx, nil, cr)
		if err != nil {
			t.Fatal(err)
		}
		j := GetDeleteJob(cluster, cr, cfg)

		expectedLabels := map[string]string{
			"app.kubernetes.io/component":  "backup",
			"app.kubernetes.io/instance":   "backup",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/name":       "xtrabackup",
			"app.kubernetes.io/part-of":    "percona-server-backup",
			"custom-label":                 "label",
		}
		expectedAnnotations := map[string]string{
			"storage-annotation": "test",
			"custom-annotation":  "annotation",
		}
		assert.Equal(t, expectedLabels, j.Labels)
		assert.Equal(t, expectedAnnotations, j.Annotations)
	})
	t.Run("container options", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cr := backup.DeepCopy()

		storage := cr.Status.Storage
		storage.ContainerOptions = &apiv1.BackupContainerOptions{
			Env: []corev1.EnvVar{
				{
					Name:  "STORAGE_ENV_VAR",
					Value: "VALUE",
				},
			},
			Args: apiv1.BackupContainerArgs{
				Xtrabackup: []string{"storage-arg-xtrabackup"},
				Xbcloud:    []string{"storage-arg-xbcloud"},
				Xbstream:   []string{"storage-arg-xbstream"},
			},
		}

		cfg, err := GetBackupConfig(ctx, nil, cr)
		require.NoError(t, err)

		j := GetDeleteJob(cluster, cr, cfg)

		getEnv := func() []corev1.EnvVar {
			xbContainer := j.Spec.Template.Spec.Containers[0]
			return xbContainer.Env
		}
		getCmd := func() []string {
			xbContainer := j.Spec.Template.Spec.Containers[0]
			return xbContainer.Command
		}

		assert.Equal(t, []corev1.EnvVar{
			{
				Name:  "STORAGE_ENV_VAR",
				Value: "VALUE",
			},
			{
				Name:  "XB_EXTRA_ARGS",
				Value: "storage-arg-xtrabackup",
			},
			{
				Name:  "XBCLOUD_EXTRA_ARGS",
				Value: "storage-arg-xbcloud",
			},
			{
				Name:  "XBSTREAM_EXTRA_ARGS",
				Value: "storage-arg-xbstream",
			},
		}, getEnv())
		assert.Equal(t, []string{"xbcloud", "delete", "--parallel=10", "--curl-retriable-errors=7", "storage-arg-xbcloud", "--md5", "--storage=s3", "--s3-bucket=bucket", "--s3-region=region", "--s3-access-key=", "--s3-secret-key=", "--s3-endpoint=endpoint", "prefix/ps-cluster1-0001-01-01-00:00:00-full"}, getCmd())

		cr.Spec.ContainerOptions = &apiv1.BackupContainerOptions{
			Env: []corev1.EnvVar{
				{
					Name:  "BACKUP_ENV_VAR",
					Value: "VALUE",
				},
			},
			Args: apiv1.BackupContainerArgs{
				Xtrabackup: []string{"backup-arg-xtrabackup"},
				Xbcloud:    []string{"backup-arg-xbcloud"},
				Xbstream:   []string{"backup-arg-xbstream"},
			},
		}

		cfg, err = GetBackupConfig(ctx, nil, cr)
		require.NoError(t, err)
		j = GetDeleteJob(cluster, cr, cfg)

		assert.Equal(t, []corev1.EnvVar{
			{
				Name:  "BACKUP_ENV_VAR",
				Value: "VALUE",
			},
			{
				Name:  "XB_EXTRA_ARGS",
				Value: "backup-arg-xtrabackup",
			},
			{
				Name:  "XBCLOUD_EXTRA_ARGS",
				Value: "backup-arg-xbcloud",
			},
			{
				Name:  "XBSTREAM_EXTRA_ARGS",
				Value: "backup-arg-xbstream",
			},
		}, getEnv())
		assert.Equal(t, []string{"xbcloud", "delete", "--parallel=10", "--curl-retriable-errors=7", "backup-arg-xbcloud", "--md5", "--storage=s3", "--s3-bucket=bucket", "--s3-region=region", "--s3-access-key=", "--s3-secret-key=", "--s3-endpoint=endpoint", "prefix/ps-cluster1-0001-01-01-00:00:00-full"}, getCmd())

		cr.Spec.ContainerOptions = &apiv1.BackupContainerOptions{
			Env:  []corev1.EnvVar{},
			Args: apiv1.BackupContainerArgs{},
		}

		cfg, err = GetBackupConfig(ctx, nil, cr)
		require.NoError(t, err)
		j = GetDeleteJob(cluster, cr, cfg)

		assert.Equal(t, []corev1.EnvVar{}, getEnv())
		assert.Equal(t, []string{"xbcloud", "delete", "--parallel=10", "--curl-retriable-errors=7", "--md5", "--storage=s3", "--s3-bucket=bucket", "--s3-region=region", "--s3-access-key=", "--s3-secret-key=", "--s3-endpoint=endpoint", "prefix/ps-cluster1-0001-01-01-00:00:00-full"}, getCmd())
	})
}

func TestRestoreJob(t *testing.T) {
	const ns = "restore-job-ns"
	const storageName = "some-storage"
	const initImage = "init-image"

	destination := DestinationInfo{
		Base: "destination",
	}

	cr := readDefaultCluster(t, "cluster", ns)
	if err := cr.CheckNSetDefaults(t.Context(), &platform.ServerVersion{
		Platform: platform.PlatformKubernetes,
	}); err != nil {
		t.Fatal(err)
	}
	cr.Spec.Backup.Storages = map[string]*apiv1.BackupStorageSpec{
		storageName: {
			Annotations: map[string]string{
				"storage-annotation": "test",
			},
			Type: apiv1.BackupStorageS3,
			S3: &apiv1.BackupStorageS3Spec{
				Bucket:            "bucket",
				Prefix:            "prefix",
				CredentialsSecret: "secret",
				Region:            "region",
				EndpointURL:       "endpoint",
			},
		},
	}
	backup := readDefaultRestore(t, "backup", ns)

	t.Run("global labels and annotations", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cr := backup.DeepCopy()

		cluster.Spec.Metadata = &apiv1.Metadata{
			Labels: map[string]string{
				"custom-label": "label",
			},
			Annotations: map[string]string{
				"custom-annotation": "annotation",
			},
		}

		j := RestoreJob(cluster, destination, cr, cluster.Spec.Backup.Storages[storageName], initImage, "pvc-name", false)

		expectedLabels := map[string]string{
			"app.kubernetes.io/component":  "restore",
			"app.kubernetes.io/instance":   "backup",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/name":       "xtrabackup",
			"app.kubernetes.io/part-of":    "percona-server-restore",
			"custom-label":                 "label",
		}
		expectedAnnotations := map[string]string{
			"storage-annotation": "test",
			"custom-annotation":  "annotation",
		}
		assert.Equal(t, expectedLabels, j.Labels)
		assert.Equal(t, expectedAnnotations, j.Annotations)
	})

	t.Run("container options", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cr := backup.DeepCopy()

		storage := cluster.Spec.Backup.Storages[storageName]
		storage.ContainerOptions = &apiv1.BackupContainerOptions{
			Env: []corev1.EnvVar{
				{
					Name:  "STORAGE_ENV_VAR",
					Value: "VALUE",
				},
			},
			Args: apiv1.BackupContainerArgs{
				Xtrabackup: []string{"storage-arg-xtrabackup"},
				Xbcloud:    []string{"storage-arg-xbcloud"},
				Xbstream:   []string{"storage-arg-xbstream"},
			},
		}

		j := RestoreJob(cluster, destination, cr, storage, initImage, "pvc-name", false)

		getEnv := func() []corev1.EnvVar {
			xbContainer := j.Spec.Template.Spec.Containers[0]
			return xbContainer.Env
		}

		assert.Equal(t, []corev1.EnvVar{
			{
				Name:  "RESTORE_NAME",
				Value: "backup",
			},
			{
				Name:  "BACKUP_DEST",
				Value: "destination",
			},
			{
				Name:  "VERIFY_TLS",
				Value: "true",
			},
			{
				Name:  "KEYRING_VAULT_PATH",
				Value: "/etc/mysql/vault-keyring-secret/keyring_vault.cnf",
			},
			{
				Name:  "STORAGE_ENV_VAR",
				Value: "VALUE",
			},
			{
				Name:  "XB_EXTRA_ARGS",
				Value: "storage-arg-xtrabackup",
			},
			{
				Name:  "XBCLOUD_EXTRA_ARGS",
				Value: "storage-arg-xbcloud",
			},
			{
				Name:  "XBSTREAM_EXTRA_ARGS",
				Value: "storage-arg-xbstream",
			},
		}, getEnv())

		cr.Spec.ContainerOptions = &apiv1.BackupContainerOptions{
			Env: []corev1.EnvVar{
				{
					Name:  "RESTORE_ENV_VAR",
					Value: "VALUE",
				},
			},
			Args: apiv1.BackupContainerArgs{
				Xtrabackup: []string{"restore-arg-xtrabackup"},
				Xbcloud:    []string{"restore-arg-xbcloud"},
				Xbstream:   []string{"restore-arg-xbstream"},
			},
		}
		j = RestoreJob(cluster, destination, cr, storage, initImage, "pvc-name", false)
		assert.Equal(t, []corev1.EnvVar{
			{
				Name:  "RESTORE_NAME",
				Value: "backup",
			},
			{
				Name:  "BACKUP_DEST",
				Value: "destination",
			},
			{
				Name:  "VERIFY_TLS",
				Value: "true",
			},
			{
				Name:  "KEYRING_VAULT_PATH",
				Value: "/etc/mysql/vault-keyring-secret/keyring_vault.cnf",
			},
			{
				Name:  "RESTORE_ENV_VAR",
				Value: "VALUE",
			},
			{
				Name:  "XB_EXTRA_ARGS",
				Value: "restore-arg-xtrabackup",
			},
			{
				Name:  "XBCLOUD_EXTRA_ARGS",
				Value: "restore-arg-xbcloud",
			},
			{
				Name:  "XBSTREAM_EXTRA_ARGS",
				Value: "restore-arg-xbstream",
			},
		}, getEnv())

		cr.Spec.ContainerOptions = &apiv1.BackupContainerOptions{
			Env:  []corev1.EnvVar{},
			Args: apiv1.BackupContainerArgs{},
		}
		j = RestoreJob(cluster, destination, cr, storage, initImage, "pvc-name", false)
		assert.Equal(t, []corev1.EnvVar{
			{
				Name:  "RESTORE_NAME",
				Value: "backup",
			},
			{
				Name:  "BACKUP_DEST",
				Value: "destination",
			},
			{
				Name:  "VERIFY_TLS",
				Value: "true",
			},
			{
				Name:  "KEYRING_VAULT_PATH",
				Value: "/etc/mysql/vault-keyring-secret/keyring_vault.cnf",
			},
		}, getEnv())
	})

	t.Run("compressed flag", func(t *testing.T) {
		cluster := cr.DeepCopy()
		restore := backup.DeepCopy()
		storage := cluster.Spec.Backup.Storages[storageName]

		hasEnv := func(envs []corev1.EnvVar, name, value string) bool {
			return slices.ContainsFunc(envs, func(e corev1.EnvVar) bool {
				return e.Name == name && e.Value == value
			})
		}

		j := RestoreJob(cluster, destination, restore, storage, initImage, "pvc-name", true)
		envs := j.Spec.Template.Spec.Containers[0].Env
		assert.True(t, hasEnv(envs, "BACKUP_COMPRESSED", "true"))

		j = RestoreJob(cluster, destination, restore, storage, initImage, "pvc-name", false)
		envs = j.Spec.Template.Spec.Containers[0].Env
		assert.False(t, hasEnv(envs, "BACKUP_COMPRESSED", "true"))
	})
}

func TestGetDestination(t *testing.T) {
	ts := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)

	newBackup := func(clusterName string, backupType apiv1.BackupType) *apiv1.PerconaServerMySQLBackup {
		return &apiv1.PerconaServerMySQLBackup{
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(ts),
			},
			Spec: apiv1.PerconaServerMySQLBackupSpec{
				Type:        backupType,
				ClusterName: clusterName,
			},
		}
	}

	testCases := []struct {
		desc     string
		storage  *apiv1.BackupStorageSpec
		backup   func() *apiv1.PerconaServerMySQLBackup
		expected apiv1.BackupDestination
		wantErr  error
	}{
		{
			desc: "s3 full backup",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageS3,
				S3: &apiv1.BackupStorageS3Spec{
					Bucket: "my-bucket",
					Prefix: "backups",
				},
			},
			backup:   func() *apiv1.PerconaServerMySQLBackup { return newBackup("my-cluster", apiv1.BackupTypeFull) },
			expected: apiv1.BackupDestination("s3://my-bucket/backups/my-cluster-2024-06-15-10:30:00-full"),
		},
		{
			desc: "s3 full backup without prefix",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageS3,
				S3: &apiv1.BackupStorageS3Spec{
					Bucket: "my-bucket",
				},
			},
			backup:   func() *apiv1.PerconaServerMySQLBackup { return newBackup("my-cluster", apiv1.BackupTypeFull) },
			expected: apiv1.BackupDestination("s3://my-bucket/my-cluster-2024-06-15-10:30:00-full"),
		},
		{
			desc: "gcs full backup",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageGCS,
				GCS: &apiv1.BackupStorageGCSSpec{
					Bucket: "gcs-bucket",
					Prefix: "db-backups",
				},
			},
			backup:   func() *apiv1.PerconaServerMySQLBackup { return newBackup("cluster1", apiv1.BackupTypeFull) },
			expected: apiv1.BackupDestination("gs://gcs-bucket/db-backups/cluster1-2024-06-15-10:30:00-full"),
		},
		{
			desc: "azure full backup",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageAzure,
				Azure: &apiv1.BackupStorageAzureSpec{
					ContainerName: "my-container",
					Prefix:        "azure-backups",
				},
			},
			backup:   func() *apiv1.PerconaServerMySQLBackup { return newBackup("cluster1", apiv1.BackupTypeFull) },
			expected: apiv1.BackupDestination("my-container/azure-backups/cluster1-2024-06-15-10:30:00-full"),
		},
		{
			desc: "incremental backup with prefix",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageS3,
				S3: &apiv1.BackupStorageS3Spec{
					Bucket: "my-bucket",
					Prefix: "backups",
				},
			},
			backup: func() *apiv1.PerconaServerMySQLBackup {
				bcp := newBackup("my-cluster", apiv1.BackupTypeIncremental)
				bcp.SetAnnotations(map[string]string{
					string(naming.AnnotationBaseBackupName): "my-cluster-2024-06-14-08:00:00-full",
				})
				return bcp
			},
			expected: apiv1.BackupDestination("s3://my-bucket/backups/my-cluster-2024-06-14-08:00:00-full.incr/my-cluster-2024-06-15-10:30:00-incr"),
		},
		{
			desc: "incremental backup without prefix",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageS3,
				S3: &apiv1.BackupStorageS3Spec{
					Bucket: "my-bucket",
				},
			},
			backup: func() *apiv1.PerconaServerMySQLBackup {
				bcp := newBackup("my-cluster", apiv1.BackupTypeIncremental)
				bcp.SetAnnotations(map[string]string{
					string(naming.AnnotationBaseBackupName): "my-cluster-2024-06-14-08:00:00-full",
				})
				return bcp
			},
			expected: apiv1.BackupDestination("s3://my-bucket/my-cluster-2024-06-14-08:00:00-full.incr/my-cluster-2024-06-15-10:30:00-incr"),
		},
		{
			desc: "incremental backup missing base annotation",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageS3,
				S3: &apiv1.BackupStorageS3Spec{
					Bucket: "my-bucket",
				},
			},
			backup: func() *apiv1.PerconaServerMySQLBackup {
				return newBackup("my-cluster", apiv1.BackupTypeIncremental)
			},
			wantErr: errors.New("base backup name not known in annotations"),
		},
		{
			desc: "s3 bucket with embedded prefix",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageS3,
				S3: &apiv1.BackupStorageS3Spec{
					Bucket: "my-bucket/sub-path",
				},
			},
			backup: func() *apiv1.PerconaServerMySQLBackup {
				return newBackup("cluster1", apiv1.BackupTypeFull)
			},
			expected: apiv1.BackupDestination("s3://my-bucket/sub-path/cluster1-2024-06-15-10:30:00-full"),
		},
		{
			desc: "s3 type with nil s3 spec",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageS3,
			},
			backup: func() *apiv1.PerconaServerMySQLBackup {
				return newBackup("cluster1", apiv1.BackupTypeFull)
			},
			wantErr: errors.New("storage type is s3, but s3 configuration is not specified"),
		},
		{
			desc: "gcs type with nil gcs spec",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageGCS,
			},
			backup: func() *apiv1.PerconaServerMySQLBackup {
				return newBackup("cluster1", apiv1.BackupTypeFull)
			},
			wantErr: errors.New("storage type is gcs, but gcs configuration is not specified"),
		},
		{
			desc: "azure type with nil azure spec",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageAzure,
			},
			backup: func() *apiv1.PerconaServerMySQLBackup {
				return newBackup("cluster1", apiv1.BackupTypeFull)
			},
			wantErr: errors.New("storage type is azure, but azure configuration is not specified"),
		},
		{
			desc: "unsupported storage type",
			storage: &apiv1.BackupStorageSpec{
				Type: apiv1.BackupStorageType("unsupported"),
			},
			backup:  func() *apiv1.PerconaServerMySQLBackup { return newBackup("my-cluster", apiv1.BackupTypeFull) },
			wantErr: errors.New("storage type unsupported is not supported"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			dest, err := GetDestination(tc.storage, tc.backup())
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr.Error())
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expected, dest)
			}
		})
	}
}

func TestCheckpointInfoParseFrom(t *testing.T) {
	t.Run("full checkpoint file", func(t *testing.T) {
		input := `backup_type = full-backuped
from_lsn = 0
to_lsn = 18446744073709551615
last_lsn = 18446744073709551615
flushed_lsn = 18446744073709551615
redo_memory = 0
redo_frames = 0
`
		var info CheckpointInfo
		err := info.ParseFrom(strings.NewReader(input))
		require.NoError(t, err)

		assert.Equal(t, "full-backuped", info.BackupType)
		assert.Equal(t, "0", info.FromLSN)
		assert.Equal(t, "18446744073709551615", info.ToLSN)
		assert.Equal(t, "18446744073709551615", info.LastLSN)
		assert.Equal(t, "18446744073709551615", info.FlushedLSN)
		assert.Equal(t, "0", info.RedoMemory)
		assert.Equal(t, "0", info.RedoFrames)
	})

	t.Run("incremental checkpoint file", func(t *testing.T) {
		input := `backup_type = incremental
from_lsn = 27655332
to_lsn = 27660845
last_lsn = 27660855
flushed_lsn = 27660855
redo_memory = 0
redo_frames = 0
`
		var info CheckpointInfo
		err := info.ParseFrom(strings.NewReader(input))
		require.NoError(t, err)

		assert.Equal(t, "incremental", info.BackupType)
		assert.Equal(t, "27655332", info.FromLSN)
		assert.Equal(t, "27660845", info.ToLSN)
		assert.Equal(t, "27660855", info.LastLSN)
		assert.Equal(t, "27660855", info.FlushedLSN)
	})

	t.Run("extra whitespace", func(t *testing.T) {
		input := "  backup_type  =  full-prepared  \n  from_lsn  =  100  \n"

		var info CheckpointInfo
		err := info.ParseFrom(strings.NewReader(input))
		require.NoError(t, err)

		assert.Equal(t, "full-prepared", info.BackupType)
		assert.Equal(t, "100", info.FromLSN)
	})

	t.Run("lines without equals are skipped", func(t *testing.T) {
		input := "this line has no separator\nbackup_type = full-backuped\nmalformed line\n"

		var info CheckpointInfo
		err := info.ParseFrom(strings.NewReader(input))
		require.NoError(t, err)

		assert.Equal(t, "full-backuped", info.BackupType)
	})

	t.Run("empty input", func(t *testing.T) {
		var info CheckpointInfo
		err := info.ParseFrom(strings.NewReader(""))
		require.NoError(t, err)

		assert.Equal(t, CheckpointInfo{}, info)
	})

	t.Run("partial fields", func(t *testing.T) {
		input := "to_lsn = 999\nredo_frames = 42\n"

		var info CheckpointInfo
		err := info.ParseFrom(strings.NewReader(input))
		require.NoError(t, err)

		assert.Equal(t, "", info.BackupType)
		assert.Equal(t, "", info.FromLSN)
		assert.Equal(t, "999", info.ToLSN)
		assert.Equal(t, "", info.LastLSN)
		assert.Equal(t, "42", info.RedoFrames)
	})

}
