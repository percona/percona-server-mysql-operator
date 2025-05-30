package xtrabackup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

func TestJob(t *testing.T) {
	const ns = "job-ns"
	const storageName = "some-storage"

	cr := updateResource(readDefaultCluster(t, "cluster", ns), func(cr *apiv1alpha1.PerconaServerMySQL) {
		cr.Spec.Backup.Storages = map[string]*apiv1alpha1.BackupStorageSpec{
			storageName: {
				Type: apiv1alpha1.BackupStorageS3,
				S3: &apiv1alpha1.BackupStorageS3Spec{
					Bucket:            "bucket",
					Prefix:            "prefix",
					CredentialsSecret: "secret",
					Region:            "region",
					EndpointURL:       "endpoint",
					StorageClass:      "storage-class",
				},
			},
		}
	})
	tests := []struct {
		name string

		cluster     *apiv1alpha1.PerconaServerMySQL
		cr          *apiv1alpha1.PerconaServerMySQLBackup
		destination apiv1alpha1.BackupDestination
		initImage   string

		compareFile string
	}{
		{
			name:        "default cr with single storage",
			cluster:     cr.DeepCopy(),
			cr:          readDefaultBackup(t, "backup", ns),
			destination: "prefix/destination",
			initImage:   "init-image",
			compareFile: "backup-job/default-job.yaml",
		},
		{
			name: "default cr with image pull secrets",
			cluster: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQL) {
				cr.Spec.Backup.ImagePullSecrets = []corev1.LocalObjectReference{
					{
						Name: "secret-1",
					},
					{
						Name: "secret-2",
					},
				}
			}),
			cr:          readDefaultBackup(t, "backup", ns),
			destination: "prefix/destination",
			initImage:   "init-image",
			compareFile: "backup-job/image-pull-secrets-job.yaml",
		},
		{
			name: "default cr with runtime class name",
			cluster: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQL) {
				n := "runtime-class-name"
				cr.Spec.Backup.Storages[storageName].RuntimeClassName = &n
			}),
			cr:          readDefaultBackup(t, "backup", ns),
			destination: "prefix/destination",
			initImage:   "init-image",
			compareFile: "backup-job/runtime-class-name-job.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := Job(tt.cluster, tt.cr, tt.destination, tt.initImage, tt.cluster.Spec.Backup.Storages[storageName])
			compareObj(t, j, expectedObject[*batchv1.Job](t, tt.compareFile))
		})
	}
}

func expectedObject[T client.Object](t *testing.T, path string) T {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", path))
	if err != nil {
		t.Fatal(err)
	}

	obj := new(T)
	if err := yaml.Unmarshal(data, obj); err != nil {
		t.Fatal(err)
	}

	return *obj
}

func compareObj[T client.Object](t *testing.T, got, want T) {
	t.Helper()

	gotBytes, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("error marshaling got: %v", err)
	}
	wantBytes, err := yaml.Marshal(want)
	if err != nil {
		t.Fatalf("error marshaling want: %v", err)
	}
	if string(gotBytes) != string(wantBytes) {
		t.Fatal(cmp.Diff(string(wantBytes), string(gotBytes)))
	}
}
