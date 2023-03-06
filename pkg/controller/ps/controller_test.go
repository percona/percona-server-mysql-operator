/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ps

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	// metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"

	// "k8s.io/client-go/kubernetes/scheme"
	// "k8s.io/client-go/rest"
	// "sigs.k8s.io/controller-runtime/pkg/client"
	// "sigs.k8s.io/controller-runtime/pkg/envtest"
	// logf "sigs.k8s.io/controller-runtime/pkg/log"
	// "sigs.k8s.io/controller-runtime/pkg/log/zap"

	psv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	// "github.com/percona/percona-server-mysql-operator/pkg/version"
	//+kubebuilder:scaffold:imports
)

var _ = Describe("PerconaServerMongoDB controller", func() {

	Describe("given a CR", func() {
		// cr := &psv1alpha1.PerconaServerMySQL{
		// 	TypeMeta: metav1.TypeMeta{
		// 		APIVersion: "ps.percona.com/v1alpha1",
		// 		Kind:       "PerconaServerMySQL",
		// 	},
		// 	ObjectMeta: metav1.ObjectMeta{
		// 		Name:      "test-cr",
		// 		Namespace: "default",
		// 	},
		// 	Spec: psv1alpha1.PerconaServerMySQLSpec{
		// 		CRVersion: version.Version,
		// 		Backup: &psv1alpha1.BackupSpec{
		// 			Enabled: false,
		// 		},
		// 		Proxy: psv1alpha1.ProxySpec{
		// 			Router: &psv1alpha1.MySQLRouterSpec{},
		// 		},
		// 		Orchestrator: psv1alpha1.OrchestratorSpec{},
		// 	},
		// }

		cr, err := readCRNative()
		It("should read defautl cr.yaml", func() {
			Expect(err).Error().Should(BeNil())
		})
		cr.Namespace = "default"

		ctx := context.Background()

		It("Should create PerconaServerMongoDB", func() {
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		Describe("Sidecars", func() {
			Context("sidecar container specified in the CR", func() {

				// add sidecar to CR
				cr.MySQLSpec().Size = 4

				// By("Update CR with sidecars")
				It("CR should be updated", func() {
					Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
				})

				Specify("controller should add specified sidecar to mysql STS", func() {
					sts := &appsv1.StatefulSet{}

					Eventually(func() bool {
						err := k8sClient.Get(ctx, types.NamespacedName{Name: mysql.Name(cr), Namespace: cr.Namespace}, sts)
						return err == nil
					}, time.Second*15, time.Millisecond*250).Should(BeTrue())

					Expect(cr.MySQLSpec().Size).Should(Equal(*&sts.Spec.Replicas))
				})
			})
		})
	})
})

func readCRNative() (*psv1alpha1.PerconaServerMySQL, error) {
	var cr *psv1alpha1.PerconaServerMySQL

	sch := runtime.NewScheme()
	_ = scheme.AddToScheme(sch)
	_ = psv1alpha1.AddToScheme(sch)
	decode := serializer.NewCodecFactory(sch).UniversalDeserializer().Decode

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "cr.yaml"))
	if err != nil {
		return nil, err
	}

	obj, gKV, _ := decode(data, nil, nil)

	if gKV.Kind == "PerconaServerMySQL" {
		cr = obj.(*psv1alpha1.PerconaServerMySQL)
	}

	return cr, nil
}
