package ps

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/gocarina/gocsv"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/haproxy"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/router"
)

func TestReconcileStatusAsync(t *testing.T) {
	ctx := context.Background()

	cr, err := readDefaultCR("cluster1", "status-1")
	if err != nil {
		t.Fatal(err)
	}
	cr.Spec.MySQL.ClusterType = apiv1alpha1.ClusterTypeAsync
	cr.Spec.UpdateStrategy = appsv1.OnDeleteStatefulSetStrategyType

	scheme := runtime.NewScheme()
	err = clientgoscheme.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}
	err = apiv1alpha1.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}

	boolTrue := true

	tests := []struct {
		name     string
		cr       *apiv1alpha1.PerconaServerMySQL
		objects  []client.Object
		expected apiv1alpha1.PerconaServerMySQLStatus
	}{
		{
			name:    "without pods",
			cr:      cr,
			objects: []client.Object{},
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					State: apiv1alpha1.StateInitializing,
				},
				Orchestrator: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					State: apiv1alpha1.StateInitializing,
				},
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					State: apiv1alpha1.StateInitializing,
				},
				State: apiv1alpha1.StateInitializing,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateInitializing.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateInitializing.String(),
					},
				},
			},
		},
		{
			name:    "with 3 ready mysql pods",
			cr:      cr,
			objects: makeFakeReadyPods(cr, 3, "mysql"),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				Orchestrator: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					State: apiv1alpha1.StateInitializing,
				},
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					State: apiv1alpha1.StateInitializing,
				},
				State: apiv1alpha1.StateInitializing,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateInitializing.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateInitializing.String(),
					},
				},
			},
		},
		{
			name: "with all ready pods",
			cr:   cr,
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "haproxy"),
				makeFakeReadyPods(cr, 3, "orchestrator"),
			),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				Orchestrator: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateReady,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateReady.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateReady.String(),
					},
				},
			},
		},
		{
			name: "with all ready pods and not ready cluster's loadbalancer",
			cr:   cr,
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "haproxy"),
				makeFakeReadyPods(cr, 3, "orchestrator"),
				[]client.Object{
					&corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "cluster-service",
							Namespace: cr.Namespace,
							Labels:    cr.Labels(),
							OwnerReferences: []metav1.OwnerReference{
								{
									Name:       cr.GetName(),
									UID:        cr.GetUID(),
									Controller: &boolTrue,
								},
							},
						},
						Spec: corev1.ServiceSpec{
							Type: corev1.ServiceTypeLoadBalancer,
						},
					},
				}),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				Orchestrator: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateInitializing,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateInitializing.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateInitializing.String(),
					},
				},
			},
		},
		{
			name: "with all ready pods and not ready custom loadbalancer",
			cr:   cr,
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "haproxy"),
				makeFakeReadyPods(cr, 3, "orchestrator"),
				[]client.Object{
					// cr has no LoadBalancer service provided.
					// Let's pretend it is a LoadBalancer created by user in the same namespace.
					&corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "idk-service",
							Namespace: cr.Namespace,
						},
						Spec: corev1.ServiceSpec{
							Type: corev1.ServiceTypeLoadBalancer,
						},
					},
				}),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				Orchestrator: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateReady,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateReady.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateReady.String(),
					},
				},
			},
		},
		{
			name: "with all ready pods without orchestrator",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQL) {
				cr.Spec.Orchestrator.Enabled = false
				cr.Spec.AllowUnsafeConfig = true
				cr.Spec.Proxy.HAProxy.Enabled = true
				cr.Spec.MySQL.Size = 1
				cr.Generation = 1
			}),
			objects: appendSlices(
				makeFakeReadyPods(cr, 1, "mysql"),
				makeFakeReadyPods(cr, 3, "haproxy"),
			),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  1,
					Ready: 1,
					State: apiv1alpha1.StateReady,
				},
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateReady,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateReady.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateReady.String(),
					},
				},
			},
		},
		{
			name: "with all ready pods without haproxy",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQL) {
				cr.Spec.Orchestrator.Enabled = true
				cr.Spec.AllowUnsafeConfig = true
				cr.Spec.Proxy.HAProxy.Enabled = false
			}),
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "orchestrator"),
			),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				Orchestrator: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateReady,
				Host:  cr.Name + "-mysql." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateReady.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateReady.String(),
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := tt.cr.DeepCopy()
			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).WithStatusSubresource(cr).WithObjects(tt.objects...).WithStatusSubresource(tt.objects...)

			r := &PerconaServerMySQLReconciler{
				Client: cb.Build(),
				Scheme: scheme,
				ServerVersion: &platform.ServerVersion{
					Platform: platform.PlatformKubernetes,
				},
			}
			err := r.reconcileCRStatus(ctx, cr, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, cr); err != nil {
				t.Fatal(err)
			}

			opt := cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message")
			if diff := cmp.Diff(cr.Status, tt.expected, opt); diff != "" {
				t.Errorf("expected status %v, got %v, diff: %s", tt.expected, cr.Status, diff)
			}
		})
	}
}

func TestReconcileStatusHAProxyGR(t *testing.T) {
	ctx := context.Background()

	cr, err := readDefaultCR("cluster1", "status-1")
	if err != nil {
		t.Fatal(err)
	}
	cr.Spec.MySQL.ClusterType = apiv1alpha1.ClusterTypeGR
	cr.Spec.Proxy.HAProxy.Enabled = true
	cr.Spec.Proxy.Router.Enabled = false

	scheme := runtime.NewScheme()
	err = clientgoscheme.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}
	err = apiv1alpha1.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}

	const operatorPass = "test"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.InternalSecretName(),
			Namespace: cr.Namespace,
		},
		Data: map[string][]byte{
			string(apiv1alpha1.UserOperator): []byte(operatorPass),
		},
	}

	tests := []struct {
		name              string
		cr                *apiv1alpha1.PerconaServerMySQL
		objects           []client.Object
		expected          apiv1alpha1.PerconaServerMySQLStatus
		mysqlMemberStates []innodbcluster.MemberState
		noMetadataDB      bool
	}{
		{
			name: "without pods",
			cr:   cr,
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					State: apiv1alpha1.StateInitializing,
				},
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					State: apiv1alpha1.StateInitializing,
				},
				State: apiv1alpha1.StateInitializing,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateInitializing.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateInitializing.String(),
					},
				},
			},
		},
		{
			name: "with all ready pods and ok cluster status",
			cr:   cr,
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "haproxy"),
				[]client.Object{secret},
			),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateReady,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateReady.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateReady.String(),
					},
				},
			},
			mysqlMemberStates: []innodbcluster.MemberState{
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
			},
		},
		{
			name: "with all ready pods, ok cluster status and invalid database",
			cr:   cr,
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "haproxy"),
				[]client.Object{secret},
			),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateInitializing,
				},
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateInitializing,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateInitializing.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateInitializing.String(),
					},
				},
			},
			mysqlMemberStates: []innodbcluster.MemberState{
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
			},
			noMetadataDB: true,
		},
		{
			name: "with all ready pods and offline cluster status",
			cr:   cr,
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "haproxy"),
				[]client.Object{secret},
			),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateInitializing,
				},
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateInitializing,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateInitializing.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateInitializing.String(),
					},
				},
			},
			mysqlMemberStates: []innodbcluster.MemberState{
				innodbcluster.MemberStateOffline,
				innodbcluster.MemberStateOffline,
				innodbcluster.MemberStateOffline,
			},
		},
		{
			name: "with all ready pods and partial ok cluster status",
			cr:   cr,
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "haproxy"),
				[]client.Object{secret},
			),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateInitializing,
				},
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateInitializing,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateInitializing.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateInitializing.String(),
					},
				},
			},
			mysqlMemberStates: []innodbcluster.MemberState{
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOffline,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := tt.cr.DeepCopy()
			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).WithStatusSubresource(cr).WithObjects(tt.objects...).WithStatusSubresource(tt.objects...)
			cliCmd, err := getFakeClient(cr, tt.mysqlMemberStates, tt.noMetadataDB)
			if err != nil {
				t.Fatal(err)
			}
			r := &PerconaServerMySQLReconciler{
				Client: cb.Build(),
				Scheme: scheme,
				ServerVersion: &platform.ServerVersion{
					Platform: platform.PlatformKubernetes,
				},
				ClientCmd: cliCmd,
				Recorder:  new(record.FakeRecorder),
			}

			err = r.reconcileCRStatus(ctx, cr, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, cr); err != nil {
				t.Fatal(err)
			}

			opt := cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message")
			if diff := cmp.Diff(cr.Status, tt.expected, opt); diff != "" {
				t.Errorf("expected status %v, got %v, diff: %s", tt.expected, cr.Status, diff)
			}
		})
	}
}

func TestReconcileStatusRouterGR(t *testing.T) {
	ctx := context.Background()

	cr, err := readDefaultCR("cluster1", "status-1")
	if err != nil {
		t.Fatal(err)
	}
	cr.Spec.MySQL.ClusterType = apiv1alpha1.ClusterTypeGR
	cr.Spec.Proxy.HAProxy.Enabled = false
	cr.Spec.Proxy.Router.Enabled = true

	scheme := runtime.NewScheme()
	err = clientgoscheme.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}
	err = apiv1alpha1.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}

	const operatorPass = "test"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.InternalSecretName(),
			Namespace: cr.Namespace,
		},
		Data: map[string][]byte{
			string(apiv1alpha1.UserOperator): []byte(operatorPass),
		},
	}

	tests := []struct {
		name              string
		cr                *apiv1alpha1.PerconaServerMySQL
		objects           []client.Object
		expected          apiv1alpha1.PerconaServerMySQLStatus
		mysqlMemberStates []innodbcluster.MemberState
		noMetadataDB      bool
	}{
		{
			name: "without pods",
			cr:   cr,
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					State: apiv1alpha1.StateInitializing,
				},
				Router: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					State: apiv1alpha1.StateInitializing,
				},
				State: apiv1alpha1.StateInitializing,
				Host:  cr.Name + "-router." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateInitializing.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateInitializing.String(),
					},
				},
			},
		},
		{
			name: "with all ready pods and ok cluster status",
			cr:   cr,
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "router"),
				[]client.Object{secret},
			),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				Router: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateReady,
				Host:  cr.Name + "-router." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateReady.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateReady.String(),
					},
				},
			},
			mysqlMemberStates: []innodbcluster.MemberState{
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
			},
		},
		{
			name: "with all ready pods, ok cluster status and invalid databaes",
			cr:   cr,
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "router"),
				[]client.Object{secret},
			),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateInitializing,
				},
				Router: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateInitializing,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateInitializing.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateInitializing.String(),
					},
				},
				Host: cr.Name + "-router." + cr.Namespace,
			},
			mysqlMemberStates: []innodbcluster.MemberState{
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
			},
			noMetadataDB: true,
		},
		{
			name: "with all ready pods and offline cluster status",
			cr:   cr,
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "router"),
				[]client.Object{secret},
			),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateInitializing,
				},
				Router: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateInitializing,
				Host:  cr.Name + "-router." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateInitializing.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateInitializing.String(),
					},
				},
			},
			mysqlMemberStates: []innodbcluster.MemberState{
				innodbcluster.MemberStateOffline,
				innodbcluster.MemberStateOffline,
				innodbcluster.MemberStateOffline,
			},
		},
		{
			name: "with all ready pods and partial ok cluster status",
			cr:   cr,
			objects: appendSlices(
				makeFakeReadyPods(cr, 3, "mysql"),
				makeFakeReadyPods(cr, 3, "router"),
				[]client.Object{secret},
			),
			expected: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateInitializing,
				},
				Router: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateInitializing,
				Host:  cr.Name + "-router." + cr.Namespace,
				Conditions: []metav1.Condition{
					{
						Type:   apiv1alpha1.StateInitializing.String(),
						Status: metav1.ConditionTrue,
						Reason: apiv1alpha1.StateInitializing.String(),
					},
				},
			},
			mysqlMemberStates: []innodbcluster.MemberState{
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOnline,
				innodbcluster.MemberStateOffline,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := tt.cr.DeepCopy()
			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).WithStatusSubresource(cr).WithObjects(tt.objects...).WithStatusSubresource(tt.objects...)
			cliCmd, err := getFakeClient(cr, tt.mysqlMemberStates, tt.noMetadataDB)
			if err != nil {
				t.Fatal(err)
			}
			r := &PerconaServerMySQLReconciler{
				Client: cb.Build(),
				Scheme: scheme,
				ServerVersion: &platform.ServerVersion{
					Platform: platform.PlatformKubernetes,
				},
				ClientCmd: cliCmd,
				Recorder:  new(record.FakeRecorder),
			}

			err = r.reconcileCRStatus(ctx, cr, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, cr); err != nil {
				t.Fatal(err)
			}

			opt := cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message")
			if diff := cmp.Diff(cr.Status, tt.expected, opt); diff != "" {
				t.Errorf("expected status %v, got %v, diff: %s", tt.expected, cr.Status, diff)
			}
		})
	}
}

func TestReconcileErrorStatus(t *testing.T) {
	ctx := context.Background()

	cr, err := readDefaultCR("cluster1", "status-err")
	if err != nil {
		t.Fatal(err)
	}
	cr.Spec.MySQL.ClusterType = apiv1alpha1.ClusterTypeAsync
	cr.Spec.UpdateStrategy = appsv1.OnDeleteStatefulSetStrategyType

	scheme := runtime.NewScheme()
	err = clientgoscheme.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}
	err = apiv1alpha1.AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}

	objects := appendSlices(
		makeFakeReadyPods(cr, 3, "mysql"),
		makeFakeReadyPods(cr, 3, "haproxy"),
		makeFakeReadyPods(cr, 3, "orchestrator"),
	)

	cb := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cr).
		WithStatusSubresource(cr).
		WithObjects(objects...).
		WithStatusSubresource(objects...)

	r := &PerconaServerMySQLReconciler{
		Client: cb.Build(),
		Scheme: scheme,
		ServerVersion: &platform.ServerVersion{
			Platform: platform.PlatformKubernetes,
		},
	}

	reconcileErr := errors.New("reconcile error")

	err = r.reconcileCRStatus(ctx, cr, reconcileErr)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, cr); err != nil {
		t.Fatal(err)
	}

	expected := apiv1alpha1.PerconaServerMySQLStatus{
		State: apiv1alpha1.StateError,
		Conditions: []metav1.Condition{
			{
				Type:    apiv1alpha1.StateError.String(),
				Status:  metav1.ConditionTrue,
				Reason:  "ErrorReconcile",
				Message: reconcileErr.Error(),
			},
		},
	}

	opt := cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")
	if diff := cmp.Diff(cr.Status.Conditions, expected.Conditions, opt); diff != "" {
		t.Errorf("unexpected conditions (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff(cr.Status.State, expected.State); diff != "" {
		t.Errorf("unexpected state (-want +got):\n%s", diff)
	}
}

type fakeClient struct {
	scripts   []fakeClientScript
	execCount int
}

// Exec increments the internal counter `execCount`.
// It compares the `scripts[execCount].cmd`, `scripts[execCount].stdin` with `command` and stdin parameters.
// It writes the `scripts[execCount].stdout`, `scripts[execCount].stderr` to `stdin` and `stderr` parameters.
// It writes the `scripts[execCount].stdout`, `scripts[execCount].stderr` to `stdin` and `stderr` parameters.
// fakeClient should have the array of fakeClientScript objects in order they are going to be executed in the tested function.
func (c *fakeClient) Exec(_ context.Context, _ *corev1.Pod, _ string, command []string, stdin io.Reader, stdout, stderr io.Writer, _ bool) error {
	if c.execCount >= len(c.scripts) {
		return errors.Errorf("unexpected exec call")
	}
	if !reflect.DeepEqual(c.scripts[c.execCount].cmd, command) {
		return errors.Errorf("expected command: %v; got %v", c.scripts[c.execCount].cmd, command)
	}
	var in []byte
	var err error
	if stdin != nil {
		in, err = io.ReadAll(stdin)
		if err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(in, c.scripts[c.execCount].stdin) {
		return errors.Errorf("expected stdin: %v; got %v", c.scripts[c.execCount].stdin, in)
	}
	_, err = stdout.Write(c.scripts[c.execCount].stdout)
	if err != nil {
		return err
	}
	_, err = stderr.Write(c.scripts[c.execCount].stderr)
	if err != nil {
		return err
	}
	c.execCount++
	if c.scripts[c.execCount-1].err != nil {
		return c.scripts[c.execCount-1].err
	}
	return nil
}

func (c *fakeClient) REST() restclient.Interface {
	return nil
}

// fakeClientScript is an object which contains an info about executed command.
// cmd, stdin values are compared with the corresponding values in the Exec method.
// stdin, stdout values are written to the corresponding streams in the Exec method.
// err is the error which should be returned in the Exec method
type fakeClientScript struct {
	cmd    []string
	stdin  []byte
	stdout []byte
	stderr []byte
	err    error
}

// getFakeClient returns a fake clientcmd.Client object with the array of fakeClientScript objects.
// This array is constructed to cover every possible client call in the reconcileCRStatus function.
func getFakeClient(cr *apiv1alpha1.PerconaServerMySQL, mysqlMemberStates []innodbcluster.MemberState, noMetadataDB bool) (clientcmd.Client, error) {
	queryScript := func(query string, out any) fakeClientScript {
		buf := new(bytes.Buffer)
		w := csv.NewWriter(buf)
		w.Comma = '\t'
		if err := gocsv.MarshalCSV(out, w); err != nil {
			panic(err)
		}
		host := fmt.Sprintf("%s.%s.%s", mysql.PodName(cr, 0), mysql.ServiceName(cr), cr.Namespace)

		return fakeClientScript{
			cmd: []string{
				"mysql",
				"--database",
				"performance_schema",
				"-ptest",
				"-u",
				"operator",
				"-h",
				host,
				"-e",
				query,
			},
			stdout: buf.Bytes(),
		}
	}

	var scripts []fakeClientScript

	// CheckIfDatabaseExists
	type db struct {
		DB string `csv:"db"`
	}
	dbs := []*db{
		{
			DB: "mysql_innodb_cluster_metadata",
		},
	}
	s := queryScript("SELECT SCHEMA_NAME AS db FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME LIKE 'mysql_innodb_cluster_metadata'", dbs)
	if !noMetadataDB {
	} else {
		s.err = sql.ErrNoRows
	}
	scripts = append(scripts, s)

	// GetGroupReplicationMembers
	if !noMetadataDB {
		type member struct {
			Member string `csv:"member"`
			State  string `csv:"state"`
		}
		var members []*member
		for _, state := range mysqlMemberStates {
			members = append(members, &member{
				Member: cr.Name + "-mysql-0." + cr.Namespace,
				State:  string(state),
			})
		}
		scripts = append(scripts, queryScript("SELECT MEMBER_HOST as member, MEMBER_STATE as state FROM replication_group_members", members))
	}

	scripts = append(scripts, fakeClientScript{
		cmd: []string{
			"cat",
			"/var/lib/mysql/full-cluster-crash",
		},
		stderr: []byte("No such file or directory"),
		err:    errors.New("fake error"),
	})
	return &fakeClient{
		scripts: scripts,
	}, nil
}

func makeFakeReadyPods(cr *apiv1alpha1.PerconaServerMySQL, amount int, podType string) []client.Object {
	fakePod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "mysql",
					Image:   "fake-image",
					Command: []string{"sh"},
				},
			},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.ContainersReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	pods := make([]client.Object, 0, amount)
	for i := 0; i < amount; i++ {
		pod := fakePod.DeepCopy()
		switch podType {
		case "mysql":
			pod.Name = mysql.PodName(cr, i)
			pod.Labels = mysql.MatchLabels(cr)
		case "orchestrator":
			pod.Name = orchestrator.PodName(cr, i)
			pod.Labels = orchestrator.MatchLabels(cr)
		case "haproxy":
			pod.Name = haproxy.PodName(cr, i)
			pod.Labels = haproxy.MatchLabels(cr)
		case "router":
			pod.Name = router.PodName(cr, i)
			pod.Labels = router.MatchLabels(cr)
		}
		pod.Namespace = cr.Namespace
		pods = append(pods, pod)
	}
	return pods
}

func updateResource[T any](obj *T, updateFuncs ...func(obj *T)) *T {
	for _, f := range updateFuncs {
		f(obj)
	}
	return obj
}

func appendSlices[T any](s ...[]T) []T {
	var result []T
	for _, v := range s {
		result = append(result, v...)
	}
	return result
}
