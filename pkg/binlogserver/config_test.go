package binlogserver

import (
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
)

func newConfigTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestGetConfigurationEncryption(t *testing.T) {
	cr := newTestCR("cluster", "ns")
	spec := cr.Spec.Backup.PiTR.BinlogServer
	spec.Storage.S3.Bucket = "binlogs"
	spec.Storage.S3.Region = "us-east-1"
	spec.Storage.Encryption = &apiv1.BinlogServerStorageEncryptionSpec{
		KeyringSecret: &apiv1.BinlogServerKeyringSecretSelector{
			Name: "keyring-secret",
			Key:  "keyring.json",
		},
		Cipher: "AES-256-CTR",
	}

	cl := newConfigTestClient(t,
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "s3-creds-secret", Namespace: "ns"},
			Data: map[string][]byte{
				secret.CredentialsAWSAccessKey: []byte("access-key"),
				secret.CredentialsAWSSecretKey: []byte("secret-key"),
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: cr.InternalSecretName(), Namespace: "ns"},
			Data: map[string][]byte{
				string(apiv1.UserReplication): []byte("replication-pass"),
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "keyring-secret", Namespace: "ns"},
			Data: map[string][]byte{
				"keyring.json": []byte(`{"version":1,"keys":[{"id":"alpha","cipher":"AES-256-ECB","data_hex":"0000000000000000000000000000000000000000000000000000000000000000"}]}`),
			},
		},
	)

	cfg, err := GetConfiguration(t.Context(), cl, cr, spec)
	require.NoError(t, err)
	require.NotNil(t, cfg.Storage.Encryption)
	assert.Equal(t, &EncryptionConfig{
		Format:     "generic",
		KeyringURI: "file:///etc/binlog_server/keyring/keyring.json",
		KekID:      "alpha",
		Cipher:     "AES-256-CTR",
	}, cfg.Storage.Encryption)
}

func TestGetConfigurationEncryptionValidation(t *testing.T) {
	cr := newTestCR("cluster", "ns")
	spec := cr.Spec.Backup.PiTR.BinlogServer
	spec.Storage.Encryption = &apiv1.BinlogServerStorageEncryptionSpec{
		KeyringSecret: &apiv1.BinlogServerKeyringSecretSelector{
			Name: "keyring-secret",
			Key:  "keyring.json",
		},
		Cipher: "AES-256-CTR",
	}

	tests := map[string]struct {
		keyringData   map[string][]byte
		expectedError string
	}{
		"missing key": {
			keyringData:   map[string][]byte{"other.json": []byte(`{"version":1,"keys":[{"id":"alpha"}]}`)},
			expectedError: `key "keyring.json" not found`,
		},
		"unknown field": {
			keyringData:   map[string][]byte{"keyring.json": []byte(`{"version":1,"unknown":true,"keys":[{"id":"alpha"}]}`)},
			expectedError: "decode keyring",
		},
		"empty keyring": {
			keyringData:   map[string][]byte{"keyring.json": []byte(`{"version":1,"keys":[]}`)},
			expectedError: "keyring must contain at least one key",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cl := newConfigTestClient(t,
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "keyring-secret", Namespace: "ns"},
					Data:       tt.keyringData,
				},
			)

			cfg, err := getEncryptionConfig(t.Context(), cl, cr, spec)
			require.ErrorContains(t, err, tt.expectedError)
			assert.Nil(t, cfg)
		})
	}
}
