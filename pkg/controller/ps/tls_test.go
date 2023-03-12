package ps

import (
	"context"
	"log"
	"path/filepath"
	"time"

	cm "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	// cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var _ = FDescribe("Cert-Manager delete-ssl finalizer", Ordered, func() {

	It("should create cert-manager CRDs", func() {
		crds, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{
			Paths: []string{filepath.Join("testdata", "cert-manager.yaml")},
		})
		Expect(err).NotTo(HaveOccurred())

		log.Println(crds)
	})

	cr, err := readDefaultCR("default")
	It("should read defautl cr.yaml", func() {
		Expect(err).NotTo(HaveOccurred())
	})

	ctx := context.Background()

	It("should create PerconaServerMongoDB", func() {
		Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
	})

	Context("delete-ssl finalizer not set", Ordered, func() {
		data := make(map[string][]byte)
		data["tls.cert"] = []byte("inel")
		data["tls.key"] = []byte("inel")
		data["ca.cert"] = []byte("inel")

		secretSSL := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cr.Name + "-ca-cert",
				Namespace: cr.Namespace,
			},
			Data: data,
			Type: corev1.SecretTypeOpaque,
		}

		// This is created by cert-manager
		It("should create ca-cert secrets", func() {
			Expect(k8sClient.Create(ctx, secretSSL)).Should(Succeed())

			// Let the cluster crete
			time.Sleep(30 * time.Second)
		})

		It("should get latest CR", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())
		})

		It("should delete PS cluster", func() {
			Expect(k8sClient.Delete(ctx, cr, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &cr.UID}})).
				Should(Succeed())
		})

		It("should get ssl secrets", func() {
			secret := &corev1.Secret{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Namespace: cr.Namespace,
					Name:      secretSSL.Name,
				}, secret)

				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())
		})
		It("should get CM issuers", func() {
			issuers := &cm.IssuerList{}
			Eventually(func() bool {

				opts := &client.ListOptions{Namespace: cr.Namespace}
				err := k8sClient.List(ctx, issuers, opts)

				return err == nil
			}, time.Second*30, time.Millisecond*250).Should(BeTrue())

			log.Println(issuers)
		})
	})
})
