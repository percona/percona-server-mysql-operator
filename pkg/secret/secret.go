package secret

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	mrand "math/rand"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

var validityNotAfter = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

func GenerateCertsSecret(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (*corev1.Secret, error) {
	// TODO: DNS suffix
	hosts := []string{
		fmt.Sprintf("*.%s-mysql", cr.Name),
		fmt.Sprintf("*.%s-mysql.%s", cr.Name, cr.Namespace),
		fmt.Sprintf("*.%s-mysql.%s.svc.cluster.local", cr.Name, cr.Namespace),
		fmt.Sprintf("*.%s-orchestrator", cr.Name),
		fmt.Sprintf("*.%s-orchestrator.%s", cr.Name, cr.Namespace),
		fmt.Sprintf("*.%s-orchestrator.%s.svc.cluster.local", cr.Name, cr.Namespace),
		fmt.Sprintf("*.%s-router", cr.Name),
		fmt.Sprintf("*.%s-router.%s", cr.Name, cr.Namespace),
		fmt.Sprintf("*.%s-router.%s.svc.cluster.local", cr.Name, cr.Namespace),
	}

	ca, cert, key, err := issueCerts(hosts)
	if err != nil {
		return nil, errors.Wrap(err, "issue TLS certificates")
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Spec.SSLSecretName,
			Namespace: cr.Namespace,
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

// issueCerts returns CA certificate, TLS certificate and TLS private key
func issueCerts(hosts []string) (caCert, tlsCert, tlsKey []byte, err error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "generate rsa key")
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "generate serial number for root")
	}

	caTemplate := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Root CA"},
		},
		NotBefore: time.Now(),
		NotAfter:  validityNotAfter,
		KeyUsage:  x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader,
		&caTemplate,
		&caTemplate,
		&privateKey.PublicKey,
		privateKey)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "generate CA certificate")
	}

	certOut := &bytes.Buffer{}
	err = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "encode CA certificate")
	}
	caCert = certOut.Bytes()

	serialNumber, err = rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "generate serial number for client")
	}

	tlsTemplate := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"PS"},
		},
		Issuer: pkix.Name{
			Organization: []string{"Root CA"},
		},
		NotBefore: time.Now(),
		NotAfter:  validityNotAfter,
		DNSNames:  hosts,
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}

	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "generate client key")
	}

	tlsDerBytes, err := x509.CreateCertificate(rand.Reader,
		&tlsTemplate,
		&caTemplate,
		&clientKey.PublicKey,
		privateKey)
	if err != nil {
		return nil, nil, nil, err
	}

	tlsCertOut := &bytes.Buffer{}
	err = pem.Encode(tlsCertOut, &pem.Block{Type: "CERTIFICATE", Bytes: tlsDerBytes})
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "encode TLS  certificate")
	}
	tlsCert = tlsCertOut.Bytes()

	keyOut := &bytes.Buffer{}
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(clientKey),
	}
	err = pem.Encode(keyOut, block)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "encode RSA private key")
	}
	tlsKey = keyOut.Bytes()

	return
}

const (
	passwordMaxLen = 20
	passwordMinLen = 16
	passSymbols    = "ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"abcdefghijklmnopqrstuvwxyz" +
		"0123456789"
)

var secretUsers = [...]apiv1alpha1.SystemUser{
	apiv1alpha1.UserHeartbeat,
	apiv1alpha1.UserMonitor,
	apiv1alpha1.UserOperator,
	apiv1alpha1.UserOrchestrator,
	apiv1alpha1.UserReplication,
	apiv1alpha1.UserRoot,
	apiv1alpha1.UserXtraBackup,
}

func GeneratePasswordsSecret(name, namespace string) (*corev1.Secret, error) {
	data := make(map[string][]byte)
	for _, user := range secretUsers {
		pass, err := generatePass()
		if err != nil {
			return nil, errors.Wrapf(err, "create %s user password", user)
		}
		data[string(user)] = pass
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: data,
		Type: corev1.SecretTypeOpaque,
	}, nil
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
