package storage

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

func TestGetOptionsFromBackupConfig(t *testing.T) {
	caFile := t.TempDir() + "/ca.crt"
	require.NoError(t, os.WriteFile(caFile, []byte("test-ca"), 0o600))

	tests := []struct {
		name         string
		caCert       string
		wantCABundle []byte
		wantErr      string
	}{
		{
			name:         "load CA bundle",
			caCert:       caFile,
			wantCABundle: []byte("test-ca"),
		},
		{
			name: "without CA bundle",
		},
		{
			name:    "missing CA bundle",
			caCert:  t.TempDir() + "/missing.crt",
			wantErr: "read S3 CA bundle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := GetOptionsFromBackupConfig(&xtrabackup.BackupConfig{
				Type:   apiv1.BackupStorageS3,
				CACert: tt.caCert,
				S3:     xtrabackup.BackupConfigS3{Bucket: "bucket"},
			})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

			s3Options, ok := opts.(*S3Options)
			require.True(t, ok)
			assert.Equal(t, tt.wantCABundle, s3Options.CABundle)
		})
	}
}

func TestGetOptionsFromBackupStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	const namespace = "test"
	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: namespace},
		Data: map[string][]byte{
			secret.CredentialsAWSAccessKey: []byte("access-key"),
			secret.CredentialsAWSSecretKey: []byte("secret-key"),
		},
	}
	selector := &apiv1.CABundleSecretSelector{Name: "minio-ca", Key: apiv1.DefaultCABundleKey}

	tests := []struct {
		name         string
		selector     *apiv1.CABundleSecretSelector
		caSecret     *corev1.Secret
		wantCABundle []byte
		wantErr      string
	}{
		{
			name:     "load CA bundle",
			selector: selector,
			caSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "minio-ca", Namespace: namespace},
				Data:       map[string][]byte{apiv1.DefaultCABundleKey: []byte("test-ca")},
			},
			wantCABundle: []byte("test-ca"),
		},
		{
			name: "without CA bundle",
		},
		{
			name:     "missing CA bundle secret",
			selector: selector,
			wantErr:  "failed to get S3 CA bundle secret",
		},
		{
			name:     "missing CA bundle key",
			selector: selector,
			caSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "minio-ca", Namespace: namespace},
			},
			wantErr: "key ca.crt is not found in the minio-ca secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := []client.Object{credentials}
			if tt.caSecret != nil {
				objects = append(objects, tt.caSecret)
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			cluster := &apiv1.PerconaServerMySQL{ObjectMeta: metav1.ObjectMeta{Namespace: namespace}}
			status := apiv1.PerconaServerMySQLBackupStatus{Storage: &apiv1.BackupStorageSpec{
				S3: &apiv1.BackupStorageS3Spec{
					Bucket:            "bucket",
					CredentialsSecret: "credentials",
					CABundle:          tt.selector,
				},
			}}

			opts, err := GetOptionsFromBackupStatus(t.Context(), cl, cluster, "storage", status)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

			s3Options, ok := opts.(*S3Options)
			require.True(t, ok)
			assert.Equal(t, tt.wantCABundle, s3Options.CABundle)
		})
	}
}

func TestNewClientRejectsInvalidS3CABundle(t *testing.T) {
	tests := []struct {
		name     string
		caBundle []byte
	}{
		{
			name:     "plain text",
			caBundle: []byte("not a PEM certificate"),
		},
		{
			name:     "invalid PEM block",
			caBundle: []byte("-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient(t.Context(), &S3Options{
				Endpoint:   "https://minio.example.com",
				BucketName: "bucket",
				VerifyTLS:  true,
				CABundle:   tt.caBundle,
			})
			require.EqualError(t, err, "failed to parse S3 CA bundle")
		})
	}
}
