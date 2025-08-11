package tls

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
	"sort"
	"time"

	cm "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

func DNSNames(cr *apiv1alpha1.PerconaServerMySQL) []string {
	hosts := []string{
		fmt.Sprintf("*.%s-mysql", cr.Name),
		fmt.Sprintf("*.%s-mysql.%s", cr.Name, cr.Namespace),
		fmt.Sprintf("*.%s-mysql.%s.svc", cr.Name, cr.Namespace),
		fmt.Sprintf("*.%s-orchestrator", cr.Name),
		fmt.Sprintf("*.%s-orchestrator.%s", cr.Name, cr.Namespace),
		fmt.Sprintf("*.%s-orchestrator.%s.svc", cr.Name, cr.Namespace),
		fmt.Sprintf("*.%s-router", cr.Name),
		fmt.Sprintf("*.%s-router.%s", cr.Name, cr.Namespace),
		fmt.Sprintf("*.%s-router.%s.svc", cr.Name, cr.Namespace),
	}
	if cr.Spec.TLS != nil {
		hosts = append(hosts, cr.Spec.TLS.SANs...)
	}
	return hosts
}

var validityNotAfter = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

// IssueCerts returns CA certificate, TLS certificate and TLS private key
func IssueCerts(hosts []string) (caCert, tlsCert, tlsKey []byte, err error) {
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

func DNSNamesFromCert(data []byte) ([]string, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("PEM data is not found")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("failed to parse certificate")
	}
	names := cert.DNSNames
	sort.Strings(names)
	return names, nil
}

func IsSecretCreatedByUser(ctx context.Context, c client.Client, cr *apiv1alpha1.PerconaServerMySQL, secret *corev1.Secret) (bool, error) {
	if metav1.IsControlledBy(secret, cr) {
		return false, nil
	}
	if secret.Labels[cm.PartOfCertManagerControllerLabelKey] == "true" {
		return isCertManagerSecretCreatedByUser(ctx, c, cr, secret)
	}
	return true, nil
}

func isCertManagerSecretCreatedByUser(ctx context.Context, c client.Client, cr *apiv1alpha1.PerconaServerMySQL, secret *corev1.Secret) (bool, error) {
	if metav1.IsControlledBy(secret, cr) {
		return false, nil
	}

	issuerName := secret.Annotations[cm.IssuerNameAnnotationKey]
	if secret.Annotations[cm.IssuerKindAnnotationKey] != cm.IssuerKind || issuerName == "" {
		return true, nil
	}
	issuer := new(cm.Issuer)
	if err := c.Get(ctx, types.NamespacedName{
		Name:      issuerName,
		Namespace: secret.Namespace,
	}, issuer); err != nil {
		if k8serrors.IsNotFound(err) {
			return true, nil
		}
		return true, errors.Wrap(err, "failed to get issuer")
	}
	if metav1.IsControlledBy(issuer, cr) {
		return false, nil
	}
	return true, nil
}
