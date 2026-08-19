package psbackup

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

type fakeStorageWithSize struct{}

func (c *fakeStorageWithSize) GetObject(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, nil
}
func (c *fakeStorageWithSize) PutObject(_ context.Context, _ string, _ io.Reader, _ int64) error {
	return nil
}
func (c *fakeStorageWithSize) ListObjects(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (c *fakeStorageWithSize) ListObjectsWithSize(_ context.Context, _ string) ([]storage.ObjectInfo, error) {
	return []storage.ObjectInfo{
		{Name: "file1.xb", Size: 40000},
		{Name: "file2.xb", Size: 38771},
	}, nil
}
func (c *fakeStorageWithSize) DeleteObject(_ context.Context, _ string) error { return nil }
func (c *fakeStorageWithSize) SetPrefix(_ string)                             {}
func (c *fakeStorageWithSize) GetPrefix() string                              { return "" }

type fakeStorageWithSizeValidatingPrefix struct {
	t              *testing.T
	expectedPrefix string
	storagePrefix  string
}

func (c *fakeStorageWithSizeValidatingPrefix) GetObject(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, nil
}
func (c *fakeStorageWithSizeValidatingPrefix) PutObject(_ context.Context, _ string, _ io.Reader, _ int64) error {
	return nil
}
func (c *fakeStorageWithSizeValidatingPrefix) ListObjects(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (c *fakeStorageWithSizeValidatingPrefix) ListObjectsWithSize(_ context.Context, prefix string) ([]storage.ObjectInfo, error) {
	assert.Equal(c.t, c.expectedPrefix, prefix)
	return []storage.ObjectInfo{
		{Name: "file1.xb.delta", Size: 5000},
		{Name: "file2.xb.delta", Size: 3000},
	}, nil
}
func (c *fakeStorageWithSizeValidatingPrefix) DeleteObject(_ context.Context, _ string) error {
	return nil
}
func (c *fakeStorageWithSizeValidatingPrefix) SetPrefix(_ string) {}
func (c *fakeStorageWithSizeValidatingPrefix) GetPrefix() string  { return c.storagePrefix }

func TestBackupSizeOnSuccess(t *testing.T) {
	const namespace = "backup-size-test"
	const storageName = "s3-us-west"

	ctx := context.Background()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	cluster, err := readDefaultCR("ps-cluster1", namespace)
	require.NoError(t, err)

	cr, err := readDefaultCRBackup("some-name", namespace)
	require.NoError(t, err)

	cr.Spec.ClusterName = cluster.Name
	cr.Spec.StorageName = storageName

	stor := cluster.Spec.Backup.Storages[storageName]

	s3Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: stor.S3.CredentialsSecret, Namespace: namespace},
		Data: map[string][]byte{
			secret.CredentialsAWSAccessKey: []byte("access-key"),
			secret.CredentialsAWSSecretKey: []byte("secret-key"),
		},
	}
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.InternalSecretName(), Namespace: namespace},
		Data: map[string][]byte{
			string(apiv1.UserOperator): []byte("operator-pass"),
		},
	}

	// Set backup status to Running (simulating an in-progress backup)
	cr.Status.State = apiv1.BackupRunning
	cr.Status.Storage = stor.DeepCopy()
	cr.Status.Destination = "s3://bucket/backup-name"

	// Create a completed job
	job, err := xtrabackup.Job(cluster.DeepCopy(), cr, "dest", "init-image", stor)
	require.NoError(t, err)
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type:   batchv1.JobComplete,
		Status: corev1.ConditionTrue,
	})

	fakeStorageClient := func(ctx context.Context, opts storage.Options) (storage.Storage, error) {
		return &fakeStorageWithSize{}, nil
	}

	cb := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cr, cluster.DeepCopy(), s3Secret, userSecret, job).
		WithStatusSubresource(cr, cluster.DeepCopy(), s3Secret, job)

	r := PerconaServerMySQLBackupReconciler{
		Client:           cb.Build(),
		Scheme:           scheme,
		ServerVersion:    &platform.ServerVersion{Platform: platform.PlatformKubernetes},
		NewStorageClient: fakeStorageClient,
	}

	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
	require.NoError(t, err)

	actual := new(apiv1.PerconaServerMySQLBackup)
	err = r.Get(ctx, client.ObjectKeyFromObject(cr), actual)
	require.NoError(t, err)

	assert.Equal(t, apiv1.BackupSucceeded, actual.Status.State)
	assert.Equal(t, "76.92KB", actual.Status.Size)
}

func TestBackupSizeIncrementalOnSuccess(t *testing.T) {
	const namespace = "backup-size-incr-test"
	const storageName = "s3-us-west"

	ctx := context.Background()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	cluster, err := readDefaultCR("ps-cluster1", namespace)
	require.NoError(t, err)

	cr, err := readDefaultCRBackup("some-name", namespace)
	require.NoError(t, err)

	cr.Spec.ClusterName = cluster.Name
	cr.Spec.StorageName = storageName

	stor := cluster.Spec.Backup.Storages[storageName]

	s3Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: stor.S3.CredentialsSecret, Namespace: namespace},
		Data: map[string][]byte{
			secret.CredentialsAWSAccessKey: []byte("access-key"),
			secret.CredentialsAWSSecretKey: []byte("secret-key"),
		},
	}
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.InternalSecretName(), Namespace: namespace},
		Data: map[string][]byte{
			string(apiv1.UserOperator): []byte("operator-pass"),
		},
	}

	// Set backup status to Running with incremental backup destination
	cr.Status.State = apiv1.BackupRunning
	cr.Status.Storage = stor.DeepCopy()
	cr.Status.Destination = "s3://bucket/base-backup-full.incr/incr-backup-name"
	cr.Status.Type = apiv1.BackupTypeIncremental

	// Create a completed job
	job, err := xtrabackup.Job(cluster.DeepCopy(), cr, "dest", "init-image", stor)
	require.NoError(t, err)
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type:   batchv1.JobComplete,
		Status: corev1.ConditionTrue,
	})

	fakeStorageClient := func(ctx context.Context, opts storage.Options) (storage.Storage, error) {
		return &fakeStorageWithSizeValidatingPrefix{
			t:              t,
			expectedPrefix: "base-backup-full.incr/incr-backup-name",
		}, nil
	}

	cb := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cr, cluster.DeepCopy(), s3Secret, userSecret, job).
		WithStatusSubresource(cr, cluster.DeepCopy(), s3Secret, job)

	r := PerconaServerMySQLBackupReconciler{
		Client:           cb.Build(),
		Scheme:           scheme,
		ServerVersion:    &platform.ServerVersion{Platform: platform.PlatformKubernetes},
		NewStorageClient: fakeStorageClient,
	}

	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
	require.NoError(t, err)

	actual := new(apiv1.PerconaServerMySQLBackup)
	err = r.Get(ctx, client.ObjectKeyFromObject(cr), actual)
	require.NoError(t, err)

	assert.Equal(t, apiv1.BackupSucceeded, actual.Status.State)
	assert.Equal(t, "7.81KB", actual.Status.Size)
}

func TestBackupSizeIncrementalWithStoragePrefix(t *testing.T) {
	const namespace = "backup-size-incr-prefix-test"
	const storageName = "s3-us-west"

	ctx := context.Background()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	cluster, err := readDefaultCR("ps-cluster1", namespace)
	require.NoError(t, err)

	cr, err := readDefaultCRBackup("some-name", namespace)
	require.NoError(t, err)

	cr.Spec.ClusterName = cluster.Name
	cr.Spec.StorageName = storageName

	stor := cluster.Spec.Backup.Storages[storageName]
	// Simulate storage with prefix in bucket name
	stor.S3.Bucket = "mybucket/myprefix"

	s3Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: stor.S3.CredentialsSecret, Namespace: namespace},
		Data: map[string][]byte{
			secret.CredentialsAWSAccessKey: []byte("access-key"),
			secret.CredentialsAWSSecretKey: []byte("secret-key"),
		},
	}
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.InternalSecretName(), Namespace: namespace},
		Data: map[string][]byte{
			string(apiv1.UserOperator): []byte("operator-pass"),
		},
	}

	// Set backup status with incremental destination that includes storage prefix
	cr.Status.State = apiv1.BackupRunning
	cr.Status.Storage = stor.DeepCopy()
	cr.Status.Destination = "s3://mybucket/myprefix/base-backup-full.incr/incr-backup-name"
	cr.Status.Type = apiv1.BackupTypeIncremental

	// Create a completed job
	job, err := xtrabackup.Job(cluster.DeepCopy(), cr, "dest", "init-image", stor)
	require.NoError(t, err)
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type:   batchv1.JobComplete,
		Status: corev1.ConditionTrue,
	})

	fakeStorageClient := func(ctx context.Context, opts storage.Options) (storage.Storage, error) {
		return &fakeStorageWithSizeValidatingPrefix{
			t:              t,
			expectedPrefix: "base-backup-full.incr/incr-backup-name",
			storagePrefix:  "myprefix/",
		}, nil
	}

	cb := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cr, cluster.DeepCopy(), s3Secret, userSecret, job).
		WithStatusSubresource(cr, cluster.DeepCopy(), s3Secret, job)

	r := PerconaServerMySQLBackupReconciler{
		Client:           cb.Build(),
		Scheme:           scheme,
		ServerVersion:    &platform.ServerVersion{Platform: platform.PlatformKubernetes},
		NewStorageClient: fakeStorageClient,
	}

	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
	require.NoError(t, err)

	actual := new(apiv1.PerconaServerMySQLBackup)
	err = r.Get(ctx, client.ObjectKeyFromObject(cr), actual)
	require.NoError(t, err)

	assert.Equal(t, apiv1.BackupSucceeded, actual.Status.State)
	assert.Equal(t, "7.81KB", actual.Status.Size)
}

func TestBackupSizeEmptyOnFailure(t *testing.T) {
	const namespace = "backup-size-fail-test"
	const storageName = "s3-us-west"

	ctx := context.Background()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	cluster, err := readDefaultCR("ps-cluster1", namespace)
	require.NoError(t, err)

	cr, err := readDefaultCRBackup("some-name", namespace)
	require.NoError(t, err)

	cr.Spec.ClusterName = cluster.Name
	cr.Spec.StorageName = storageName

	stor := cluster.Spec.Backup.Storages[storageName]

	s3Secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: stor.S3.CredentialsSecret, Namespace: namespace},
		Data: map[string][]byte{
			secret.CredentialsAWSAccessKey: []byte("access-key"),
			secret.CredentialsAWSSecretKey: []byte("secret-key"),
		},
	}
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster.InternalSecretName(), Namespace: namespace},
		Data: map[string][]byte{
			string(apiv1.UserOperator): []byte("operator-pass"),
		},
	}

	// Set backup status to Running
	cr.Status.State = apiv1.BackupRunning
	cr.Status.Storage = stor.DeepCopy()
	cr.Status.Destination = "s3://bucket/backup-name"

	// Create a failed job
	job, err := xtrabackup.Job(cluster.DeepCopy(), cr, "dest", "init-image", stor)
	require.NoError(t, err)
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type:   batchv1.JobFailed,
		Status: corev1.ConditionTrue,
	})

	cb := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cr, cluster.DeepCopy(), s3Secret, userSecret, job).
		WithStatusSubresource(cr, cluster.DeepCopy(), s3Secret, job)

	r := PerconaServerMySQLBackupReconciler{
		Client:        cb.Build(),
		Scheme:        scheme,
		ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
	}

	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
	require.NoError(t, err)

	actual := new(apiv1.PerconaServerMySQLBackup)
	err = r.Get(ctx, client.ObjectKeyFromObject(cr), actual)
	require.NoError(t, err)

	assert.Equal(t, apiv1.BackupFailed, actual.Status.State)
	assert.Empty(t, actual.Status.Size)
}
