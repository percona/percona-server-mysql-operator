package ps

import (
	"context"
	"fmt"
	"time"

	cm "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
)

func (r *PerconaServerMySQLReconciler) ensureTLSSecret(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	err := r.ensureSSLByCertManager(ctx, cr)
	if err != nil {
		if cr.Spec.TLS != nil && cr.Spec.TLS.IssuerConf != nil {
			log.Error(err, fmt.Sprintf("Failed to ensure certificate by cert-manager. Check `.spec.tls.issuerConf` in PerconaServerMySQL %s/%s", cr.Namespace, cr.Name))
			return errors.Wrap(err, "create ssl with cert manager")
		}
		secret, err := secret.GenerateCertsSecret(ctx, cr)
		if err != nil {
			return errors.Wrap(err, "create SSL manually")
		}

		if err := k8s.EnsureObjectWithHash(ctx, r.Client, cr, secret, r.Scheme); err != nil {
			return errors.Wrap(err, "create secret")
		}
	}

	return nil
}
func (r *PerconaServerMySQLReconciler) checkTLSIssuer(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	if cr.Spec.TLS == nil || cr.Spec.TLS.IssuerConf == nil {
		return nil
	}
	isr := &cm.Issuer{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: cr.Namespace,
		Name:      cr.Spec.TLS.IssuerConf.Name,
	}, isr)
	if err != nil {
		return err
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) ensureSSLByCertManager(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	issuerName := cr.Name + "-pso-issuer"
	caIssuerName := cr.Name + "-pso-ca-issuer"
	issuerKind := "Issuer"
	issuerGroup := ""
	if cr.Spec.TLS != nil && cr.Spec.TLS.IssuerConf != nil {
		issuerKind = cr.Spec.TLS.IssuerConf.Kind
		issuerName = cr.Spec.TLS.IssuerConf.Name
		issuerGroup = cr.Spec.TLS.IssuerConf.Group

		if err := r.checkTLSIssuer(ctx, cr); err != nil {
			return err
		}
	} else {
		issuerConf := cm.IssuerConfig{
			SelfSigned: &cm.SelfSignedIssuer{},
		}
		if cr.Spec.TLS != nil && cr.Spec.TLS.IssuerConf != nil {
			issuerConf = cm.IssuerConfig{
				CA: &cm.CAIssuer{SecretName: cr.Spec.TLS.IssuerConf.Name},
			}
		}
		if err := r.ensureIssuer(ctx, cr, caIssuerName, issuerConf); err != nil {
			return err
		}
		certName := cr.Name + "-ca-cert"
		secretName := cr.Name + "-ca-cert"

		caCert := &cm.Certificate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      certName,
				Namespace: cr.Namespace,
			},
			Spec: cm.CertificateSpec{
				SecretName: secretName,
				CommonName: cr.Name + "-ca",
				IsCA:       true,
				IssuerRef: cmmeta.ObjectReference{
					Name:  caIssuerName,
					Kind:  issuerKind,
					Group: issuerGroup,
				},
				Duration:    &metav1.Duration{Duration: time.Hour * 24 * 365},
				RenewBefore: &metav1.Duration{Duration: 730 * time.Hour},
			},
		}
		if err := k8s.EnsureObjectWithHash(ctx, r.Client, nil, caCert, r.Scheme); err != nil {
			return errors.Wrap(err, "ensure CA certificate")
		}

		if err := r.waitForCert(ctx, cr.Namespace, certName, secretName); err != nil {
			return err
		}

		issuerConf = cm.IssuerConfig{
			CA: &cm.CAIssuer{SecretName: caCert.Spec.SecretName},
		}

		if err := r.ensureIssuer(ctx, cr, issuerName, issuerConf); err != nil {
			return err
		}
	}
	certName := cr.Name + "-ssl"

	kubeCert := &cm.Certificate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      certName,
			Namespace: cr.Namespace,
		},
		Spec: cm.CertificateSpec{
			SecretName: cr.Spec.SSLSecretName,
			DNSNames:   secret.DNSNames(cr),
			IsCA:       false,
			IssuerRef: cmmeta.ObjectReference{
				Name:  issuerName,
				Kind:  issuerKind,
				Group: issuerGroup,
			},
		},
	}

	if err := k8s.EnsureObjectWithHash(ctx, r.Client, nil, kubeCert, r.Scheme); err != nil {
		return errors.Wrap(err, "ensure certificate")
	}

	return r.waitForCert(ctx, cr.Namespace, certName, cr.Spec.SSLSecretName)
}

func (r *PerconaServerMySQLReconciler) ensureIssuer(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, issuerName string, IssuerConf cm.IssuerConfig,
) error {
	isr := &cm.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      issuerName,
			Namespace: cr.Namespace,
		},
		Spec: cm.IssuerSpec{
			IssuerConfig: IssuerConf,
		},
	}
	err := k8s.EnsureObjectWithHash(ctx, r.Client, nil, isr, r.Scheme)
	if err != nil {
		return errors.Wrap(err, "create issuer")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) waitForCert(ctx context.Context, namespace, certName, secretName string) error {
	ticker := time.NewTicker(3 * time.Second)
	timeoutTimer := time.NewTimer(30 * time.Second)
	defer timeoutTimer.Stop()
	defer ticker.Stop()
	secretFound := false
	for {
		select {
		case <-timeoutTimer.C:
			if !secretFound {
				return errors.Errorf("timeout: can't get tls certificate from certmanager: %s", secretName)
			}
			return errors.Errorf("timeout: tls certificate from certmanager is not ready: %s", secretName)
		case <-ticker.C:
			err := r.Get(ctx, types.NamespacedName{
				Name:      secretName,
				Namespace: namespace,
			}, new(corev1.Secret))
			if err != nil {
				if k8serrors.IsNotFound(err) {
					continue
				}
				return errors.Wrap(err, "failed to get secret")
			}
			secretFound = true

			cert := new(cm.Certificate)
			err = r.Get(ctx, types.NamespacedName{
				Name:      certName,
				Namespace: namespace,
			}, cert)
			if err != nil {
				return errors.Wrap(err, "failed to get certificate")
			}
			for _, cond := range cert.Status.Conditions {
				if cond.Type == cm.CertificateConditionReady && cond.Status == cmmeta.ConditionTrue {
					return nil
				}
			}
		}
	}
}
