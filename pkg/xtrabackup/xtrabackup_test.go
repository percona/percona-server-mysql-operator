package xtrabackup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
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

		j := Job(cluster, cr, destination, initImage, cluster.Spec.Backup.Storages[storageName])

		assert.NotNil(t, j)
		assert.Equal(t, "xb-backup-minio", j.Name)
		assert.Equal(t, ns, j.Namespace)
		assert.Equal(t, map[string]string{
			"app.kubernetes.io/name":       "xtrabackup",
			"app.kubernetes.io/part-of":    "percona-server-backup",
			"app.kubernetes.io/version":    "v" + version.Version(),
			"app.kubernetes.io/instance":   "backup",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/component":  "backup",
		}, j.Labels)
		assert.Equal(t, map[string]string{"storage-annotation": "test"}, j.Annotations)
	})

	t.Run("image pull secrets", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cr := backup.DeepCopy()

		j := Job(cluster, cr, destination, initImage, cluster.Spec.Backup.Storages[storageName])
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

		j = Job(cluster, cr, destination, initImage, cluster.Spec.Backup.Storages[storageName])
		assert.Equal(t, imagePullSecrets, j.Spec.Template.Spec.ImagePullSecrets)
	})

	t.Run("runtime class name", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cr := backup.DeepCopy()

		j := Job(cluster, cr, destination, initImage, cluster.Spec.Backup.Storages[storageName])
		var e *string
		assert.Equal(t, e, j.Spec.Template.Spec.RuntimeClassName)

		const runtimeClassName = "runtimeClassName"
		cluster.Spec.Backup.Storages[storageName].RuntimeClassName = ptr.To(runtimeClassName)

		j = Job(cluster, cr, destination, initImage, cluster.Spec.Backup.Storages[storageName])
		assert.Equal(t, runtimeClassName, *j.Spec.Template.Spec.RuntimeClassName)
	})
}
