package tls

import (
	"context"
	"testing"

	cm "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestDNSNames(t *testing.T) {
	tests := map[string]struct {
		cr       *apiv1.PerconaServerMySQL
		expected map[string]struct{}
	}{
		"no extra SANs": {
			cr: &apiv1.PerconaServerMySQL{
				Name:      "cluster1",
				Namespace: "default",
				Spec: apiv1.PerconaServerMySQLSpec{
					CRVersion: version.Version(),
				},
			},
			expected: map[string]struct{}{
				"*.cluster1-mysql":                    {},
				"*.cluster1-mysql.default":            {},
				"*.cluster1-mysql.default.svc":        {},
				"cluster1-mysql-primary":              {},
				"cluster1-mysql-primary.default":      {},
				"cluster1-mysql-primary.default.svc":  {},
				"*.cluster1-orchestrator":             {},
				"*.cluster1-orchestrator.default":     {},
				"*.cluster1-orchestrator.default.svc": {},
				"*.cluster1-router":                   {},
				"*.cluster1-router.default":           {},
				"*.cluster1-router.default.svc":       {},
			},
		},
		"with extra SANs": {
			cr: &apiv1.PerconaServerMySQL{
				Name:      "cluster1",
				Namespace: "default",
				Spec: apiv1.PerconaServerMySQLSpec{
					CRVersion: version.Version(),
					TLS: &apiv1.TLSSpec{
						SANs: []string{"extra.example.com"},
					},
				},
			},
			expected: map[string]struct{}{
				"*.cluster1-mysql":                    {},
				"*.cluster1-mysql.default":            {},
				"*.cluster1-mysql.default.svc":        {},
				"cluster1-mysql-primary":              {},
				"cluster1-mysql-primary.default":      {},
				"cluster1-mysql-primary.default.svc":  {},
				"*.cluster1-orchestrator":             {},
				"*.cluster1-orchestrator.default":     {},
				"*.cluster1-orchestrator.default.svc": {},
				"*.cluster1-router":                   {},
				"*.cluster1-router.default":           {},
				"*.cluster1-router.default.svc":       {},
				"extra.example.com":                   {},
			},
		},
		"with extra SANs version 1.0.0": {
			cr: &apiv1.PerconaServerMySQL{
				Name:      "cluster1",
				Namespace: "default",
				Spec: apiv1.PerconaServerMySQLSpec{
					CRVersion: "1.0.0",
					TLS: &apiv1.TLSSpec{
						SANs: []string{"extra.example.com"},
					},
				},
			},
			expected: map[string]struct{}{
				"*.cluster1-mysql":                    {},
				"*.cluster1-mysql.default":            {},
				"*.cluster1-mysql.default.svc":        {},
				"*.cluster1-orchestrator":             {},
				"*.cluster1-orchestrator.default":     {},
				"*.cluster1-orchestrator.default.svc": {},
				"*.cluster1-router":                   {},
				"*.cluster1-router.default":           {},
				"*.cluster1-router.default.svc":       {},
				"extra.example.com":                   {},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			actual := make(map[string]struct{})
			for _, n := range DNSNames(tt.cr) {
				actual[n] = struct{}{}
			}
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestIsSecretCreatedByUser(t *testing.T) {
	const (
		crName = "cluster1"
		ns     = "default"
	)

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, cm.AddToScheme(scheme))

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns},
	}

	// operatorCertName is the name of the Certificate managed by the operator.
	operatorCertName := crName + "-ssl"

	certManagerSecret := func(annotations map[string]string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-ssl",
				Namespace: ns,
				Labels: map[string]string{
					cm.PartOfCertManagerControllerLabelKey: "true",
				},
				Annotations: annotations,
			},
		}
	}

	tests := map[string]struct {
		cr       *apiv1.PerconaServerMySQL
		secret   *corev1.Secret
		existing []runtime.Object
		expected bool
	}{
		// Regression: after switching the operator-managed certificate to a
		// ClusterIssuer, cert-manager sets issuer-kind=ClusterIssuer on the
		// secret. The operator must still recognize it as its own secret so it
		// keeps reconciling (e.g. when switching back to an Issuer).
		"operator secret issued by ClusterIssuer is not user-created": {
			cr: cr,
			secret: certManagerSecret(map[string]string{
				cm.CertificateNameKey:      operatorCertName,
				cm.IssuerKindAnnotationKey: cm.ClusterIssuerKind,
				cm.IssuerNameAnnotationKey: "some-cluster-issuer",
			}),
			expected: false,
		},
		"operator secret issued by Issuer owned by cr is not user-created": {
			cr: cr,
			secret: certManagerSecret(map[string]string{
				cm.IssuerKindAnnotationKey: cm.IssuerKind,
				cm.IssuerNameAnnotationKey: crName + "-ps-issuer",
			}),
			existing: []runtime.Object{
				&cm.Issuer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      crName + "-ps-issuer",
						Namespace: ns,
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: apiv1.GroupVersion.String(),
								Kind:       "PerconaServerMySQL",
								Name:       crName,
								UID:        cr.UID,
								Controller: new(true),
							},
						},
					},
				},
			},
			expected: false,
		},
		"secret issued by ClusterIssuer configured in cr is not user-created": {
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns},
				Spec: apiv1.PerconaServerMySQLSpec{
					TLS: &apiv1.TLSSpec{
						IssuerConf: &cmmeta.IssuerReference{
							Name: "some-cluster-issuer",
							Kind: cm.ClusterIssuerKind,
						},
					},
				},
			},
			secret: certManagerSecret(map[string]string{
				cm.IssuerKindAnnotationKey: cm.ClusterIssuerKind,
				cm.IssuerNameAnnotationKey: "some-cluster-issuer",
			}),
			expected: false,
		},
		"secret issued by unrelated ClusterIssuer is user-created": {
			cr: cr,
			secret: certManagerSecret(map[string]string{
				cm.IssuerKindAnnotationKey: cm.ClusterIssuerKind,
				cm.IssuerNameAnnotationKey: "external-cluster-issuer",
			}),
			expected: true,
		},
		"secret without cert-manager label is user-created": {
			cr: cr,
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ssl", Namespace: ns},
			},
			expected: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(tt.existing...).Build()

			got, err := IsSecretCreatedByUser(context.Background(), cl, tt.cr, tt.secret)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}
