package ps

import (
	"log"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

	

})
