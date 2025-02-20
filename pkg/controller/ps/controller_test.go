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
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gs "github.com/onsi/gomega/gstruct"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	psv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
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
					Resources: corev1.VolumeResourceRequirements{
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
		Specify("controller shouldn't allow setting less than minimum safe size", func() {
			cr.Spec.Unsafe.MySQLSize = false
			cr.MySQLSpec().ClusterType = psv1alpha1.ClusterTypeGR
			cr.MySQLSpec().Size = 1
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

			_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).To(HaveOccurred())

			Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
			Expect(cr.Status.State).Should(Equal(psv1alpha1.StateError))
		})

		Specify("controller shouldn't allow setting more than maximum safe size", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, crNamespacedName, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			cr.Spec.Unsafe.MySQLSize = false
			cr.MySQLSpec().ClusterType = psv1alpha1.ClusterTypeGR
			cr.MySQLSpec().Size = 11
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

			_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).To(HaveOccurred())

			Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
			Expect(cr.Status.State).Should(Equal(psv1alpha1.StateError))
		})

		Specify("controller should't allow setting even number of nodes for MySQL", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, crNamespacedName, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			cr.Spec.Unsafe.MySQLSize = false
			cr.MySQLSpec().ClusterType = psv1alpha1.ClusterTypeGR
			cr.MySQLSpec().Size = 4
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

			_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).To(HaveOccurred())

			Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
			Expect(cr.Status.State).Should(Equal(psv1alpha1.StateError))
		})
	})

	Context("Unsafe configurations are enabled", func() {
		Specify("controller should set unsafe number of replicas to MySQL statefulset", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, crNamespacedName, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			cr.Spec.Unsafe.MySQLSize = true
			cr.MySQLSpec().ClusterType = psv1alpha1.ClusterTypeGR
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
		cr.Spec.Orchestrator.Enabled = true
		cr.Spec.UpdateStrategy = appsv1.RollingUpdateStatefulSetStrategyType
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
				cr.Spec.Unsafe.Proxy = true
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

var _ = Describe("Reconcile Binlog Server", Ordered, func() {
	ctx := context.Background()

	crName := "binlog-server"
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

	Context("Deploy Binlog Server", Ordered, func() {
		cr, err := readDefaultCR(crName, ns)

		cr.Spec.Backup.PiTR.Enabled = true
		cr.Spec.Backup.PiTR.BinlogServer = psv1alpha1.BinlogServerSpec{
			ConnectTimeout: 20,
			WriteTimeout:   20,
			ReadTimeout:    20,
			ServerID:       42,
			IdleTime:       60,
			Storage: psv1alpha1.BinlogServerStorageSpec{
				S3: &psv1alpha1.BackupStorageS3Spec{
					Bucket:            "s3-test-bucket",
					Region:            "us-west-1",
					EndpointURL:       "s3.amazonaws.com",
					CredentialsSecret: "s3-test-credentials",
				},
			},
			PodSpec: psv1alpha1.PodSpec{
				Size: 1,
				ContainerSpec: psv1alpha1.ContainerSpec{
					Image: "binlog-server-image",
				},
			},
		}

		It("should create s3 credentials secret", func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "s3-test-credentials",
					Namespace: cr.Namespace,
				},
			}

			err := k8sClient.Create(ctx, secret)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should read and create default cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())

			_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create secret for Binlog Server configuration", func() {
			_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cr.Name + "-binlog-server-config",
					Namespace: cr.Namespace,
				},
			}

			err := k8sClient.Get(ctx, client.ObjectKeyFromObject(secret), secret)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create statefulset for Binlog Server", func() {
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cr.Name + "-binlog-server",
					Namespace: cr.Namespace,
				},
			}

			err := k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), sts)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

var _ = Describe("Finalizer delete-mysql-pvc", Ordered, func() {
	ctx := context.Background()

	const crName = "del-mysql-pvc-fnlz"
	const ns = "del-mysql-pvc-fnlz"
	crNamespacedName := types.NamespacedName{Name: crName, Namespace: ns}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ns,
			Namespace: ns,
		},
	}

	BeforeAll(func() {
		By("Creating the Namespace to perform the tests")
		err := k8sClient.Create(ctx, namespace)
		Expect(err).To(Not(HaveOccurred()))

		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	Context("delete-mysql-pvc finalizer specified", Ordered, func() {

		cr, err := readDefaultCR(crName, ns)

		It("should read default cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
		})
		cr.Finalizers = append(cr.Finalizers, naming.FinalizerDeleteMySQLPvc)
		cr.Spec.SecretsName = "cluster1-secrets"

		sfsWithOwner := appsv1.StatefulSet{}
		// stsApp := statefulset.NewNode(cr)
		exposer := mysql.Exposer(*cr)

		It("Should create PerconaXtraDBCluster", func() {
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		It("should reconcile once to create user secret", func() {
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should create mysql sts", func() {

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      cr.Name + "-mysql",
				Namespace: cr.Namespace,
			}, &sfsWithOwner)).Should(Succeed())
		})

		It("Should create secrets", func() {
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: cr.Namespace,
				Name:      cr.Spec.SecretsName,
			}, secret)).Should(Succeed())
		})

		It("should create mysql PVC", func() {
			for _, claim := range sfsWithOwner.Spec.VolumeClaimTemplates {
				for i := 0; i < int(*sfsWithOwner.Spec.Replicas); i++ {
					pvc := claim.DeepCopy()
					pvc.Labels = exposer.Labels()
					pvc.Name = strings.Join([]string{pvc.Name, sfsWithOwner.Name, strconv.Itoa(i)}, "-")
					pvc.Namespace = ns
					Expect(k8sClient.Create(ctx, pvc)).Should(Succeed())
				}
			}
		})

		It("controller should have mysql pvc", func() {
			pvcList := corev1.PersistentVolumeClaimList{}
			Eventually(func() bool {
				err := k8sClient.List(ctx,
					&pvcList,
					&client.ListOptions{
						Namespace: cr.Namespace,
						LabelSelector: labels.SelectorFromSet(map[string]string{
							"app.kubernetes.io/component": "mysql",
						}),
					})
				return err == nil
			}, time.Second*25, time.Millisecond*250).Should(BeTrue())
			Expect(len(pvcList.Items)).Should(Equal(3))
		})

		When("mysql cluster is deleted with delete-mysql-pvc finalizer sts, pvc, and secrets should be removed", func() {
			It("should delete mysql cluster and reconcile changes", func() {
				Expect(k8sClient.Delete(ctx, cr)).Should(Succeed())

				_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			})

			It("controller should remove pvc for mysql", func() {
				pvcList := corev1.PersistentVolumeClaimList{}
				Eventually(func() bool {
					err := k8sClient.List(ctx, &pvcList, &client.ListOptions{
						Namespace: cr.Namespace,
						LabelSelector: labels.SelectorFromSet(map[string]string{
							"app.kubernetes.io/component": "mysql",
						}),
					})
					return err == nil
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				for _, pvc := range pvcList.Items {
					By(fmt.Sprintf("checking pvc/%s", pvc.Name))
					Expect(pvc.DeletionTimestamp).ShouldNot(BeNil())
				}
			})

			It("controller should delete secrets", func() {
				secret := &corev1.Secret{}
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Namespace: cr.Namespace,
						Name:      cr.Spec.SecretsName,
					}, secret)

					return k8serrors.IsNotFound(err)
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{
						Namespace: cr.Namespace,
						Name:      "internal-" + cr.Name,
					}, secret)

					return k8serrors.IsNotFound(err)
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			})
		})
	})
})
