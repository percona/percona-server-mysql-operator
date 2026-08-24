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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gs "github.com/onsi/gomega/gstruct"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/haproxy"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
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
		const pvcName = "pvc-vol"
		const mountPath = "/var/app/pvc"

		cr, err := readDefaultCR(crName+"-pvc-vol", ns)
		It("should read default cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
		})

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

		Specify("CR should be updated", func() {
			cr.MySQLSpec().Sidecars = append(cr.Spec.MySQL.Sidecars, sidecarPVC)
			cr.MySQLSpec().SidecarPVCs = []psv1.SidecarPVC{
				{
					Name: pvcName,
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("1G"),
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		Specify("controller should add specified sidecar and volume to mysql STS", func() {
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
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
			cr.MySQLSpec().ClusterType = psv1.ClusterTypeGR
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
		cr.Spec.CRVersion = version.Version()
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
					string(psv1.UserOperator): []byte(operatorPass),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).Should(Succeed())
		})

		It("should create cr.yaml", func() {
			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.Unsafe.Orchestrator = true
			cr.Spec.Unsafe.Proxy = true
			cr.Spec.MySQL.PodDisruptionBudget = &psv1.PodDisruptionBudgetSpec{
				MaxUnavailable: &intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: 20,
				},
			}
			cr.Spec.Proxy.Router.Enabled = false
			cr.Spec.Proxy.HAProxy.Enabled = true
			cr.Spec.Proxy.HAProxy.PodDisruptionBudget = &psv1.PodDisruptionBudgetSpec{
				MinAvailable: &intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: 12,
				},
			}
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.Orchestrator.PodDisruptionBudget = &psv1.PodDisruptionBudgetSpec{
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
				// reconcile and a second time cause the orchestrator needs 2 cycles
				_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
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
		cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
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

	Context("cr creation based on CheckNSetDefaults", Ordered, func() {
		defaultCR := new(psv1.PerconaServerMySQL)
		defaultCR.Namespace = ns
		defaultCR.Spec.InitContainer.Image = "init-image"
		defaultCR.Spec.Backup = &psv1.BackupSpec{
			Image: "backup-image",
		}
		defaultCR.Spec.MySQL.VolumeSpec = &psv1.VolumeSpec{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1G"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1G"),
					},
				},
			},
		}
		nn := func(name string) types.NamespacedName { return types.NamespacedName{Name: name, Namespace: ns} }
		When("defaults are used", Ordered, func() {
			cr := defaultCR.DeepCopy()
			cr.Name = "defaults-1"

			err := cr.CheckNSetDefaults(ctx, nil)
			Expect(err).NotTo(HaveOccurred())

			It("should fail the creation of cr", func() {
				err := k8sClient.Create(ctx, cr)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("mysql.image is required"))
				Expect(err.Error()).To(ContainSubstring("mysql.size must be greater than 0"))
			})
		})
		When("group-replication cluster", Ordered, func() {
			cr := defaultCR.DeepCopy()
			cr.Name = "gr-1"

			err := cr.CheckNSetDefaults(ctx, nil)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.Size = 3
			cr.Spec.Proxy.Router.Size = 3
			cr.Spec.Proxy.HAProxy.Size = 3
			cr.Spec.Orchestrator.Size = 3
			cr.Spec.Proxy.HAProxy.Enabled = true
			cr.Spec.Proxy.Router.Enabled = false
			cr.Spec.MySQL.VaultSecretName = ""

			cr.Spec.MySQL.Image = "mysql-image"
			cr.Spec.Toolkit.Image = "toolkit-image"
			cr.Spec.Proxy.HAProxy.Image = "haproxy-image"
			cr.Spec.Orchestrator.Image = "orc-image"

			It("should create and reconcile", func() {
				err := k8sClient.Create(ctx, cr)
				Expect(err).To(Succeed())

				_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: nn(cr.Name)})
				Expect(err).NotTo(HaveOccurred())
			})
		})
		When("async cluster", Ordered, func() {
			cr := defaultCR.DeepCopy()
			cr.Name = "async-1"
			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync

			err := cr.CheckNSetDefaults(ctx, nil)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.Size = 3
			cr.Spec.Proxy.Router.Size = 3
			cr.Spec.Proxy.HAProxy.Size = 3
			cr.Spec.Orchestrator.Size = 3
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.Proxy.HAProxy.Enabled = true
			cr.Spec.MySQL.VaultSecretName = ""

			cr.Spec.MySQL.Image = "mysql-image"
			cr.Spec.Toolkit.Image = "backup-image"
			cr.Spec.Proxy.HAProxy.Image = "haproxy-image"
			cr.Spec.Orchestrator.Image = "orc-image"

			It("should create and reconcile", func() {
				err := k8sClient.Create(ctx, cr)
				Expect(err).To(Succeed())

				_, err = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: nn(cr.Name)})
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Context("cr creation based on default mysql cluster file", Ordered, func() {
		When("the cr is configured using default values and async cluster type", Ordered, func() {
			cr, err := readDefaultCR("cr-validation-1", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.UpdateStrategy = appsv1.RollingUpdateStatefulSetStrategyType
			It("should read and create default cr.yaml", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("cluster type is async and the orchestrator is disabled but unsafe flag enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-2", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
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

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
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
			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
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

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
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

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
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

		When("group-replication cluster type with no proxy enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-10", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
			cr.Spec.Proxy.Router.Enabled = false
			cr.Spec.Proxy.HAProxy.Enabled = false
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("Invalid configuration: For 'group replication', MySQL Router or HAProxy must be enabled unless 'unsafeFlags.proxy' is enabled"))
			})
		})

		When("group-replication cluster type with no proxy enabled but unsafe flag enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-11", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
			cr.Spec.Proxy.Router.Enabled = false
			cr.Spec.Proxy.HAProxy.Enabled = false
			cr.Spec.Unsafe.Proxy = true
			It("should create the cluster successfully", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("group-replication cluster omits proxy but unsafe flags are enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-no-proxy", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
			cr.Spec.Unsafe.Proxy = true
			cr.Spec.Unsafe.ProxySize = true
			It("should create the cluster successfully", func() {
				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cr)
				Expect(err).NotTo(HaveOccurred())
				unstructured.RemoveNestedField(obj, "spec", "proxy")

				Expect(k8sClient.Create(ctx, &unstructured.Unstructured{Object: obj})).Should(Succeed())
			})
		})

		When("group-replication cluster type with router size less than 2", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-12", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
			cr.Spec.Proxy.Router.Enabled = true
			cr.Spec.Proxy.Router.Size = 1
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("Invalid configuration: For 'group replication', Router size must be 2 or greater unless 'unsafeFlags.proxySize' is enabled"))
			})
		})

		When("group-replication cluster type with router size less than 2 but unsafe flag enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-13", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
			cr.Spec.Proxy.Router.Enabled = true
			cr.Spec.Proxy.HAProxy.Enabled = false
			cr.Spec.Proxy.Router.Size = 1
			cr.Spec.Unsafe.ProxySize = true
			It("should create the cluster successfully", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("group-replication cluster type with mysql size less than 3", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-14", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
			cr.Spec.MySQL.Size = 2
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("Invalid configuration: For 'group replication', MySQL size must be 3 or greater unless 'unsafeFlags.mysqlSize' is enabled"))
			})
		})

		When("group-replication cluster type with mysql size less than 3 but unsafe flag enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-15", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
			cr.Spec.MySQL.Size = 2
			cr.Spec.Unsafe.MySQLSize = true
			It("should create the cluster successfully", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("async cluster type with orchestrator size less than 3", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-16", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.Orchestrator.Size = 2
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("Invalid configuration: For 'async' replication, Orchestrator size must be 3 or greater and odd unless 'unsafeFlags.orchestratorSize' is enabled"))
			})
		})

		When("async cluster type with orchestrator size even number", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-17", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.Orchestrator.Size = 4
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("Invalid configuration: For 'async' replication, Orchestrator size must be 3 or greater and odd unless 'unsafeFlags.orchestratorSize' is enabled"))
			})
		})

		When("async cluster type with orchestrator size less than 3 but unsafe flag enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-18", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.Orchestrator.Size = 2
			cr.Spec.Unsafe.OrchestratorSize = true
			It("should create the cluster successfully", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("async cluster type with orchestrator size even number but unsafe flag enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-19", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.Orchestrator.Size = 4
			cr.Spec.Unsafe.OrchestratorSize = true
			It("should create the cluster successfully", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("async cluster type with SmartUpdate but orchestrator disabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-20", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.UpdateStrategy = psv1.SmartUpdateStatefulSetStrategyType
			cr.Spec.Orchestrator.Enabled = false
			cr.Spec.Unsafe.Proxy = true
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("Invalid configuration: For 'async' replication, SmartUpdate requires Orchestrator to be enabled"))
			})
		})

		When("async cluster type with SmartUpdate and orchestrator enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-21", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.UpdateStrategy = psv1.SmartUpdateStatefulSetStrategyType
			cr.Spec.Orchestrator.Enabled = true
			It("should create the cluster successfully", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("async cluster type omits orchestrator but unsafe flag is enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-no-orchestrator", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.UpdateStrategy = appsv1.RollingUpdateStatefulSetStrategyType
			cr.Spec.Unsafe.Orchestrator = true
			cr.Spec.Unsafe.Proxy = true
			It("should create the cluster successfully", func() {
				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cr)
				Expect(err).NotTo(HaveOccurred())
				unstructured.RemoveNestedField(obj, "spec", "orchestrator")

				Expect(k8sClient.Create(ctx, &unstructured.Unstructured{Object: obj})).Should(Succeed())
			})
		})

		When("async cluster type with SmartUpdate omits orchestrator but unsafe flag is enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-smart-update-no-orchestrator", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.UpdateStrategy = psv1.SmartUpdateStatefulSetStrategyType
			cr.Spec.Unsafe.Orchestrator = true
			cr.Spec.Unsafe.Proxy = true
			It("the creation of the cluster should fail with error message", func() {
				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cr)
				Expect(err).NotTo(HaveOccurred())
				unstructured.RemoveNestedField(obj, "spec", "orchestrator")

				createErr := k8sClient.Create(ctx, &unstructured.Unstructured{Object: obj})
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("Invalid configuration: For 'async' replication, SmartUpdate requires Orchestrator to be enabled"))
			})
		})

		When("async cluster type with SmartUpdate disables orchestrator but unsafe flag is enabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-disabled-orchestrator", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.UpdateStrategy = psv1.SmartUpdateStatefulSetStrategyType
			cr.Spec.Orchestrator.Enabled = false
			cr.Spec.Unsafe.Orchestrator = true
			cr.Spec.Unsafe.Proxy = true
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("Invalid configuration: For 'async' replication, SmartUpdate requires Orchestrator to be enabled"))
			})
		})

		When("MySQL Router and HAProxy can't be enabled at the same time", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-20", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
			cr.Spec.Proxy.HAProxy.Enabled = true
			cr.Spec.Proxy.Router.Enabled = true
			It("the creation of the cluster should fail with error message", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("Invalid configuration: MySQL Router and HAProxy can't be enabled at the same time"))
			})
		})

		When("component image/size is missing but component is disabled", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-comp-disabled", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
			cr.Spec.Proxy.HAProxy.Enabled = true
			cr.Spec.Proxy.Router.Enabled = false
			cr.Spec.Proxy.Router.Image = ""
			cr.Spec.Proxy.Router.Size = 0
			cr.Spec.Orchestrator.Enabled = false
			cr.Spec.Orchestrator.Image = ""
			cr.Spec.Orchestrator.Size = 0
			It("should create the cluster successfully", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("group-replication cluster defines only HAProxy", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-haproxy-only", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.Proxy.Router = nil
			It("should create the cluster successfully", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("group-replication cluster defines only Router", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-router-only", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.Proxy.HAProxy = nil
			cr.Spec.Proxy.Router.Enabled = true
			It("should create the cluster successfully", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("async cluster defines only HAProxy", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-async-haproxy-only", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.Proxy.Router = nil
			It("should create the cluster successfully", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("haproxy is enabled but image is missing", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-haproxy-no-image", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.Proxy.HAProxy.Enabled = true
			cr.Spec.Proxy.HAProxy.Image = ""
			It("should fail with image required error", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("haproxy.image is required when haproxy is enabled"))
			})
		})

		When("haproxy is enabled but size is 0", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-haproxy-no-size", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.Proxy.HAProxy.Enabled = true
			cr.Spec.Proxy.HAProxy.Size = 0
			It("should fail with size required error", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("haproxy.size must be greater than 0 when haproxy is enabled"))
			})
		})

		When("orchestrator is enabled but image is missing", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-orc-no-image", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
			cr.Spec.Orchestrator.Enabled = true
			cr.Spec.Orchestrator.Image = ""
			It("should fail with image required error", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("orchestrator.image is required when orchestrator is enabled"))
			})
		})

		When("router is enabled but image is missing", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-router-no-image", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
			cr.Spec.Proxy.Router.Enabled = true
			cr.Spec.Proxy.Router.Image = ""
			It("should fail with image required error", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("router.image is required when router is enabled"))
			})
		})

		When("storage autoscaling growth step is negative", Ordered, func() {
			cr, err := readDefaultCR("cr-validations-storage-autoscaling-growth-step-negative", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.StorageScaling = &psv1.StorageScalingSpec{
				EnableVolumeScaling: true,
				Autoscaling: &psv1.AutoscalingSpec{
					Enabled:    true,
					GrowthStep: resource.MustParse("-1Gi"),
				},
			}

			It("should fail with growth step required error", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("growthStep must be a positive quantity"))
			})
		})
	})

	Context("PITR validation rules", Ordered, func() {
		ns := "validate-pitr"

		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: ns,
			},
		}

		BeforeAll(func() {
			By("Creating the Namespace to perform the tests")
			err := k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))
		})

		AfterAll(func() {
			By("Deleting the Namespace")
			_ = k8sClient.Delete(ctx, namespace)
		})

		When("pitr is disabled, no binlogServer required", Ordered, func() {
			cr, err := readDefaultCR("pitr-disabled-no-binlog", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.Backup.PiTR.Enabled = false
			It("should create successfully without any binlogServer fields", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("pitr is disabled, binlogServer provided without image and size", Ordered, func() {
			cr, err := readDefaultCR("pitr-disabled-empty-binlog", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.Backup.PiTR.Enabled = false
			cr.Spec.Backup.PiTR.BinlogServer = &psv1.BinlogServerSpec{}
			It("should create successfully since pitr is disabled", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("pitr is enabled but binlogServer is missing", Ordered, func() {
			cr, err := readDefaultCR("pitr-enabled-no-binlog", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.Backup.PiTR.Enabled = true
			cr.Spec.Backup.PiTR.BinlogServer = nil
			It("should create successfully without cluster binlog server", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("pitr is enabled but binlogServer image is missing", Ordered, func() {
			cr, err := readDefaultCR("pitr-enabled-no-image", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.Backup.PiTR.Enabled = true
			cr.Spec.Backup.PiTR.BinlogServer = &psv1.BinlogServerSpec{}
			cr.Spec.Backup.PiTR.BinlogServer.Size = 1
			It("should fail with image required error", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("binlogServer.image is required when pitr is enabled"))
			})
		})

		When("pitr is enabled but binlogServer size is 0", Ordered, func() {
			cr, err := readDefaultCR("pitr-enabled-no-size", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.Backup.PiTR.Enabled = true
			cr.Spec.Backup.PiTR.BinlogServer = &psv1.BinlogServerSpec{}
			cr.Spec.Backup.PiTR.BinlogServer.Image = "binlog-server-image"
			It("should fail with size required error", func() {
				createErr := k8sClient.Create(ctx, cr)
				Expect(createErr).To(HaveOccurred())
				Expect(createErr.Error()).To(ContainSubstring("binlogServer.size is required when pitr is enabled"))
			})
		})

		When("pitr is enabled with all required fields set", Ordered, func() {
			cr, err := readDefaultCR("pitr-enabled-valid", ns)
			Expect(err).NotTo(HaveOccurred())

			cr.Spec.Backup.PiTR.Enabled = true
			cr.Spec.Backup.PiTR.BinlogServer = &psv1.BinlogServerSpec{}
			cr.Spec.Backup.PiTR.BinlogServer.Image = "binlog-server-image"
			cr.Spec.Backup.PiTR.BinlogServer.Size = 1
			cr.Spec.Backup.PiTR.BinlogServer.ServerID = 100
			It("should create successfully", func() {
				Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
			})
		})

		When("the referenced vault secret is missing", Ordered, func() {
			var cr *psv1.PerconaServerMySQL

			BeforeAll(func() {
				var err error
				cr, err = readDefaultCR("vault-missing", ns)
				Expect(err).NotTo(HaveOccurred())

				cr.Spec.MySQL.VaultSecretName = "vault-missing-secret"
				Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			})

			It("fails reconcile with a vault error condition", func() {
				_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)})
				Expect(err).To(HaveOccurred())

				observed := &psv1.PerconaServerMySQL{}
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), observed)).To(Succeed())

				errCond := meta.FindStatusCondition(observed.Status.Conditions, psv1.StateError.String())
				Expect(errCond).ToNot(BeNil())
				Expect(errCond.Message).To(ContainSubstring(`get vault secret 'vault-missing-secret': secrets "vault-missing-secret" not found`))
			})
		})

		When("the referenced vault secret is present", Ordered, func() {
			var cr *psv1.PerconaServerMySQL

			BeforeAll(func() {
				var err error
				cr, err = readDefaultCR("vault-present", ns)
				Expect(err).NotTo(HaveOccurred())

				cr.Spec.MySQL.VaultSecretName = "vault-present-secret"
				Expect(k8sClient.Create(ctx, cr)).To(Succeed())

				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      cr.Spec.MySQL.VaultSecretName,
						Namespace: cr.Namespace,
					},
					StringData: map[string]string{
						"keyring_vault.cnf": `vault_url = https://vault.example.com:8200
secret_mount_point = secret_v2
token = s.1234567890abcdef
vault_ca = /etc/mysql/vault-keyring-secret/ca.cert`,
					},
				}
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			})

			It("passes vault validation", func() {
				// Reconcile still fails on downstream steps under envtest (no real MySQL),
				// so we only assert that vault validation itself was not the failure.
				_, _ = reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)})

				observed := &psv1.PerconaServerMySQL{}
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), observed)).To(Succeed())

				if errCond := meta.FindStatusCondition(observed.Status.Conditions, psv1.StateError.String()); errCond != nil {
					Expect(errCond.Message).NotTo(ContainSubstring("vault secret"))
				}
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
		cr.Spec.Backup.PiTR.BinlogServer = &psv1.BinlogServerSpec{
			ConnectTimeout: 20,
			WriteTimeout:   20,
			ReadTimeout:    20,
			ServerID:       42,
			IdleTime:       60,
			Storage: psv1.BinlogServerStorageSpec{
				S3: &psv1.BackupStorageS3Spec{
					Bucket:            "s3-test-bucket",
					Region:            "us-west-1",
					EndpointURL:       "https://s3.amazonaws.com",
					CredentialsSecret: "s3-test-credentials",
				},
			},
			PodSpec: psv1.PodSpec{
				Size: 1,
				ContainerSpec: psv1.ContainerSpec{
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

		It("should set MySQL status as ready", func() {
			fetchedCR := cr.DeepCopy()
			Expect(k8sClient.Get(ctx, crNamespacedName, fetchedCR)).Should(Succeed())
			fetchedCR.Status.MySQL.Ready = 1
			fetchedCR.Status.Host = mysql.FQDN(fetchedCR, 0)
			Expect(k8sClient.Status().Update(ctx, fetchedCR)).Should(Succeed())
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
					pvc.Labels = exposer.MatchLabels()
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
						Labels:    exposer.MatchLabels(),
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
					pvc.Labels = exposer.MatchLabels()
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

		cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
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

		cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
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

			cr.Spec.MySQL.ClusterType = psv1.ClusterTypeGR
			cr.Spec.Metadata = &psv1.Metadata{
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
						if strings.HasSuffix(r.Kind, "Event") {
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
		It("Should update global labels and annotations", func() {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), cr)).Should(Succeed())

			cr.Spec.Metadata = &psv1.Metadata{
				Labels: map[string]string{
					"test-label2": "test-value2",
				},
				Annotations: map[string]string{
					"test-annotation2": "test-value2",
				},
			}
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
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
							if item.GetLabels()["test-label2"] != "test-value2" || item.GetAnnotations()["test-annotation2"] != "test-value2" {
								objectWithMissingMetadata = item.GetName() + "/" + kind
							}
							Expect(objectWithMissingMetadata).To(BeEmpty())
						}
					}
				}
			}
		})
		It("Should update global labels and annotations", func() {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), cr)).Should(Succeed())

			cr.Spec.Metadata = &psv1.Metadata{
				Labels: map[string]string{
					"test-label3": "test-value3",
				},
				Annotations: map[string]string{
					"test-annotation3": "test-value3",
				},
			}
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
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
							if item.GetLabels()["test-label3"] != "test-value3" || item.GetAnnotations()["test-annotation3"] != "test-value3" {
								objectWithMissingMetadata = item.GetName() + "/" + kind
							}
							Expect(objectWithMissingMetadata).To(BeEmpty())
						}
					}
				}
			}
		})
	})

	Context("Check labels/annotations on async cluster type", Ordered, func() {
		ns := asyncNS
		cr, err := readDefaultCR("async-cluster", ns)

		cr.Spec.MySQL.ClusterType = psv1.ClusterTypeAsync
		cr.Spec.Orchestrator.Enabled = true
		cr.Spec.Metadata = &psv1.Metadata{
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
		It("Should update global labels and annotations", func() {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), cr)).Should(Succeed())

			cr.Spec.Metadata = &psv1.Metadata{
				Labels: map[string]string{
					"test-label2": "test-value2",
				},
				Annotations: map[string]string{
					"test-annotation2": "test-value2",
				},
			}
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
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
							if item.GetLabels()["test-label2"] != "test-value2" || item.GetAnnotations()["test-annotation2"] != "test-value2" {
								objectWithMissingMetadata = item.GetName() + "/" + kind
							}
							Expect(objectWithMissingMetadata).To(BeEmpty())
						}
					}
				}
			}
		})
		It("Should update global labels and annotations", func() {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), cr)).Should(Succeed())

			cr.Spec.Metadata = &psv1.Metadata{
				Labels: map[string]string{
					"test-label3": "test-value3",
				},
				Annotations: map[string]string{
					"test-annotation3": "test-value3",
				},
			}
			Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
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
							if item.GetLabels()["test-label3"] != "test-value3" || item.GetAnnotations()["test-annotation3"] != "test-value3" {
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

var _ = Describe("BinlogServer", Ordered, func() {
	ctx := context.Background()

	const crName = "pitr-test"
	const ns = crName
	crNamespacedName := types.NamespacedName{Name: crName, Namespace: ns}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
		},
	}

	BeforeAll(func() {
		By("Creating the Namespace")
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
	})

	AfterAll(func() {
		_ = k8sClient.Delete(ctx, namespace)
	})

	cr, err := readDefaultCR(crName, ns)
	It("should read default cr.yaml", func() {
		Expect(err).NotTo(HaveOccurred())
	})

	It("should configure PiTR and create the CR", func() {
		cr.Spec.Backup.PiTR.Enabled = true
		cr.Spec.Backup.PiTR.BinlogServer = &psv1.BinlogServerSpec{
			Storage: psv1.BinlogServerStorageSpec{
				S3: &psv1.BackupStorageS3Spec{
					Bucket:            "test-bucket",
					Region:            "us-east-1",
					EndpointURL:       "s3://s3.amazonaws.com",
					CredentialsSecret: "s3-secret",
				},
			},
			ServerID: 1,
			PodSpec: psv1.PodSpec{
				Size: 1,
				ContainerSpec: psv1.ContainerSpec{
					Image: "perconalab/percona-binlog-server:0.2.0",
				},
			},
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())
	})

	It("should create the S3 credentials secret", func() {
		s3Secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "s3-secret",
				Namespace: ns,
			},
			Data: map[string][]byte{
				secret.CredentialsAWSAccessKey: []byte("access-key"),
				secret.CredentialsAWSSecretKey: []byte("secret-key"),
			},
		}
		Expect(k8sClient.Create(ctx, s3Secret)).To(Succeed())
	})

	It("should create the internal secret with the replication user password", func() {
		internalSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cr.InternalSecretName(),
				Namespace: ns,
			},
			Data: map[string][]byte{
				string(psv1.UserReplication): []byte("repl-password"),
			},
		}
		Expect(k8sClient.Create(ctx, internalSecret)).To(Succeed())
	})

	It("should set the binlog server connection host to the primary service", func() {
		Expect(k8sClient.Get(ctx, crNamespacedName, cr)).To(Succeed())

		Expect(reconciler().reconcileBinlogServer(ctx, cr)).To(Succeed())

		configSecret := &corev1.Secret{}
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      binlogserver.ConfigSecretName(cr),
				Namespace: ns,
			}, configSecret)
			return err == nil
		}, time.Second*15, time.Millisecond*250).Should(BeTrue())

		var config binlogserver.Configuration
		Expect(json.Unmarshal(configSecret.Data[binlogserver.ConfigKey], &config)).To(Succeed())

		Expect(config.Connection.Host).To(Equal(fmt.Sprintf("%s.%s", mysql.PrimaryServiceName(cr), ns)))
	})

	It("should create the binlog server StatefulSet once MySQL is ready", func() {
		Expect(k8sClient.Get(ctx, crNamespacedName, cr)).To(Succeed())

		cr.Status.MySQL.Ready = 1
		cr.Status.Host = "pitr-test-haproxy.pitr-test"
		Expect(k8sClient.Status().Update(ctx, cr)).To(Succeed())

		Expect(k8sClient.Get(ctx, crNamespacedName, cr)).To(Succeed())
		Expect(reconciler().reconcileBinlogServer(ctx, cr)).To(Succeed())

		sts := &appsv1.StatefulSet{}
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      binlogserver.Name(cr),
				Namespace: ns,
			}, sts)
			return err == nil
		}, time.Second*15, time.Millisecond*250).Should(BeTrue())

		Expect(sts.Spec.Replicas).To(gs.PointTo(BeEquivalentTo(1)))
	})
})

var _ = Describe("PVC Resizing with orphaned PVCs", Ordered, func() {
	ctx := context.Background()

	const crName = "pvc-resize-orphan"
	const ns = crName
	crNamespacedName := types.NamespacedName{Name: crName, Namespace: ns}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ns,
			Namespace: ns,
		},
	}

	BeforeAll(func() {
		Expect(k8sClient.Create(ctx, namespace)).To(Not(HaveOccurred()))
	})

	AfterAll(func() {
		_ = k8sClient.Delete(ctx, namespace)
	})

	// Scaling the cluster up and back down leaves the PVCs of the removed
	// replicas behind, still holding the old size. They have no pod, so they are
	// never resized and must not be taken into account when the operator decides
	// whether a resize is needed.
	Context("PVCs left behind by a scale down are smaller than the live ones", Ordered, func() {
		cr, err := readDefaultCR(crName, ns)

		It("should read default cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
		})

		cr.Spec.StorageScaling = &psv1.StorageScalingSpec{EnableVolumeScaling: true}
		orphanSize := resource.MustParse("2Gi")
		liveSize := resource.MustParse("3Gi")
		cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = liveSize

		It("should create PerconaServerMySQL", func() {
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		It("should reconcile to create StatefulSet", func() {
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		})

		sts := &appsv1.StatefulSet{}
		stsName := types.NamespacedName{Name: cr.Name + "-mysql", Namespace: ns}
		It("should create MySQL StatefulSet", func() {
			Eventually(func() bool {
				return k8sClient.Get(ctx, stsName, sts) == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())
		})

		It("should create StorageClass that supports volume expansion", func() {
			allowVolumeExpansion := true
			sc := &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "orphan-storage-class",
				},
				Provisioner:          "kubernetes.io/no-provisioner",
				AllowVolumeExpansion: &allowVolumeExpansion,
			}
			Expect(k8sClient.Create(ctx, sc)).Should(Succeed())
		})

		It("should create 5 PVCs where the 2 orphaned ones kept the old size", func() {
			exposer := mysql.Exposer(*cr)
			var claim *corev1.PersistentVolumeClaim
			for _, c := range sts.Spec.VolumeClaimTemplates {
				if c.Name == "datadir" {
					claim = c.DeepCopy()
				}
			}
			Expect(claim).NotTo(BeNil())

			for i := 0; i < 5; i++ {
				size := liveSize
				if i >= int(cr.Spec.MySQL.Size) {
					size = orphanSize
				}

				pvc := claim.DeepCopy()
				pvc.Labels = exposer.MatchLabels()
				pvc.Name = fmt.Sprintf("datadir-%s-%d", sts.Name, i)
				pvc.Namespace = ns
				pvc.Spec.VolumeName = fmt.Sprintf("pv-orphan-%s-%d", sts.Name, i)
				pvc.Spec.Resources.Requests[corev1.ResourceStorage] = size
				storageClassName := "orphan-storage-class"
				pvc.Spec.StorageClassName = &storageClassName
				Expect(k8sClient.Create(ctx, pvc)).Should(Succeed())

				Eventually(func() bool {
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pvc), pvc); err != nil {
						return false
					}
					pvc.Status.Phase = corev1.ClaimBound
					pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: size}
					return k8sClient.Status().Update(ctx, pvc) == nil
				}, time.Second*10, time.Millisecond*100).Should(BeTrue())
			}
		})

		It("should create a pod for every live PVC only", func() {
			exposer := mysql.Exposer(*cr)
			for i := 0; i < int(cr.Spec.MySQL.Size); i++ {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("%s-%d", sts.Name, i),
						Namespace: ns,
						Labels:    exposer.MatchLabels(),
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "mysql", Image: "mysql:8.0"}},
					},
				}
				Expect(k8sClient.Create(ctx, pod)).Should(Succeed())
			}
		})

		It("should not start a resize when every live PVC already has the requested size", func() {
			Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
			Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

			Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
			_, exists := cr.GetAnnotations()[string(naming.AnnotationPVCResizeInProgress)]
			Expect(exists).To(BeFalse(), "orphaned PVCs must not trigger a resize")
		})

		It("should not delete the StatefulSet", func() {
			// envtest runs no garbage collector, so an orphan-propagation delete
			// leaves the object behind with a deletion timestamp instead of
			// removing it. Existence alone therefore proves nothing.
			for i := 0; i < 3; i++ {
				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())
				Expect(k8sClient.Get(ctx, stsName, sts)).Should(Succeed(), "statefulset should not be deleted in a loop")
				Expect(sts.DeletionTimestamp).To(BeNil(), "statefulset should not be deleted in a loop")
			}
		})

		// A scale up reuses the leftover PVCs. They must be expanded before their
		// replica is created, otherwise the new replica clones data onto a volume
		// that is still too small.
		When("the cluster is scaled back up", Ordered, func() {
			stsUID := ""
			It("should scale the CR up to 5", func() {
				Expect(k8sClient.Get(ctx, stsName, sts)).Should(Succeed())
				stsUID = string(sts.UID)
				Expect(stsUID).NotTo(BeEmpty())

				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				cr.Spec.MySQL.Size = 5
				Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
			})

			It("should expand the leftover PVCs before their pods exist", func() {
				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

				for i := 3; i < 5; i++ {
					pvc := &corev1.PersistentVolumeClaim{}
					key := types.NamespacedName{Name: fmt.Sprintf("datadir-%s-%d", sts.Name, i), Namespace: ns}
					Expect(k8sClient.Get(ctx, key, pvc)).Should(Succeed())
					requested := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
					Expect(requested.Cmp(liveSize)).To(Equal(0), "leftover PVC should be expanded before its replica is created")

					pod := &corev1.Pod{}
					podKey := types.NamespacedName{Name: fmt.Sprintf("%s-%d", sts.Name, i), Namespace: ns}
					err := k8sClient.Get(ctx, podKey, pod)
					Expect(k8serrors.IsNotFound(err)).To(BeTrue(), "the replica should not exist yet")
				}

				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				_, exists := cr.GetAnnotations()[string(naming.AnnotationPVCResizeInProgress)]
				Expect(exists).To(BeTrue(), "resize should be marked as in progress")
			})

			It("should hold the StatefulSet at its current size while resizing", func() {
				_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
				Expect(err).NotTo(HaveOccurred())

				Expect(k8sClient.Get(ctx, stsName, sts)).Should(Succeed())
				Expect(sts.Spec.Replicas).To(gs.PointTo(BeEquivalentTo(3)), "the new replicas must wait for the resize")
			})

			It("should finish the resize once the volumes are expanded", func() {
				// the volume is expanded, only the filesystem is still to be grown,
				// which kubelet does when the replica mounts it
				for i := 3; i < 5; i++ {
					pvc := &corev1.PersistentVolumeClaim{}
					key := types.NamespacedName{Name: fmt.Sprintf("datadir-%s-%d", sts.Name, i), Namespace: ns}
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, key, pvc); err != nil {
							return false
						}
						pvc.Status.AllocatedResources = corev1.ResourceList{corev1.ResourceStorage: liveSize}
						pvc.Status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
							corev1.ResourceStorage: corev1.PersistentVolumeClaimNodeResizePending,
						}
						return k8sClient.Status().Update(ctx, pvc) == nil
					}, time.Second*10, time.Millisecond*100).Should(BeTrue())
				}

				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				_, exists := cr.GetAnnotations()[string(naming.AnnotationPVCResizeInProgress)]
				Expect(exists).To(BeFalse(), "resize should not block the scale up once the volumes are expanded")
			})

			It("should not recreate the StatefulSet when its volume template is already correct", func() {
				Expect(k8sClient.Get(ctx, stsName, sts)).Should(Succeed())
				Expect(string(sts.UID)).To(Equal(stsUID), "recreating the statefulset costs a needless rolling restart")
				Expect(sts.DeletionTimestamp).To(BeNil(), "recreating the statefulset costs a needless rolling restart")
			})

			It("should let the StatefulSet scale up afterwards", func() {
				Eventually(func() bool {
					if _, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName}); err != nil {
						return false
					}
					if err := k8sClient.Get(ctx, stsName, sts); err != nil {
						return false
					}
					return sts.Spec.Replicas != nil && *sts.Spec.Replicas == 5
				}, time.Second*15, time.Millisecond*250).Should(BeTrue())
			})
		})

		// The statefulset must still be recreated when its volume claim template is
		// stale, since that template is immutable.
		When("the requested size no longer matches the volume template", Ordered, func() {
			biggerSize := resource.MustParse("4Gi")
			stsUID := ""

			It("should request a bigger volume", func() {
				Expect(k8sClient.Get(ctx, stsName, sts)).Should(Succeed())
				stsUID = string(sts.UID)
				configured := sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
				Expect(configured.Cmp(biggerSize)).NotTo(Equal(0), "volume template should be stale for this case")

				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = biggerSize
				Expect(k8sClient.Update(ctx, cr)).Should(Succeed())
			})

			It("should resize every PVC of the cluster", func() {
				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

				for i := 0; i < 5; i++ {
					pvc := &corev1.PersistentVolumeClaim{}
					key := types.NamespacedName{Name: fmt.Sprintf("datadir-%s-%d", sts.Name, i), Namespace: ns}
					mounted := i < int(cr.Spec.MySQL.Size) && i < 3
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, key, pvc); err != nil {
							return false
						}
						if mounted {
							pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: biggerSize}
						} else {
							// no replica mounts it, so only the volume grows
							pvc.Status.AllocatedResources = corev1.ResourceList{corev1.ResourceStorage: biggerSize}
							pvc.Status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
								corev1.ResourceStorage: corev1.PersistentVolumeClaimNodeResizePending,
							}
						}
						return k8sClient.Status().Update(ctx, pvc) == nil
					}, time.Second*10, time.Millisecond*100).Should(BeTrue())
				}
			})

			It("should recreate the StatefulSet to update the stale volume template", func() {
				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

				cur := &appsv1.StatefulSet{}
				err := k8sClient.Get(ctx, stsName, cur)
				deleted := k8serrors.IsNotFound(err) ||
					(err == nil && (string(cur.UID) != stsUID || cur.DeletionTimestamp != nil))
				Expect(deleted).To(BeTrue(), "a stale volume template must still force a recreate")
			})
		})
	})
})

var _ = Describe("PVC Resizing with a size that is not whole GiB", Ordered, func() {
	ctx := context.Background()

	const crName = "pvc-resize-nogib"
	const ns = crName
	crNamespacedName := types.NamespacedName{Name: crName, Namespace: ns}

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ns,
			Namespace: ns,
		},
	}

	BeforeAll(func() {
		Expect(k8sClient.Create(ctx, namespace)).To(Not(HaveOccurred()))
	})

	AfterAll(func() {
		_ = k8sClient.Delete(ctx, namespace)
	})

	// PVCs are resized to whole GiB while the volume claim template keeps the
	// size as written in the spec, and a replica that never mounted its volume
	// can carry a stale resize status from an earlier expansion.
	Context("a replica has no pod and the request is rounded up", Ordered, func() {
		cr, err := readDefaultCR(crName, ns)

		It("should read default cr.yaml", func() {
			Expect(err).NotTo(HaveOccurred())
		})

		specSize := resource.MustParse("2500Mi") // what the template holds
		roundedSize := resource.MustParse("3Gi") // what the PVCs are resized to
		cr.Spec.StorageScaling = &psv1.StorageScalingSpec{EnableVolumeScaling: true}
		cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = specSize

		It("should create PerconaServerMySQL", func() {
			Expect(k8sClient.Create(ctx, cr)).Should(Succeed())
		})

		It("should reconcile to create StatefulSet", func() {
			_, err := reconciler().Reconcile(ctx, ctrl.Request{NamespacedName: crNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		})

		sts := &appsv1.StatefulSet{}
		stsName := types.NamespacedName{Name: cr.Name + "-mysql", Namespace: ns}
		It("should create MySQL StatefulSet with the size as written", func() {
			Eventually(func() bool {
				return k8sClient.Get(ctx, stsName, sts) == nil
			}, time.Second*15, time.Millisecond*250).Should(BeTrue())

			configured := sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(configured.Cmp(specSize)).To(Equal(0), "the template holds the unrounded size")
		})

		It("should create StorageClass that supports volume expansion", func() {
			allowVolumeExpansion := true
			sc := &storagev1.StorageClass{
				ObjectMeta:           metav1.ObjectMeta{Name: "nogib-storage-class"},
				Provisioner:          "kubernetes.io/no-provisioner",
				AllowVolumeExpansion: &allowVolumeExpansion,
			}
			Expect(k8sClient.Create(ctx, sc)).Should(Succeed())
		})

		It("should create PVCs, and a pod for all but the last replica", func() {
			exposer := mysql.Exposer(*cr)
			var claim *corev1.PersistentVolumeClaim
			for _, c := range sts.Spec.VolumeClaimTemplates {
				if c.Name == "datadir" {
					claim = c.DeepCopy()
				}
			}
			Expect(claim).NotTo(BeNil())

			for i := 0; i < int(cr.Spec.MySQL.Size); i++ {
				pvc := claim.DeepCopy()
				pvc.Labels = exposer.MatchLabels()
				pvc.Name = fmt.Sprintf("datadir-%s-%d", sts.Name, i)
				pvc.Namespace = ns
				pvc.Spec.VolumeName = fmt.Sprintf("pv-nogib-%s-%d", sts.Name, i)
				storageClassName := "nogib-storage-class"
				pvc.Spec.StorageClassName = &storageClassName
				Expect(k8sClient.Create(ctx, pvc)).Should(Succeed())

				Eventually(func() bool {
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pvc), pvc); err != nil {
						return false
					}
					pvc.Status.Phase = corev1.ClaimBound
					pvc.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: specSize}
					return k8sClient.Status().Update(ctx, pvc) == nil
				}, time.Second*10, time.Millisecond*100).Should(BeTrue())
			}

			// the last replica has no pod, so its volume is never mounted
			for i := 0; i < int(cr.Spec.MySQL.Size)-1; i++ {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("%s-%d", sts.Name, i),
						Namespace: ns,
						Labels:    exposer.MatchLabels(),
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "mysql", Image: "mysql:8.0"}},
					},
				}
				Expect(k8sClient.Create(ctx, pod)).Should(Succeed())
			}
		})

		lastPVC := types.NamespacedName{}
		It("should resize every PVC up to whole GiB", func() {
			lastPVC = types.NamespacedName{
				Name:      fmt.Sprintf("datadir-%s-%d", sts.Name, int(cr.Spec.MySQL.Size)-1),
				Namespace: ns,
			}

			Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
			Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, lastPVC, pvc)).Should(Succeed())
			requested := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(requested.Cmp(roundedSize)).To(Equal(0))
		})

		It("should not treat a stale resize status as the current resize", func() {
			// The volume of the pod-less replica was expanded to the OLD size by an
			// earlier resize and never mounted since, so it still reports
			// NodeResizePending and the sticky FileSystemResizePending condition
			// while allocatedResources lags behind the new request.
			pvc := &corev1.PersistentVolumeClaim{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, lastPVC, pvc); err != nil {
					return false
				}
				pvc.Status.AllocatedResources = corev1.ResourceList{corev1.ResourceStorage: specSize}
				pvc.Status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
					corev1.ResourceStorage: corev1.PersistentVolumeClaimNodeResizePending,
				}
				pvc.Status.Conditions = []corev1.PersistentVolumeClaimCondition{{
					Type:               corev1.PersistentVolumeClaimFileSystemResizePending,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
				}}
				return k8sClient.Status().Update(ctx, pvc) == nil
			}, time.Second*10, time.Millisecond*100).Should(BeTrue())

			// the mounted ones are done
			for i := 0; i < int(cr.Spec.MySQL.Size)-1; i++ {
				p := &corev1.PersistentVolumeClaim{}
				key := types.NamespacedName{Name: fmt.Sprintf("datadir-%s-%d", sts.Name, i), Namespace: ns}
				Eventually(func() bool {
					if err := k8sClient.Get(ctx, key, p); err != nil {
						return false
					}
					p.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: roundedSize}
					return k8sClient.Status().Update(ctx, p) == nil
				}, time.Second*10, time.Millisecond*100).Should(BeTrue())
			}

			Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
			Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

			Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
			_, exists := cr.GetAnnotations()[string(naming.AnnotationPVCResizeInProgress)]
			Expect(exists).To(BeTrue(), "the volume has not been expanded for this request yet")
		})

		It("should finish and leave the StatefulSet alone once the volume is expanded", func() {
			Expect(k8sClient.Get(ctx, stsName, sts)).Should(Succeed())
			stsUID := string(sts.UID)

			pvc := &corev1.PersistentVolumeClaim{}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, lastPVC, pvc); err != nil {
					return false
				}
				pvc.Status.AllocatedResources = corev1.ResourceList{corev1.ResourceStorage: roundedSize}
				return k8sClient.Status().Update(ctx, pvc) == nil
			}, time.Second*10, time.Millisecond*100).Should(BeTrue())

			Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
			Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

			Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
			_, exists := cr.GetAnnotations()[string(naming.AnnotationPVCResizeInProgress)]
			Expect(exists).To(BeFalse(), "resize should be complete")

			// the template already holds the size as written in the spec, so
			// recreating the statefulset would restart the replicas for nothing
			Expect(k8sClient.Get(ctx, stsName, sts)).Should(Succeed())
			Expect(string(sts.UID)).To(Equal(stsUID), "template matches the spec, no recreate needed")
			Expect(sts.DeletionTimestamp).To(BeNil(), "template matches the spec, no recreate needed")
		})

		// Wherever RecoverVolumeExpansionFailure is off the per request resize
		// status is not reported at all. The event of this resize is then the only
		// sign that the volume is expanded, and a resize that never finishes holds
		// the replicas back for good.
		When("the cluster reports no resize status", Ordered, func() {
			lastSize := resource.MustParse("6Gi")

			It("should request a bigger volume", func() {
				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = lastSize
				Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

				// the mounted claims are done, the pod-less one reports no status at
				// all and keeps a condition left over from the previous expansion
				for i := 0; i < int(cr.Spec.MySQL.Size)-1; i++ {
					p := &corev1.PersistentVolumeClaim{}
					key := types.NamespacedName{Name: fmt.Sprintf("datadir-%s-%d", sts.Name, i), Namespace: ns}
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, key, p); err != nil {
							return false
						}
						p.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: lastSize}
						return k8sClient.Status().Update(ctx, p) == nil
					}, time.Second*10, time.Millisecond*100).Should(BeTrue())
				}

				pvc := &corev1.PersistentVolumeClaim{}
				Eventually(func() bool {
					if err := k8sClient.Get(ctx, lastPVC, pvc); err != nil {
						return false
					}
					pvc.Status.AllocatedResources = nil
					pvc.Status.AllocatedResourceStatuses = nil
					pvc.Status.Conditions = []corev1.PersistentVolumeClaimCondition{{
						Type:               corev1.PersistentVolumeClaimFileSystemResizePending,
						Status:             corev1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour)),
					}}
					return k8sClient.Status().Update(ctx, pvc) == nil
				}, time.Second*10, time.Millisecond*100).Should(BeTrue())
			})

			It("should finish from the condition where no status is reported", func() {
				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				_, exists := cr.GetAnnotations()[string(naming.AnnotationPVCResizeInProgress)]
				Expect(exists).To(BeFalse(), "a resize that never finishes holds the replicas back")
			})

			It("should not start the resize over again", func() {
				// the claim reports a capacity below the request until a replica
				// mounts it, so a size read that disagrees with the one that ended
				// the resize starts it again, and the replicas never arrive
				for i := 0; i < 3; i++ {
					Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
					Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

					Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
					_, exists := cr.GetAnnotations()[string(naming.AnnotationPVCResizeInProgress)]
					Expect(exists).To(BeFalse(), "the resize must not be started over")
				}
			})
		})

		// A claim can already be bigger than the request, for instance after a
		// resize that only some of them completed. It needs no expansion, and it
		// can never shrink to match, so it has to count as done.
		When("a claim is already bigger than the request", Ordered, func() {
			bigger := resource.MustParse("8Gi")
			biggest := resource.MustParse("10Gi")

			It("should start a resize with one claim already past it", func() {
				first := types.NamespacedName{Name: fmt.Sprintf("datadir-%s-0", sts.Name), Namespace: ns}
				p := &corev1.PersistentVolumeClaim{}

				// grow it past the size that is about to be requested, spec and all,
				// so that asking it to match would be a shrink the API refuses
				Eventually(func() bool {
					if err := k8sClient.Get(ctx, first, p); err != nil {
						return false
					}
					p.Spec.Resources.Requests[corev1.ResourceStorage] = biggest
					return k8sClient.Update(ctx, p) == nil
				}, time.Second*10, time.Millisecond*100).Should(BeTrue())

				Eventually(func() bool {
					if err := k8sClient.Get(ctx, first, p); err != nil {
						return false
					}
					p.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: biggest}
					return k8sClient.Status().Update(ctx, p) == nil
				}, time.Second*10, time.Millisecond*100).Should(BeTrue())

				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = bigger
				Expect(k8sClient.Update(ctx, cr)).Should(Succeed())

				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

				// it must not be asked to shrink, which the API would refuse
				Expect(k8sClient.Get(ctx, first, p)).Should(Succeed())
				requested := p.Spec.Resources.Requests[corev1.ResourceStorage]
				Expect(requested.Cmp(biggest)).To(Equal(0), "a bigger claim must be left alone")
			})

			It("should finish once the smaller claims caught up", func() {
				for i := 1; i < int(cr.Spec.MySQL.Size); i++ {
					p := &corev1.PersistentVolumeClaim{}
					key := types.NamespacedName{Name: fmt.Sprintf("datadir-%s-%d", sts.Name, i), Namespace: ns}
					Eventually(func() bool {
						if err := k8sClient.Get(ctx, key, p); err != nil {
							return false
						}
						p.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: bigger}
						p.Status.AllocatedResources = nil
						p.Status.AllocatedResourceStatuses = nil
						return k8sClient.Status().Update(ctx, p) == nil
					}, time.Second*10, time.Millisecond*100).Should(BeTrue())
				}

				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				Expect(reconciler().reconcilePersistentVolumes(ctx, cr)).Should(Succeed())

				Expect(k8sClient.Get(ctx, crNamespacedName, cr)).Should(Succeed())
				_, exists := cr.GetAnnotations()[string(naming.AnnotationPVCResizeInProgress)]
				Expect(exists).To(BeFalse(), "a claim that needs no expansion must not hold the resize open")
			})
		})
	})
})
