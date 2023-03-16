package ps

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	cm "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	cmscheme "github.com/cert-manager/cert-manager/pkg/client/clientset/versioned/scheme"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sversion "k8s.io/apimachinery/pkg/version"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

var _ = Describe("Finalizer delete-ssl", Ordered, func() {
	ctx := context.Background()

	const crName = "delete-ssl-finalizer"
	const ns = crName
	crNamespacedName := types.NamespacedName{Name: crName, Namespace: ns}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crName,
			Namespace: ns,
		},
	}

	BeforeAll(func() {
		By("Creating the Namespace to perform the tests")
		err := k8sClient.Create(ctx, namespace)
		Expect(err).To(Not(HaveOccurred()))

		_, err = envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{
			Paths: []string{filepath.Join("testdata", "cert-manager.yaml")},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		time.Sleep(60 * time.Second)
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	Context("delete-ssl finalizer not set", Ordered, func() {
		cr, err := readDefaultCR(crName, ns)
		It("should read and create defautl cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		// This way we simulate cert-manager creating secrets
		go func() {
			defer GinkgoRecover()

			// We need to wait a bit in order to ensure that the operator starts
			// creating issuers and certificates, because if secrets
			// are already there, the operator will see that they exist and will
			// not proceed with the algorithm.
			time.Sleep(10 * time.Second)
			secretCACert := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cr.Name + "-ca-cert",
					Namespace: cr.Namespace,
				},
				Type: corev1.SecretTypeOpaque,
			}

			secretSSL := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cr.Spec.SSLSecretName,
					Namespace: cr.Namespace,
				},
				Type: corev1.SecretTypeOpaque,
			}

			Expect(k8sClient.Create(ctx, secretCACert)).Should(Succeed())
			Expect(k8sClient.Create(ctx, secretSSL)).Should(Succeed())
		}()

		It("should reconcile once to create issuers and certificates", func() {
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		})

		When("PS cluster is deleted, without delete-ssl finalizer certs should not be deleted", func() {
			It("should delete PS cluster and reconcile changes", func() {
				Expect(k8sClient.Delete(ctx, cr, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &cr.UID}})).
					Should(Succeed())

				_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			})

			It("controller should not remove secrets", func() {
				secret := &corev1.Secret{}
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Namespace: cr.Namespace,
						Name:      cr.Spec.SSLSecretName,
					}, secret)

					return err == nil
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				Expect(secret.Name).Should(Equal(cr.Spec.SSLSecretName))

				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Namespace: cr.Namespace,
						Name:      cr.Name + "-ca-cert",
					}, secret)

					return err == nil
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				Expect(secret.Name).Should(Equal(cr.Name + "-ca-cert"))
			})

			It("controller should not remove CM issuers and certificates", func() {
				issuers := &cm.IssuerList{}
				Eventually(func() bool {
					opts := &client.ListOptions{Namespace: cr.Namespace}
					err := k8sClient.List(ctx, issuers, opts)

					return err == nil
				}, time.Second*30, time.Millisecond*250).Should(BeTrue())
				Expect(issuers.Items).Should(HaveLen(2))

				certs := &cm.CertificateList{}
				Eventually(func() bool {
					opts := &client.ListOptions{Namespace: cr.Namespace}
					err := k8sClient.List(ctx, certs, opts)

					return err == nil
				}, time.Second*30, time.Millisecond*250).Should(BeTrue())
				Expect(certs.Items).Should(HaveLen(2))
			})
		})
	})

	Context("delete-ssl finalizer set", Ordered, func() {
		cr, err := readDefaultCR(crName, ns)
		cr.Finalizers = append(cr.Finalizers, "delete-ssl")
		It("should read and create defautl cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		When("PS cluster is deleted with delete-ssl finalizer certs should be removed", func() {
			It("should delete PS cluster and reconcile changes", func() {
				Expect(k8sClient.Delete(ctx, cr, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &cr.UID}})).
					Should(Succeed())

				_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			})

			It("controller should remove secrets", func() {
				secret := &corev1.Secret{}
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Namespace: cr.Namespace,
						Name:      cr.Spec.SSLSecretName,
					}, secret)

					return k8serrors.IsNotFound(err)
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Namespace: cr.Namespace,
						Name:      cr.Name + "-ca-cert",
					}, secret)

					return k8serrors.IsNotFound(err)
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())
			})

			It("controller should delete issuers and certificates", func() {
				issuers := &cm.IssuerList{}
				Eventually(func() bool {

					opts := &client.ListOptions{Namespace: cr.Namespace}
					err := k8sClient.List(ctx, issuers, opts)

					return err == nil
				}, time.Second*30, time.Millisecond*250).Should(BeTrue())

				Expect(issuers.Items).Should(BeEmpty())

				certs := &cm.CertificateList{}
				Eventually(func() bool {

					opts := &client.ListOptions{Namespace: cr.Namespace}
					err := k8sClient.List(ctx, certs, opts)

					return err == nil
				}, time.Second*30, time.Millisecond*250).Should(BeTrue())

				Expect(certs.Items).Should(BeEmpty())
			})
		})
	})
})

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
