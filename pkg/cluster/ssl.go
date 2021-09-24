package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

var validityNotAfter = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

func (r *MySQLReconciler) reconcileSSL(log logr.Logger, cr *v2.PerconaServerForMySQL) error {
	log.Info("Creating SSL certificates")

	if err := r.createSSLCerts(cr); err != nil {
		return errors.Wrapf(err, "create SSL certificates")
	}

	return nil
}

func (r *MySQLReconciler) createSSLCerts(cr *v2.PerconaServerForMySQL) error {
	sslSecret := &corev1.Secret{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: cr.Spec.SSLSecretName, Namespace: cr.Namespace}, sslSecret)
	if err == nil {
		return nil
	} else if !k8serrors.IsNotFound(err) {
		return errors.Wrap(err, "get SSL secret")
	}

	if err := r.createSSLManually(cr); err != nil {
		return errors.Wrap(err, "create SSL manually")
	}

	return nil
}

func (r *MySQLReconciler) createSSLManually(cr *v2.PerconaServerForMySQL) error {
	// TODO: DNS suffix
	hosts := []string{
		"*." + cr.Name + "-mysql",
		"*." + cr.Name + "-mysql" + "." + cr.Namespace,
		"*." + cr.Name + "-mysql" + "." + cr.Namespace + ".svc.cluster.local",
		"*." + cr.Name + "-orchestrator",
		"*." + cr.Name + "-orchestrator" + "." + cr.Namespace,
		"*." + cr.Name + "-orchestrator" + "." + cr.Namespace + ".svc.cluster.local",
	}

	ca, cert, key, err := Issue(hosts)
	if err != nil {
		return errors.Wrap(err, "issue TLS certificates")
	}

	data := make(map[string][]byte)
	data["ca.crt"] = ca
	data["tls.crt"] = cert
	data["tls.key"] = key

	secretObj := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Spec.SSLSecretName,
			Namespace: cr.Namespace,
		},
		Data: data,
		Type: corev1.SecretTypeTLS,
	}

	if err := k8s.SetControllerReference(cr, &secretObj, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to %s/%s", secretObj.Kind, secretObj.Name)
	}

	if err := r.Client.Create(context.TODO(), &secretObj); err != nil {
		return errors.Wrap(err, "create TLS secret")
	}

	return nil
}

// Issue returns CA certificate, TLS certificate and TLS private key
func Issue(hosts []string) (caCert []byte, tlsCert []byte, tlsKey []byte, err error) {
	rsaBits := 2048
	priv, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "generate rsa key")
	}
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "generate serial number for root")
	}
	subject := pkix.Name{
		Organization: []string{"Root CA"},
	}
	issuer := pkix.Name{
		Organization: []string{"Root CA"},
	}
	caTemplate := x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               subject,
		NotBefore:             time.Now(),
		NotAfter:              validityNotAfter,
		KeyUsage:              x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "generate CA certificate")
	}
	certOut := &bytes.Buffer{}
	err = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "encode CA certificate")
	}
	cert := certOut.Bytes()

	serialNumber, err = rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "generate serial number for client")
	}
	subject = pkix.Name{
		Organization: []string{"PXC"},
	}
	tlsTemplate := x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               subject,
		Issuer:                issuer,
		NotBefore:             time.Now(),
		NotAfter:              validityNotAfter,
		DNSNames:              hosts,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "generate client key")
	}
	tlsDerBytes, err := x509.CreateCertificate(rand.Reader, &tlsTemplate, &caTemplate, &clientKey.PublicKey, priv)
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
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(clientKey)}
	err = pem.Encode(keyOut, block)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "encode RSA private key")
	}
	privKey := keyOut.Bytes()

	return cert, tlsCert, privKey, nil
}
