package ps

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"reflect"
	"testing"

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	cmscheme "github.com/cert-manager/cert-manager/pkg/client/clientset/versioned/scheme"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sversion "k8s.io/apimachinery/pkg/version"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

func TestCertManager(t *testing.T) {
	q, err := resource.ParseQuantity("1Gi")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	tests := []struct {
		name              string
		cr                *apiv1alpha1.PerconaServerMySQL
		enableCertManager bool
		dnsNames          []string
		shouldErr         bool
	}{
		// TODO: Add cert-manager test cases.
		{
			name: "Test tls secret without cert-manager",
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-cluster",
					Namespace: "some-namespace",
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					Backup: &apiv1alpha1.BackupSpec{
						Image: "backup-image",
					},
					MySQL: apiv1alpha1.MySQLSpec{
						PodSpec: apiv1alpha1.PodSpec{
							VolumeSpec: &apiv1alpha1.VolumeSpec{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
									Resources: corev1.ResourceRequirements{
										Requests: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceStorage: q,
										},
									},
								},
							},
						},
					},
				},
			},
			dnsNames: []string{
				"*.some-cluster-mysql",
				"*.some-cluster-mysql.some-namespace",
				"*.some-cluster-mysql.some-namespace.svc.cluster.local",
				"*.some-cluster-orchestrator",
				"*.some-cluster-orchestrator.some-namespace",
				"*.some-cluster-orchestrator.some-namespace.svc.cluster.local",
				"*.some-cluster-router",
				"*.some-cluster-router.some-namespace",
				"*.some-cluster-router.some-namespace.svc.cluster.local",
			},
		},
		{
			name: "Test tls secret with custom SANs without cert-manager",
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-cluster",
					Namespace: "some-namespace",
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					Backup: &apiv1alpha1.BackupSpec{
						Image: "backup-image",
					},
					MySQL: apiv1alpha1.MySQLSpec{
						PodSpec: apiv1alpha1.PodSpec{
							VolumeSpec: &apiv1alpha1.VolumeSpec{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
									Resources: corev1.ResourceRequirements{
										Requests: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceStorage: q,
										},
									},
								},
							},
						},
					},
					TLS: &apiv1alpha1.TLSSpec{
						SANs: []string{
							"mysql-1.example.com",
						},
					},
				},
			},
			dnsNames: []string{
				"*.some-cluster-mysql",
				"*.some-cluster-mysql.some-namespace",
				"*.some-cluster-mysql.some-namespace.svc.cluster.local",
				"*.some-cluster-orchestrator",
				"*.some-cluster-orchestrator.some-namespace",
				"*.some-cluster-orchestrator.some-namespace.svc.cluster.local",
				"*.some-cluster-router",
				"*.some-cluster-router.some-namespace",
				"*.some-cluster-router.some-namespace.svc.cluster.local",
				"mysql-1.example.com",
			},
		},
		{
			name: "Disabled cert-manager with specified TLS issuerConf",
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-cluster",
					Namespace: "some-namespace",
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					TLS: &apiv1alpha1.TLSSpec{
						IssuerConf: &cmmeta.ObjectReference{
							Name: "some-issuer",
						},
					},
					Backup: &apiv1alpha1.BackupSpec{
						Image: "backup-image",
					},
					MySQL: apiv1alpha1.MySQLSpec{
						PodSpec: apiv1alpha1.PodSpec{
							VolumeSpec: &apiv1alpha1.VolumeSpec{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
									Resources: corev1.ResourceRequirements{
										Requests: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceStorage: q,
										},
									},
								},
							},
						},
					},
				},
			},
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := clientgoscheme.AddToScheme(scheme); err != nil {
				t.Fatal(err, "failed to add client-go scheme")
			}
			if err := apiv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err, "failed to add apis scheme")
			}
			if tt.enableCertManager {
				if err := cmscheme.AddToScheme(scheme); err != nil {
					t.Fatal(err, "failed to add cert-manager scheme")
				}
			}
			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr)
			r := PerconaServerMySQLReconciler{
				Client: cb.Build(),
				Scheme: scheme,
				ServerVersion: &platform.ServerVersion{
					Platform: platform.PlatformKubernetes,
					Info: k8sversion.Info{
						GitVersion: "kube-version",
					},
				},
			}
			cr, err := k8s.GetCRWithDefaults(ctx, r.Client, types.NamespacedName{Name: tt.cr.Name, Namespace: tt.cr.Namespace}, r.ServerVersion)
			if err != nil {
				t.Fatal(err, "failed to get cr")
			}
			if err := r.ensureTLSSecret(ctx, cr); err != nil {
				if tt.shouldErr {
					return
				}
				t.Fatal(err, "failed to ensure tls secret")
			}
			secret := new(corev1.Secret)
			if err := r.Get(ctx, types.NamespacedName{Name: cr.Spec.SSLSecretName, Namespace: cr.Namespace}, secret); err != nil {
				t.Fatal(err, "failed to get secret")
			}

			bl, _ := pem.Decode(secret.Data["tls.crt"])

			tlsCert, err := x509.ParseCertificate(bl.Bytes)
			if err != nil {
				t.Fatal(err, "failed to parse cert")
			}

			if !reflect.DeepEqual(tlsCert.DNSNames, tt.dnsNames) {
				t.Fatalf("got %v, want %v", tlsCert.DNSNames, tt.dnsNames)
			}
		})
	}
}
