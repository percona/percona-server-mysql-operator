package k8s

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestS3CertVolumesExposeOnlySelectedKeys(t *testing.T) {
	selectors := []apiv1.CABundleSecretSelector{
		{Name: "cert-manager-tls", Key: "ca.crt"},
		{Name: "cert-manager-tls", Key: "other-ca.crt"},
		{Name: "default-key", Key: apiv1.DefaultCABundleKey},
	}

	volumes := S3CertVolumes(selectors)
	require.Len(t, volumes, 1)
	require.NotNil(t, volumes[0].Projected)
	require.Len(t, volumes[0].Projected.Sources, 3)

	for _, source := range volumes[0].Projected.Sources {
		require.Len(t, source.Secret.Items, 1)
	}
	for i, selector := range selectors {
		t.Run(selector.Name+"/"+selector.Key, func(t *testing.T) {
			item := volumes[0].Projected.Sources[i].Secret.Items[0]
			assert.Equal(t, selector.Key, item.Key)
			assert.Equal(t, s3CertFileName(selector), item.Path)
			assert.NotEqual(t, "tls.crt", item.Key)
			assert.NotEqual(t, "tls.key", item.Key)
		})
	}
}

func TestS3CAPath(t *testing.T) {
	selector := apiv1.CABundleSecretSelector{Name: "private-ca", Key: "root.crt"}

	assert.Equal(t, S3CAPath(selector), S3CAPath(apiv1.CABundleSecretSelector{Name: "private-ca", Key: "root.crt"}))

	assert.NotEqual(t, S3CAPath(selector), S3CAPath(apiv1.CABundleSecretSelector{Name: "other-ca", Key: "root.crt"}))
}

func TestPrepareJobWithS3CA(t *testing.T) {
	selector := apiv1.CABundleSecretSelector{Name: "private-ca", Key: "root.crt"}
	cluster := &apiv1.PerconaServerMySQL{Spec: apiv1.PerconaServerMySQLSpec{CRVersion: "1.3.0"}}
	storage := &apiv1.BackupStorageS3Spec{CABundle: &selector}
	job := &batchv1.Job{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "backup"}}},
	}}}

	PrepareJobWithS3CA(job, cluster, storage)

	require.Len(t, job.Spec.Template.Spec.Volumes, 1)
	container := job.Spec.Template.Spec.Containers[0]
	assert.Contains(t, container.VolumeMounts, corev1.VolumeMount{
		Name: naming.S3CertsInputVolumeName, MountPath: naming.S3CertsInputMountPath, ReadOnly: true,
	})
	assert.Contains(t, container.Env, corev1.EnvVar{
		Name: naming.EnvSSLCertFile, Value: S3CAPath(selector),
	})
}

func TestPrepareJobWithS3CANoOp(t *testing.T) {
	cluster := &apiv1.PerconaServerMySQL{Spec: apiv1.PerconaServerMySQLSpec{CRVersion: "1.3.0"}}

	tests := []struct {
		name    string
		storage *apiv1.BackupStorageS3Spec
	}{
		{name: "without S3 storage"},
		{name: "without CA bundle", storage: &apiv1.BackupStorageS3Spec{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &batchv1.Job{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "backup"}}},
			}}}

			PrepareJobWithS3CA(job, cluster, tt.storage)

			assert.Empty(t, job.Spec.Template.Spec.Volumes)
			assert.Empty(t, job.Spec.Template.Spec.Containers[0].VolumeMounts)
			assert.Empty(t, job.Spec.Template.Spec.Containers[0].Env)
		})
	}
}

func TestPrepareS3Certs(t *testing.T) {
	tempDir := t.TempDir()
	inputDir := filepath.Join(tempDir, "input")
	require.NoError(t, os.Mkdir(inputDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(inputDir, "ca-1.crt"), []byte("second"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(inputDir, "ca-0.crt"), []byte("first"), 0o600))

	output := filepath.Join(tempDir, "bundle.crt")
	systemBundle := filepath.Join(tempDir, "system-bundle.crt")
	require.NoError(t, os.WriteFile(systemBundle, []byte("system"), 0o600))
	scriptContents, err := os.ReadFile(filepath.Join("..", "..", "build", "prepare-s3-certs.sh"))
	require.NoError(t, err)
	scriptContents = []byte(strings.ReplaceAll(string(scriptContents), naming.S3CertsInputMountPath, inputDir))
	scriptContents = []byte(strings.ReplaceAll(string(scriptContents), naming.S3CABundlePath, output))
	scriptContents = []byte(strings.ReplaceAll(string(scriptContents), naming.SystemCABundlePath, systemBundle))
	script := filepath.Join(tempDir, "prepare-s3-certs.sh")
	require.NoError(t, os.WriteFile(script, scriptContents, 0o700))
	cmd := exec.Command("bash", script)
	cmd.Env = os.Environ()
	require.NoError(t, cmd.Run())

	bundle, err := os.ReadFile(output)
	require.NoError(t, err)
	assert.Equal(t, "system\nfirst\nsecond\n", string(bundle))
}
