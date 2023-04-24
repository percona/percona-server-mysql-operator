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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gs "github.com/onsi/gomega/gstruct"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	psv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	//+kubebuilder:scaffold:imports
)

var _ = Describe("Sidecars", Ordered, func() {
	ctx := context.Background()

	const crName = "sidecars"
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
	})

	AfterAll(func() {
		// TODO(user): Attention if you improve this code by adding other context test you MUST
		// be aware of the current delete namespace limitations. More info: https://book.kubebuilder.io/reference/envtest.html#testing-considerations
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	cr, err := readDefaultCR(crName, ns)
	It("should read defautl cr.yaml", func() {
		Expect(err).NotTo(HaveOccurred())
	})

	It("should create PerconaServerMySQL", func() {
		Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
	})

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
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())

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
				err := k8sClient.Get(ctx, crNamespacedName, cr)
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
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())

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
				err := k8sClient.Get(ctx, crNamespacedName, cr)
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
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())

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

var _ = Describe("Unsafe configurations", Ordered, func() {
	ctx := context.Background()

	const crName = "unsafe-configs"
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
	})

	AfterAll(func() {
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	cr, err := readDefaultCR(crName, ns)
	It("should read and create defautl cr.yaml", func() {
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
	})

	Context("Unsafe configurations are disabled", func() {
		Specify("controller should set minimum safe number of replicas to MySQL statefulset", func() {
			cr.Spec.AllowUnsafeConfig = false
			cr.MySQLSpec().ClusterType = v1alpha1.ClusterTypeGR
			cr.MySQLSpec().Size = 1
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

			_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mysql.Name(cr), Namespace: cr.Namespace}, sts)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			Expect(*sts.Spec.Replicas).Should(Equal(int32(psv1alpha1.MinSafeGRSize)))
		})

		Specify("controller should set maximum safe number of replicas to MySQL statefulset", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, crNamespacedName, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			cr.Spec.AllowUnsafeConfig = false
			cr.MySQLSpec().ClusterType = v1alpha1.ClusterTypeGR
			cr.MySQLSpec().Size = 11
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

			_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mysql.Name(cr), Namespace: cr.Namespace}, sts)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			Expect(*sts.Spec.Replicas).Should(Equal(int32(psv1alpha1.MaxSafeGRSize)))
		})

		Specify("controller should set even number of replicas to MySQL statefulset", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, crNamespacedName, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			cr.Spec.AllowUnsafeConfig = false
			cr.MySQLSpec().ClusterType = v1alpha1.ClusterTypeGR
			cr.MySQLSpec().Size = 4
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

			_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			sts := &appsv1.StatefulSet{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mysql.Name(cr), Namespace: cr.Namespace}, sts)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			Expect(*sts.Spec.Replicas).Should(Equal(int32(5)))
		})
	})

	Context("Unsafe configurations are enabled", func() {
		Specify("controller should set unsafe number of replicas to MySQL statefulset", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, crNamespacedName, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			cr.Spec.AllowUnsafeConfig = true
			cr.MySQLSpec().ClusterType = v1alpha1.ClusterTypeGR
			cr.MySQLSpec().Size = 1
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

			_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})

			sts := &appsv1.StatefulSet{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: mysql.Name(cr), Namespace: cr.Namespace}, sts)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			Expect(*sts.Spec.Replicas).Should(Equal(int32(1)))
		})
	})
})

var _ = Describe("Reconcile HAProxy", Ordered, func() {
	ctx := context.Background()

	crName := "reconcile-haproxy"
	ns := crName
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
	})

	AfterAll(func() {
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	Context("Cleanup outdated HAProxy service", Ordered, func() {
		cr, err := readDefaultCR(crName, ns)
		cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeAsync
		cr.Spec.Proxy.HAProxy.Enabled = true
		cr.Spec.AllowUnsafeConfig = false
		It("should read and create defautl cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		svcName := crName + "-haproxy"

		When("HAPRoxy is disabled with setting enabled option to false", Ordered, func() {
			It("should remove outdated HAProxy service", func() {
				_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
				Expect(err).NotTo(HaveOccurred())

				Eventually(func() bool {
					err := k8sClient.Get(ctx, crNamespacedName, cr)
					return err == nil
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				cr.Spec.Proxy.HAProxy.Enabled = false
				cr.Spec.AllowUnsafeConfig = true
				Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

				_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
				Expect(err).NotTo(HaveOccurred())

				svc := &corev1.Service{}
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Namespace: cr.Namespace,
						Name:      svcName,
					}, svc)

					return k8serrors.IsNotFound(err)
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())
			})
		})

		When("HAPRoxy is disabled by setting the size to zero", Ordered, func() {
			It("should remove outdated HAProxy service", func() {
				Eventually(func() bool {
					err := k8sClient.Get(ctx, crNamespacedName, cr)
					return err == nil
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				cr.Spec.Proxy.HAProxy.Enabled = true
				Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

				By("Reconcile once so the operator can create HAProxy service")
				_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
				Expect(err).NotTo(HaveOccurred())

				Eventually(func() bool {
					err := k8sClient.Get(ctx, crNamespacedName, cr)
					return err == nil
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				cr.Spec.Proxy.HAProxy.Size = 0
				Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

				_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
				Expect(err).NotTo(HaveOccurred())

				svc := &corev1.Service{}
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Namespace: cr.Namespace,
						Name:      svcName,
					}, svc)

					return k8serrors.IsNotFound(err)
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())
			})
		})
	})
})
