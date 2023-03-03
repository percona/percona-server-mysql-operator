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
	// "os"
	// "path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	// "k8s.io/client-go/kubernetes/scheme"
	// "k8s.io/client-go/rest"
	// "sigs.k8s.io/controller-runtime/pkg/client"
	// "sigs.k8s.io/controller-runtime/pkg/envtest"
	// logf "sigs.k8s.io/controller-runtime/pkg/log"
	// "sigs.k8s.io/controller-runtime/pkg/log/zap"

	psv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
	//+kubebuilder:scaffold:imports
)



var _ = Describe("PerconaServerMongoDB controller", func() {
	It("Should create PerconaServerMongoDB", func() {
		ctx := context.Background()

		cr := &psv1alpha1.PerconaServerMySQL{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "ps.percona.com/v1alpha1",
				Kind:       "PerconaServerMySQL",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cr",
				Namespace: "default",
			},
			Spec: psv1alpha1.PerconaServerMySQLSpec{
				CRVersion: version.Version,
			},
		}

		Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
	})
})
