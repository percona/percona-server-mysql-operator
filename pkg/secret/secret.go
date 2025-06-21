package secret

import (
	"context"
	"crypto/rand"
	"math/big"
	mrand "math/rand"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/tls"
)

var validityNotAfter = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

func GenerateCertsSecret(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (*corev1.Secret, error) {
	ca, cert, key, err := tls.IssueCerts(tls.DNSNames(cr))
	if err != nil {
		return nil, errors.Wrap(err, "issue TLS certificates")
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Spec.SSLSecretName,
			Namespace: cr.Namespace,
			Labels:    cr.Labels("certificate", naming.ComponentTLS),
		},
		Data: map[string][]byte{
			"ca.crt":  ca,
			"tls.crt": cert,
			"tls.key": key,
		},
		Type: corev1.SecretTypeTLS,
	}
	return secret, nil
}

const (
	passwordMaxLen = 20
	passwordMinLen = 16
	passSymbols    = "ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"abcdefghijklmnopqrstuvwxyz" +
		"0123456789" +
		"!$%&()*+,-.<=>?@[]^_{}~#"
)

var SecretUsers = []apiv1alpha1.SystemUser{
	apiv1alpha1.UserHeartbeat,
	apiv1alpha1.UserMonitor,
	apiv1alpha1.UserOperator,
	apiv1alpha1.UserOrchestrator,
	apiv1alpha1.UserRoot,
	apiv1alpha1.UserXtraBackup,
	apiv1alpha1.UserReplication,
}

func FillPasswordsSecret(cr *apiv1alpha1.PerconaServerMySQL, secret *corev1.Secret) error {
	if len(secret.Data) == 0 {
		secret.Data = make(map[string][]byte, len(SecretUsers))
	}
	for _, user := range SecretUsers {
		if _, ok := secret.Data[string(user)]; ok {
			continue
		}
		pass, err := generatePass()
		if err != nil {
			return errors.Wrapf(err, "create %s user password", user)
		}
		secret.Data[string(user)] = pass
	}
	return nil
}

// generatePass generates a random password
func generatePass() ([]byte, error) {
	mrand.Seed(time.Now().UnixNano())
	ln := mrand.Intn(passwordMaxLen-passwordMinLen) + passwordMinLen
	b := make([]byte, ln)
	for i := 0; i != ln; i++ {
		randInt, err := rand.Int(rand.Reader, big.NewInt(int64(len(passSymbols))))
		if err != nil {
			return nil, errors.Wrap(err, "get rand int")
		}
		b[i] = passSymbols[randInt.Int64()]
	}

	return b, nil
}

const (
	CredentialsAzureStorageAccount = "AZURE_STORAGE_ACCOUNT_NAME"
	CredentialsAzureAccessKey      = "AZURE_STORAGE_ACCOUNT_KEY"
	CredentialsAWSAccessKey        = "AWS_ACCESS_KEY_ID"
	CredentialsAWSSecretKey        = "AWS_SECRET_ACCESS_KEY"
	CredentialsGCSAccessKey        = "ACCESS_KEY_ID"
	CredentialsGCSSecretKey        = "SECRET_ACCESS_KEY"
)
