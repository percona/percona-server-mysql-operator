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

func TestBackupSize(t *testing.T) {
	const storageName = "s3-us-west"

	tests := []struct {
		name                   string
		namespace              string
		backupSize             int64
		uncompressedBackupSize int64
		compressed             bool
		jobCondition           batchv1.JobConditionType
		needsMySQLPod          bool
		reconcileCount         int
		expectedState          apiv1.BackupState
		expectedSize           string
		expectedUncompressed   string
	}{
		{
			name:                 "size is set on success",
			namespace:            "backup-size-test",
			backupSize:           78771, // ~76.92KB
			jobCondition:         batchv1.JobComplete,
			needsMySQLPod:        true,
			reconcileCount:       2, // 1st: Running->Succeeded, 2nd: fetches size
			expectedState:        apiv1.BackupSucceeded,
			expectedSize:         "77KiB",
			expectedUncompressed: "77KiB",
		},
		{
			name:                   "compressed backup shows uncompressed size",
			namespace:              "backup-size-compressed-test",
			backupSize:             50000,
			uncompressedBackupSize: 200000,
			compressed:             true,
			jobCondition:           batchv1.JobComplete,
			needsMySQLPod:          true,
			reconcileCount:         2,
			expectedState:          apiv1.BackupSucceeded,
			expectedSize:           "49KiB",
			expectedUncompressed:   "195KiB",
		},
		{
			name:                 "size is empty when xtrabackup reports zero",
			namespace:            "backup-size-zero-test",
			backupSize:           0,
			jobCondition:         batchv1.JobComplete,
			needsMySQLPod:        true,
			reconcileCount:       2,
			expectedState:        apiv1.BackupSucceeded,
			expectedSize:         "",
			expectedUncompressed: "",
		},
		{
			name:                 "size is empty on failure",
			namespace:            "backup-size-fail-test",
			backupSize:           0,
			jobCondition:         batchv1.JobFailed,
			needsMySQLPod:        false,
			reconcileCount:       1,
			expectedState:        apiv1.BackupFailed,
			expectedSize:         "",
			expectedUncompressed: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			scheme := runtime.NewScheme()
			require.NoError(t, clientgoscheme.AddToScheme(scheme))
			require.NoError(t, apiv1.AddToScheme(scheme))

			cluster, err := readDefaultCR("ps-cluster1", tt.namespace)
			require.NoError(t, err)

			cr, err := readDefaultCRBackup("some-name", tt.namespace)
			require.NoError(t, err)

			cr.Spec.ClusterName = cluster.Name
			cr.Spec.StorageName = storageName

			stor := cluster.Spec.Backup.Storages[storageName]

			s3Secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: stor.S3.CredentialsSecret, Namespace: tt.namespace},
				Data: map[string][]byte{
					secret.CredentialsAWSAccessKey: []byte("access-key"),
					secret.CredentialsAWSSecretKey: []byte("secret-key"),
				},
			}
			userSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: cluster.InternalSecretName(), Namespace: tt.namespace},
				Data: map[string][]byte{
					string(apiv1.UserOperator): []byte("operator-pass"),
				},
			}

			cr.Status.State = apiv1.BackupRunning
			cr.Status.Storage = stor.DeepCopy()
			cr.Status.Destination = "s3://bucket/backup-name"
			cr.Status.Compressed = tt.compressed

			job, err := xtrabackup.Job(cluster.DeepCopy(), cr, "dest", "init-image", stor)
			require.NoError(t, err)
			job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
				Type:   tt.jobCondition,
				Status: corev1.ConditionTrue,
			})

			objects := []client.Object{cr, cluster.DeepCopy(), s3Secret, userSecret, job}

			r := PerconaServerMySQLBackupReconciler{
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{Platform: platform.PlatformKubernetes},
			}

			if tt.needsMySQLPod {
				mysqlPod := newMySQLPod(cluster.Name, tt.namespace)
				objects = append(objects, mysqlPod)

				sidecar := &fakeSidecarClient{backupSize: tt.backupSize, uncompressedBackupSize: tt.uncompressedBackupSize}
				r.NewStorageClient = fakestorage.NewFakeClient
				r.NewSidecarClient = func(_ string) xtrabackup.SidecarClient {
					return sidecar
				}
			}

			cb := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(objects...).
				WithStatusSubresource(cr, cluster.DeepCopy(), s3Secret, job)
			r.Client = cb.Build()

			for i := 0; i < tt.reconcileCount; i++ {
				_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
				require.NoError(t, err)
			}

			actual := new(apiv1.PerconaServerMySQLBackup)
			err = r.Get(ctx, client.ObjectKeyFromObject(cr), actual)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedState, actual.Status.State)
			assert.Equal(t, tt.expectedSize, actual.Status.Size)
			assert.Equal(t, tt.expectedUncompressed, actual.Status.UncompressedSize)
		})
	}
}
