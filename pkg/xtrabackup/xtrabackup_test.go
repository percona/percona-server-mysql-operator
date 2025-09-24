package xtrabackup

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
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
				StorageClass:      "storage-class",
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
			if idx == -1 {
				t.Fatal("missing CONTAINER_OPTIONS env var")
			}
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
				StorageClass:      "storage-class",
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
				StorageClass:      "storage-class",
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

		j := RestoreJob(cluster, destination, cr, cluster.Spec.Backup.Storages[storageName], initImage, "pvc-name")

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

		j := RestoreJob(cluster, destination, cr, storage, initImage, "pvc-name")

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
		j = RestoreJob(cluster, destination, cr, storage, initImage, "pvc-name")
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
		j = RestoreJob(cluster, destination, cr, storage, initImage, "pvc-name")
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
}
