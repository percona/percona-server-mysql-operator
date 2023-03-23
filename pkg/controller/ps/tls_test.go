package ps

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"time"

	cm "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

var _ = Describe("TLS secrets without cert-manager", Ordered, func() {
	ctx := context.Background()
	cr, err := readDefaultCR("cluster1", "tls-1")
	It("should read defautl cr.yaml", func() {
		Expect(err).NotTo(HaveOccurred())
	})
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: cr.Namespace,
		},
	}

	BeforeAll(func() {
		By("Creating the Namespace to perform the tests")
		err := k8sClient.Create(ctx, namespace)
		Expect(err).To(Not(HaveOccurred()))
	})

	AfterAll(func() {
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	It("should create PerconaServerMySQL", func() {
		Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
	})

	Context("without custom SANs", Ordered, func() {
		It("should reconcile", func() {
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Namespace: cr.Namespace,
					Name:      cr.Name,
				}}
			_, err := reconciler().Reconcile(ctx, req)
			Expect(err).Should(Succeed())
		})
		Specify("should not have custom SAN", func() {
			secret := new(corev1.Secret)

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: cr.Spec.SSLSecretName, Namespace: cr.Namespace}, secret)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			bl, _ := pem.Decode(secret.Data["tls.crt"])

			tlsCert, err := x509.ParseCertificate(bl.Bytes)
			Expect(err).NotTo(HaveOccurred())

			dnsNames := []string{
				"*.cluster1-mysql",
				"*.cluster1-mysql.tls-1",
				"*.cluster1-mysql.tls-1.svc",
				"*.cluster1-orchestrator",
				"*.cluster1-orchestrator.tls-1",
				"*.cluster1-orchestrator.tls-1.svc",
				"*.cluster1-router",
				"*.cluster1-router.tls-1",
				"*.cluster1-router.tls-1.svc",
			}

			Expect(tlsCert.DNSNames).Should(BeEquivalentTo(dnsNames))
		})
	})

	Context("with custom SANs", func() {
		Specify("CR should be updated", func() {
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, cr)).Should(Succeed())
			cr.Spec.TLS = &apiv1alpha1.TLSSpec{
				SANs: []string{"mysql-1.example.com"},
			}
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
		})
		It("should reconcile", func() {
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Namespace: cr.Namespace,
					Name:      cr.Name,
				}}
			_, err := reconciler().Reconcile(ctx, req)
			Expect(err).Should(Succeed())
		})
		Specify("should have custom SAN", func() {
			secret := new(corev1.Secret)

			time.Sleep(time.Second * 5)
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: cr.Spec.SSLSecretName, Namespace: cr.Namespace}, secret)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			bl, _ := pem.Decode(secret.Data["tls.crt"])

			tlsCert, err := x509.ParseCertificate(bl.Bytes)
			Expect(err).NotTo(HaveOccurred())

			dnsNames := []string{
				"*.cluster1-mysql",
				"*.cluster1-mysql.tls-1",
				"*.cluster1-mysql.tls-1.svc",
				"*.cluster1-orchestrator",
				"*.cluster1-orchestrator.tls-1",
				"*.cluster1-orchestrator.tls-1.svc",
				"*.cluster1-router",
				"*.cluster1-router.tls-1",
				"*.cluster1-router.tls-1.svc",
				"mysql-1.example.com",
			}

			Expect(tlsCert.DNSNames).Should(BeEquivalentTo(dnsNames))
		})
	})

	Context("with specified TLS issuerConf", func() {
		Specify("CR should be updated", func() {
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, cr)).Should(Succeed())

			cr.Spec.TLS = &apiv1alpha1.TLSSpec{
				SANs: []string{"mysql-1.example.com"},
				IssuerConf: &cmmeta.ObjectReference{
					Name: "some-issuer",
				},
			}
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
		})
		It("should fail on ensure TLS secret", func() {
			Expect(reconciler().ensureTLSSecret(ctx, cr)).ShouldNot(BeNil())
		})
	})
})

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
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	Context("delete-ssl finalizer specified", Ordered, func() {
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
