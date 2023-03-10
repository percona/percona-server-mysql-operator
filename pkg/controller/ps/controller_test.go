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
	gs "github.com/onsi/gomega/gstruct"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"

	psv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	//+kubebuilder:scaffold:imports
)

var _ = Describe("Sidecars", Ordered, func() {
	cr, err := readDefaultCR("default")
	It("should read defautl cr.yaml", func() {
		Expect(err).NotTo(HaveOccurred())
	})

	It("should create PerconaServerMongoDB", func() {
		Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
	})

	ctx := context.Background()

	Context("Sidecar container specified in the CR", func() {
		sidecar := corev1.Container{
			Name:    "sidecar1",
			Image:   "busybox",
			Command: []string{"sleep", "30d"},
		}

		cr.MySQLSpec().Sidecars = []corev1.Container{sidecar}

		Specify("CR should be updated", func() {
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
		})

		Specify("controller should add specified sidecar to mysql STS", func() {
			sts := &appsv1.StatefulSet{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mysql.Name(cr), Namespace: cr.Namespace}, sts)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			Expect(sts.Spec.Template.Spec.Containers).Should(ContainElement(gs.MatchFields(gs.IgnoreExtras, gs.Fields{
				"Name":  Equal(sidecar.Name),
				"Image": Equal(sidecar.Image),
			})))
		})
	})

	Context("Sidecar container specified with a volume mounted", func() {
		Specify("should get latest CR", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "cluster1", Namespace: cr.Namespace}, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())
		})

		const volumeName = "empty-vol"
		const mounthPath = "/var/app/empty"

		sidecarVol := corev1.Container{
			Name:    "sidecar-vol",
			Image:   "busybox",
			Command: []string{"sleep", "30d"},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      volumeName,
					MountPath: mounthPath,
				},
			},
		}
		cr.MySQLSpec().Sidecars = append(cr.Spec.MySQL.Sidecars, sidecarVol)
		cr.MySQLSpec().SidecarVolumes = []corev1.Volume{
			{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						Medium: corev1.StorageMediumMemory,
					},
				},
			}}

		Specify("CR should be updated", func() {
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
		})

		Specify("controller should add specified sidecar and volume to mysql STS", func() {
			sts := &appsv1.StatefulSet{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mysql.Name(cr), Namespace: cr.Namespace}, sts)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			Expect(sts.Spec.Template.Spec.Containers).Should(
				ContainElement(gs.MatchFields(gs.IgnoreExtras, gs.Fields{
					"Name":  Equal(sidecarVol.Name),
					"Image": Equal(sidecarVol.Image),
					"VolumeMounts": ContainElement(gs.MatchFields(gs.IgnoreExtras, gs.Fields{
						"Name":      Equal(volumeName),
						"MountPath": Equal(mounthPath),
					})),
				})))
			Expect(sts.Spec.Template.Spec.Volumes).Should(ContainElement(gs.MatchFields(gs.IgnoreExtras, gs.Fields{
				"Name": Equal(volumeName),
				"VolumeSource": gs.MatchFields(gs.IgnoreExtras, gs.Fields{
					"EmptyDir": gs.PointTo(gs.MatchFields(gs.IgnoreExtras, gs.Fields{
						"Medium": Equal(corev1.StorageMediumMemory),
					})),
				}),
			})))
		})
	})

	Context("Sidecar container specified with a PVC mounted", func() {
		It("should get latest CR", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "cluster1", Namespace: cr.Namespace}, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())
		})

		const pvcName = "pvc-vol"
		const mountPath = "/var/app/pvc"

		sidecarPVC := corev1.Container{
			Name:    "sidecar-pvc",
			Image:   "busybox",
			Command: []string{"sleep", "30d"},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      pvcName,
					MountPath: "/var/app/pvc",
				},
			},
		}
		cr.MySQLSpec().Sidecars = append(cr.Spec.MySQL.Sidecars, sidecarPVC)
		cr.MySQLSpec().SidecarPVCs = []psv1alpha1.SidecarPVC{
			{
				Name: pvcName,
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.Quantity{
								Format: "1G",
							},
						},
					},
				},
			}}

		Specify("CR should be updated", func() {
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
		})

		Specify("controller should add specified sidecar and volume to mysql STS", func() {
			sts := &appsv1.StatefulSet{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mysql.Name(cr), Namespace: cr.Namespace}, sts)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			Expect(sts.Spec.Template.Spec.Containers).Should(
				ContainElement(gs.MatchFields(gs.IgnoreExtras, gs.Fields{
					"Name":  Equal(sidecarPVC.Name),
					"Image": Equal(sidecarPVC.Image),
					"VolumeMounts": ContainElement(gs.MatchFields(gs.IgnoreExtras, gs.Fields{
						"Name":      Equal(pvcName),
						"MountPath": Equal(mountPath),
					})),
				})))
			Expect(sts.Spec.VolumeClaimTemplates).Should(ContainElement(gs.MatchFields(gs.IgnoreExtras, gs.Fields{
				"ObjectMeta": gs.MatchFields(gs.IgnoreExtras, gs.Fields{
					"Name": Equal(pvcName),
				}),
			})))
		})
	})
})

func readDefaultCR(namespace string) (*psv1alpha1.PerconaServerMySQL, error) {
	sch := runtime.NewScheme()
	err := scheme.AddToScheme(sch)
	if err != nil {
		return nil, err
	}
	err = psv1alpha1.AddToScheme(sch)
	if err != nil {
		return nil, err
	}

	decode := serializer.NewCodecFactory(sch).UniversalDeserializer().Decode

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "cr.yaml"))
	if err != nil {
		return nil, err
	}

	obj, gKV, err := decode(data, nil, nil)
	if err != nil {
		return nil, err
	}

	var cr *psv1alpha1.PerconaServerMySQL

	if gKV.Kind == "PerconaServerMySQL" {
		cr = obj.(*psv1alpha1.PerconaServerMySQL)
	}

	cr.Namespace = namespace
	cr.Spec.InitImage = "perconalab/percona-server-mysql-operator:main"
	return cr, nil
}
