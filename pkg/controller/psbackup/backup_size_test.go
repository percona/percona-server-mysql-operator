package psbackup

import (
	"context"
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
	fakestorage "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage/fake"
)

func newMySQLPod(clusterName, namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + "-mysql-0",
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/component":  "database",
				"app.kubernetes.io/instance":   clusterName,
				"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
				"app.kubernetes.io/name":       "mysql",
				"app.kubernetes.io/part-of":    "percona-server",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

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

	mysqlPod := newMySQLPod(cluster.Name, namespace)
	sidecar := &fakeSidecarClient{backupSize: 78771} // ~76.92KB

	cb := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cr, cluster.DeepCopy(), s3Secret, userSecret, job, mysqlPod).
		WithStatusSubresource(cr, cluster.DeepCopy(), s3Secret, job)

	r := PerconaServerMySQLBackupReconciler{
		Client:           cb.Build(),
		Scheme:           scheme,
		ServerVersion:    &platform.ServerVersion{Platform: platform.PlatformKubernetes},
		NewStorageClient: fakestorage.NewFakeClient,
		NewSidecarClient: func(_ string) xtrabackup.SidecarClient {
			return sidecar
		},
	}

	// First reconcile: transitions BackupRunning -> BackupSucceeded
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
	require.NoError(t, err)

	actual := new(apiv1.PerconaServerMySQLBackup)
	err = r.Get(ctx, client.ObjectKeyFromObject(cr), actual)
	require.NoError(t, err)
	assert.Equal(t, apiv1.BackupSucceeded, actual.Status.State)

	// Second reconcile: fetches and sets backup size
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
	require.NoError(t, err)

	err = r.Get(ctx, client.ObjectKeyFromObject(cr), actual)
	require.NoError(t, err)

	assert.Equal(t, apiv1.BackupSucceeded, actual.Status.State)
	assert.Equal(t, "76.92KB", actual.Status.Size)
}

func TestBackupSizeZeroOnNoSize(t *testing.T) {
	const namespace = "backup-size-zero-test"
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

	cr.Status.State = apiv1.BackupRunning
	cr.Status.Storage = stor.DeepCopy()
	cr.Status.Destination = "s3://bucket/backup-name"

	job, err := xtrabackup.Job(cluster.DeepCopy(), cr, "dest", "init-image", stor)
	require.NoError(t, err)
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type:   batchv1.JobComplete,
		Status: corev1.ConditionTrue,
	})

	mysqlPod := newMySQLPod(cluster.Name, namespace)
	// BackupSize is 0, meaning xtrabackup didn't report a size
	sidecar := &fakeSidecarClient{backupSize: 0}

	cb := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cr, cluster.DeepCopy(), s3Secret, userSecret, job, mysqlPod).
		WithStatusSubresource(cr, cluster.DeepCopy(), s3Secret, job)

	r := PerconaServerMySQLBackupReconciler{
		Client:           cb.Build(),
		Scheme:           scheme,
		ServerVersion:    &platform.ServerVersion{Platform: platform.PlatformKubernetes},
		NewStorageClient: fakestorage.NewFakeClient,
		NewSidecarClient: func(_ string) xtrabackup.SidecarClient {
			return sidecar
		},
	}

	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
	require.NoError(t, err)

	actual := new(apiv1.PerconaServerMySQLBackup)
	err = r.Get(ctx, client.ObjectKeyFromObject(cr), actual)
	require.NoError(t, err)

	assert.Equal(t, apiv1.BackupSucceeded, actual.Status.State)
	assert.Empty(t, actual.Status.Size)
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
