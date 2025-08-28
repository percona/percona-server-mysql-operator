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
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	psv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/haproxy"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
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
			},
		}

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
			},
		}

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

var _ = Describe("PodDisruptionBudget", Ordered, func() {
	ctx := context.Background()

	crName := "pdb"
	ns := crName
	crNamespacedName := types.NamespacedName{Name: crName, Namespace: ns}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crName,
			Namespace: ns,
		},
	}

	var r *PerconaServerMySQLReconciler

	BeforeAll(func() {
		By("Creating the Namespace to perform the tests")
		err := k8sClient.Create(ctx, namespace)
		Expect(err).To(Not(HaveOccurred()))
	})

	AfterAll(func() {
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	Context("Check default cluster", Ordered, func() {
		cr, err := readDefaultCR(crName, ns)
		cr.Spec.CRVersion = "0.12.0"
		It("should prepare reconciler", func() {
			r = reconciler()
			Expect(err).To(Succeed())
			cliCmd, err := getFakeClient(cr, innodbcluster.ClusterStatusOK, []innodbcluster.MemberState{
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
			}, false, true)
			Expect(err).To(Succeed())
			r.ClientCmd = cliCmd
			const operatorPass = "test"
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cr.InternalSecretName(),
					Namespace: cr.Namespace,
				},
				Data: map[string][]byte{
					string(psv1alpha1.UserOperator): []byte(operatorPass),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())
		})

		It("should create cr.yaml", func() {
			cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeAsync
			cr.Spec.Unsafe.Orchestrator = true
			cr.Spec.Unsafe.Proxy = true
			cr.Spec.MySQL.PodDisruptionBudget = &psv1alpha1.PodDisruptionBudgetSpec{
				MaxUnavailable: &intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: 20,
				},
			}
			cr.Spec.Proxy.Router.Enabled = false
			cr.Spec.Proxy.HAProxy.Enabled = true
			cr.Spec.Proxy.HAProxy.PodDisruptionBudget = &psv1alpha1.PodDisruptionBudgetSpec{
				MinAvailable: &intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: 12,
				},
			}
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.Orchestrator.PodDisruptionBudget = &psv1alpha1.PodDisruptionBudgetSpec{
				MaxUnavailable: &intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: 11,
				},
			}
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		It("should create MySQL pods", func() {
			for _, pod := range makeFakeReadyPods(cr, 3, "mysql") {
				status := pod.(*corev1.Pod).Status
				Expect(k8sClient.Create(ctx, pod)).Should(Succeed())
				p := new(corev1.Pod)
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), p)).Should(Succeed())
				p.Status = status
				Expect(k8sClient.Status().Update(ctx, p)).Should(Succeed())
			}
		})

		When("HAProxy is enabled", Ordered, func() {
			It("should reconcile", func() {
				_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			})
			It("should check PodDisruptionBudget for MySQL", func() {
				pdb := &policyv1.PodDisruptionBudget{
					ObjectMeta: metav1.ObjectMeta{
						Name:      cr.Name + "-mysql",
						Namespace: cr.Namespace,
					},
				}

				Eventually(func() bool {
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pdb), pdb)
					return err == nil
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				Expect(pdb.Labels).To(Equal(mysql.MatchLabels(cr)))
				Expect(pdb.Spec.Selector.MatchLabels).To(Equal(mysql.MatchLabels(cr)))

				Expect(pdb.Spec.MaxUnavailable.IntVal).To(Equal(int32(20)))
			})

			It("should check PodDisruptionBudget for HAProxy", func() {
				pdb := &policyv1.PodDisruptionBudget{
					ObjectMeta: metav1.ObjectMeta{
						Name:      cr.Name + "-haproxy",
						Namespace: cr.Namespace,
					},
				}

				Eventually(func() bool {
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pdb), pdb)
					return err == nil
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				Expect(pdb.Labels).To(Equal(haproxy.MatchLabels(cr)))
				Expect(pdb.Spec.Selector.MatchLabels).To(Equal(haproxy.MatchLabels(cr)))

				Expect(pdb.Spec.MinAvailable.IntVal).To(Equal(int32(12)))
			})

			It("should check PodDisruptionBudget for Orchestrator", func() {
				pdb := &policyv1.PodDisruptionBudget{
					ObjectMeta: metav1.ObjectMeta{
						Name:      cr.Name + "-orchestrator",
						Namespace: cr.Namespace,
					},
				}

				Eventually(func() bool {
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pdb), pdb)
					return err == nil
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				Expect(pdb.Labels).To(Equal(orchestrator.MatchLabels(cr)))
				Expect(pdb.Spec.Selector.MatchLabels).To(Equal(orchestrator.MatchLabels(cr)))

				Expect(pdb.Spec.MaxUnavailable.IntVal).To(Equal(int32(11)))
			})
		})
	})
})

var _ = Describe("Reconcile HAProxy when async cluster type", Ordered, func() {
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
		cr.Spec.Orchestrator.Enabled = true
		cr.Spec.UpdateStrategy = appsv1.RollingUpdateStatefulSetStrategyType
		It("should read and create default cr.yaml", func() {
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

var _ = Describe("CR validations", Ordered, func() {
	ctx := context.Background()

	ns := "validate"

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
	})

	AfterAll(func() {
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
	})

	Context("cr creation based on mysql cluster configuration", Ordered, func() {
		When("the cr is configured using default values and async cluster type", Ordered, func() {
			cr, err := readDefaultCR("cr-validation-1", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeAsync
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.UpdateStrategy = appsv1.RollingUpdateStatefulSetStrategyType
			It("should read and create default cr.yaml", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("cluster type is async and the orchestrator is disabled but unsafe flag enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-2", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeAsync
			cr.Spec.UpdateStrategy = appsv1.RollingUpdateStatefulSetStrategyType
			cr.Spec.Orchestrator.Enabled = false
			cr.Spec.Unsafe.Orchestrator = true
			It("should read and create default cr.yaml", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("cluster type is async and the orchestrator is disabled with unsafe flag disabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-3", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeAsync
			cr.Spec.UpdateStrategy = appsv1.RollingUpdateStatefulSetStrategyType
			cr.Spec.Orchestrator.Enabled = false
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("'orchestrator.enabled' must be true unless 'unsafeFlags.orchestrator' is enabled"))
			})
		})

		When("cluster type is async and HAProxy is disabled but unsafe flag enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-4", ns)
			cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeAsync
			cr.Spec.UpdateStrategy = appsv1.RollingUpdateStatefulSetStrategyType
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.Proxy.HAProxy.Enabled = false
			cr.Spec.Unsafe.Proxy = true
			It("should read and create default cr.yaml", func() {
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("cluster type is async and HAProxy is disabled with unsafe flag disabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-5", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeAsync
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.UpdateStrategy = appsv1.RollingUpdateStatefulSetStrategyType
			cr.Spec.Proxy.HAProxy.Enabled = false
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("'proxy.haproxy.enabled' must be true unless 'unsafeFlags.proxy' is enabled"))
			})
		})

		When("cluster type is async and router is enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-6", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeAsync
			cr.Spec.UpdateStrategy = appsv1.RollingUpdateStatefulSetStrategyType
			cr.Spec.Proxy.Router.Enabled = true
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("'mysql.clusterType' is set to 'async', 'proxy.router.enabled' must be disabled"))
			})
		})

		When("mysql replicas are set to even number", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-7", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.Size = 4
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("For 'group replication', using an even number of MySQL replicas requires 'unsafeFlags.mysqlSize: true'"))
			})
		})

		When("mysql replicas are set to lower than 3", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-8", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.Size = 2
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("Scaling MySQL replicas below 3 requires 'unsafeFlags.mysqlSize: true'"))
			})
		})

		When("mysql replicas are set to higher than 9", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-9", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.Size = 11
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("For 'group replication', scaling MySQL replicas above 9 requires 'unsafeFlags.mysqlSize: true'"))
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
		cr.Spec.Backup.PiTR.BinlogServer = &psv1alpha1.BinlogServerSpec{
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

var _ = Describe("PVC Resizing", Ordered, func() {
	ctx := context.Background()

	const crName = "pvc-resize"
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

	Context("Happy path PVC resizing", Ordered, func() {
		cr, err := readDefaultCR(crName, ns)

		It("should read default cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
		})

		cr.Spec.VolumeExpansionEnabled = true
		originalSize := cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]

		It("should create PerconaServerMySQL", func() {
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		It("should reconcile to create StatefulSet", func() {
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		})

		sts := &appsv1.StatefulSet{}
		It("should create MySQL StatefulSet", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      cr.Name + "-mysql",
					Namespace: cr.Namespace,
				}, sts)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())
		})

		It("should create StorageClass that supports volume expansion", func() {
			allowVolumeExpansion := true
			sc := &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-storage-class",
				},
				Provisioner:          "kubernetes.io/no-provisioner",
				AllowVolumeExpansion: &allowVolumeExpansion,
			}
			Expect(k8sClient.Create(ctx, sc)).Should(Succeed())
		})

		It("should create MySQL PVCs", func() {
			exposer := mysql.Exposer(*cr)
			for _, claim := range sts.Spec.VolumeClaimTemplates {
				if claim.Name != "datadir" {
					continue
				}
				for i := 0; i < int(*sts.Spec.Replicas); i++ {
					pvc := claim.DeepCopy()
					pvc.Labels = exposer.Labels()
					pvc.Name = fmt.Sprintf("%s-%s-%d", claim.Name, sts.Name, i)
					pvc.Namespace = ns
					pvc.Spec.VolumeName = fmt.Sprintf("pv-%s-%d", sts.Name, i)
					storageClassName := "test-storage-class"
					pvc.Spec.StorageClassName = &storageClassName
					Expect(k8sClient.Create(ctx, pvc)).Should(Succeed())

					Eventually(func() bool {
						err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pvc), pvc)
						if err != nil {
							return false
						}
						pvc.Status.Phase = corev1.ClaimBound
						pvc.Status.Capacity = corev1.ResourceList{
							corev1.ResourceStorage: originalSize,
						}
						return k8sClient.Status().Update(ctx, pvc) == nil
					}, time.Second*10, time.Millisecond*100).Should(BeTrue())
				}
			}
		})

		It("should create MySQL pods", func() {
			exposer := mysql.Exposer(*cr)
			for i := 0; i < int(*sts.Spec.Replicas); i++ {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("%s-%d", sts.Name, i),
						Namespace: ns,
						Labels:    exposer.Labels(),
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "mysql",
								Image: "mysql:8.0",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, pod)).Should(Succeed())
			}
		})

		When("volume expansion is requested", func() {
			newSize := resource.MustParse("10Gi")

			It("should update the CR with larger storage size", func() {
				Eventually(func() bool {
					err := k8sClient.Get(ctx, crNamespacedName, cr)
					return err == nil
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())

				cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = newSize
				Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
			})

			It("should trigger PVC resize when reconcilePersistentVolumes is called", func() {
				err := reconciler().reconcilePersistentVolumes(ctx, cr)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should set PVC resize annotation on CR", func() {
				Eventually(func() bool {
					err := k8sClient.Get(ctx, crNamespacedName, cr)
					if err != nil {
						return false
					}
					annotations := cr.GetAnnotations()
					_, exists := annotations[string(naming.AnnotationPVCResizeInProgress)]
					return exists
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())
			})

			It("should update PVC specs with new size", func() {
				pvcList := &corev1.PersistentVolumeClaimList{}
				Eventually(func() bool {
					err := k8sClient.List(ctx, pvcList, &client.ListOptions{
						Namespace:     cr.Namespace,
						LabelSelector: labels.SelectorFromSet(mysql.MatchLabels(cr)),
					})
					if err != nil {
						return false
					}

					matchingPVCs := 0
					for _, pvc := range pvcList.Items {
						if !strings.HasPrefix(pvc.Name, "datadir-") {
							continue
						}
						requestedSize := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
						if requestedSize.Cmp(newSize) == 0 {
							matchingPVCs++
						}
					}
					return matchingPVCs >= 3
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())
			})
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
		cr.Spec.SecretsName = "ps-cluster1-secrets"

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
							"app.kubernetes.io/name": "mysql",
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
							"app.kubernetes.io/name": "mysql",
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

var _ = Describe("Primary mysql service", Ordered, func() {
	ctx := context.Background()

	const crName = "gr-primary-service"
	const ns = "gr-primary-service"
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

	Context("Expose primary with gr cluster type", Ordered, func() {
		cr, err := readDefaultCR(crName, ns)

		cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeGR
		cr.Spec.MySQL.ExposePrimary.Enabled = true
		cr.Spec.MySQL.ExposePrimary.Type = corev1.ServiceTypeClusterIP

		It("Should read default cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should create cluster", func() {
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		It("Should reconcile once to create user secret", func() {
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should create primary service", func() {
			svc := &corev1.Service{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      cr.Name + "-mysql-primary",
					Namespace: cr.Namespace,
				}, svc)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			Expect(svc.Spec.Type).Should(Equal(corev1.ServiceTypeClusterIP))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("app.kubernetes.io/component", "database"))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("app.kubernetes.io/instance", "gr-primary-service"))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("app.kubernetes.io/managed-by", "percona-server-mysql-operator"))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("app.kubernetes.io/name", "mysql"))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("app.kubernetes.io/part-of", "percona-server"))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("mysql.percona.com/primary", "true"))
		})

		It("Should remove primary service when expose primary is disabled", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, crNamespacedName, cr)
				return err == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			cr.Spec.MySQL.ExposePrimary.Enabled = false
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			svc := &corev1.Service{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      cr.Name + "-mysql-primary",
					Namespace: cr.Namespace,
				}, svc)
				return k8serrors.IsNotFound(err)
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())
		})
	})

	Context("Expose primary with async cluster type", Ordered, func() {
		cr, err := readDefaultCR("async-cluster", ns)

		cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeAsync
		cr.Spec.MySQL.ExposePrimary.Enabled = true
		cr.Spec.MySQL.ExposePrimary.Type = corev1.ServiceTypeClusterIP
		cr.Spec.Orchestrator.Enabled = true

		It("Should read default cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should create async cluster", func() {
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		It("Should reconcile once to create user secret", func() {
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "async-cluster", Namespace: ns}})
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should create primary service for async cluster", func() {
			svc := &corev1.Service{}
			Consistently(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "async-cluster-mysql-primary",
					Namespace: cr.Namespace,
				}, svc)
				return err == nil
			}, time.Second*5, time.Millisecond*250).Should(BeTrue())

			Expect(svc.Spec.Type).Should(Equal(corev1.ServiceTypeClusterIP))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("app.kubernetes.io/component", "database"))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("app.kubernetes.io/instance", "async-cluster"))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("app.kubernetes.io/managed-by", "percona-server-mysql-operator"))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("app.kubernetes.io/name", "mysql"))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("app.kubernetes.io/part-of", "percona-server"))
			Expect(svc.Spec.Selector).Should(HaveKeyWithValue("mysql.percona.com/primary", "true"))
		})
	})
})

var _ = Describe("Global labels and annotations", Ordered, func() {
	ctx := context.Background()

	const crName = "global-labels-annotations"
	const ns = "gr-" + crName
	const asyncNS = "async-" + crName
	crNamespacedName := types.NamespacedName{Name: crName, Namespace: ns}
	asyncCrNamespacedName := types.NamespacedName{Name: crName, Namespace: asyncNS}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ns,
			Namespace: ns,
		},
	}

	asyncNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      asyncNS,
			Namespace: asyncNS,
		},
	}

	BeforeAll(func() {
		By("Creating the Namespace to perform the tests")
		err := k8sClient.Create(ctx, namespace)
		Expect(err).To(Not(HaveOccurred()))

		err = k8sClient.Create(ctx, asyncNamespace)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("Deleting the Namespace to perform the tests")
		_ = k8sClient.Delete(ctx, namespace)
		_ = k8sClient.Delete(ctx, asyncNamespace)
	})

	Context("Check labels/annotations on gr cluster type", Ordered, func() {
		cr, err := readDefaultCR(crName, ns)

		It("Should read default cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeGR
			cr.Spec.Metadata = &psv1alpha1.Metadata{
				Labels: map[string]string{
					"test-label": "test-value",
				},
				Annotations: map[string]string{
					"test-annotation": "test-value",
				},
			}
		})

		It("Should create cluster", func() {
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		It("Should reconcile once", func() {
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should check all objects", func() {
			dyn, err := dynamic.NewForConfig(cfg)
			Expect(err).NotTo(HaveOccurred())

			disc, err := discovery.NewDiscoveryClientForConfig(cfg)
			Expect(err).NotTo(HaveOccurred())

			gr, err := restmapper.GetAPIGroupResources(disc)
			Expect(err).NotTo(HaveOccurred())

			for _, list := range gr {
				for version, resources := range list.VersionedResources {
					for _, r := range resources {
						// Skip subresources (like pods/status)
						if strings.Contains(r.Name, "/") {
							continue
						}
						if !r.Namespaced {
							continue
						}

						gv, err := schema.ParseGroupVersion(version)
						if err != nil {
							continue
						}
						gvr := gv.WithResource(r.Name)

						resList, err := dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
						if err != nil {
							continue // some resources may not be listable
						}
						for _, item := range resList.Items {
							objectWithMissingMetadata := ""
							_, kind := item.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
							if item.GetLabels()["test-label"] != "test-value" || item.GetAnnotations()["test-annotation"] != "test-value" {
								objectWithMissingMetadata = item.GetName() + "/" + kind
							}
							Expect(objectWithMissingMetadata).To(BeEmpty())
						}
					}
				}
			}
		})
	})

	Context("Check labels/annotations on gr cluster type", Ordered, func() {
		ns := asyncNS
		cr, err := readDefaultCR("async-cluster", ns)

		cr.Spec.MySQL.ClusterType = psv1alpha1.ClusterTypeAsync
		cr.Spec.Orchestrator.Enabled = true
		cr.Spec.Metadata = &psv1alpha1.Metadata{
			Labels: map[string]string{
				"test-label": "test-value",
			},
			Annotations: map[string]string{
				"test-annotation": "test-value",
			},
		}

		It("Should read default cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should create async cluster", func() {
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		It("Should reconcile once to create user secret", func() {
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: asyncCrNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should check all objects", func() {
			dyn, err := dynamic.NewForConfig(cfg)
			Expect(err).NotTo(HaveOccurred())

			disc, err := discovery.NewDiscoveryClientForConfig(cfg)
			Expect(err).NotTo(HaveOccurred())

			gr, err := restmapper.GetAPIGroupResources(disc)
			Expect(err).NotTo(HaveOccurred())

			for _, list := range gr {
				for version, resources := range list.VersionedResources {
					for _, r := range resources {
						// Skip subresources (like pods/status)
						if strings.Contains(r.Name, "/") {
							continue
						}
						if !r.Namespaced {
							continue
						}

						gv, err := schema.ParseGroupVersion(version)
						if err != nil {
							continue
						}
						gvr := gv.WithResource(r.Name)

						resList, err := dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
						if err != nil {
							continue // some resources may not be listable
						}
						for _, item := range resList.Items {
							objectWithMissingMetadata := ""
							_, kind := item.GetObjectKind().GroupVersionKind().ToAPIVersionAndKind()
							if item.GetLabels()["test-label"] != "test-value" || item.GetAnnotations()["test-annotation"] != "test-value" {
								objectWithMissingMetadata = item.GetName() + "/" + kind
							}
							Expect(objectWithMissingMetadata).To(BeEmpty())
						}
					}
				}
			}
		})
	})
})
