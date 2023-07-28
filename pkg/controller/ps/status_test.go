package ps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"testing"

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"github.com/pkg/errors"
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
			},
		},
		{
			name: "with all ready pods and invalid issuer",
			cr: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQL) {
				cr.Spec.TLS = &apiv1alpha1.TLSSpec{
					IssuerConf: &cmmeta.ObjectReference{
						Name: "invalid-issuer",
					},
				}
			}),
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
				State: apiv1alpha1.StateError,
				Host:  cr.Name + "-haproxy." + cr.Namespace,
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
			err := r.reconcileCRStatus(ctx, cr)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, cr); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cr.Status, tt.expected) {
				t.Errorf("expected status %v, got %v", tt.expected, cr.Status)
			}
		})
	}
}
func TestReconcileStatusGR(t *testing.T) {
	ctx := context.Background()

	cr, err := readDefaultCR("cluster1", "status-1")
	if err != nil {
		t.Fatal(err)
	}
	cr.Spec.MySQL.ClusterType = apiv1alpha1.ClusterTypeGR

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
		name          string
		cr            *apiv1alpha1.PerconaServerMySQL
		clusterStatus innodbcluster.ClusterStatus
		objects       []client.Object
		expected      apiv1alpha1.PerconaServerMySQLStatus
		mysqlReady    bool
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
			},
			clusterStatus: innodbcluster.ClusterStatusOK,
			mysqlReady:    true,
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
			},
			clusterStatus: innodbcluster.ClusterStatusOffline,
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
					State: apiv1alpha1.StateReady,
				},
				Router: apiv1alpha1.StatefulAppStatus{
					Size:  3,
					Ready: 3,
					State: apiv1alpha1.StateReady,
				},
				State: apiv1alpha1.StateReady,
				Host:  cr.Name + "-router." + cr.Namespace,
			},
			clusterStatus: innodbcluster.ClusterStatusOKPartial,
			mysqlReady:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := tt.cr.DeepCopy()
			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).WithStatusSubresource(cr).WithObjects(tt.objects...).WithStatusSubresource(tt.objects...)
			cliCmd, err := getFakeClient(cr, operatorPass, tt.mysqlReady, tt.clusterStatus)
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

			err = r.reconcileCRStatus(ctx, cr)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, cr); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cr.Status, tt.expected) {
				t.Errorf("expected status %v, got %v", tt.expected, cr.Status)
			}
		})
	}
}

type fakeClient struct {
	scripts   []fakeClientScript
	execCount int
}

func (c *fakeClient) Exec(ctx context.Context, pod *corev1.Pod, containerName string, command []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
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
	if c.scripts[c.execCount-1].shouldErr {
		return errors.Errorf("fake error")
	}
	return nil
}

func (c *fakeClient) REST() restclient.Interface {
	return nil
}

type fakeClientScript struct {
	cmd       []string
	stdin     []byte
	stdout    []byte
	stderr    []byte
	shouldErr bool
}

func getFakeClient(cr *apiv1alpha1.PerconaServerMySQL, operatorPass string, mysqlReady bool, clusterStatus innodbcluster.ClusterStatus) (clientcmd.Client, error) {
	status, err := json.Marshal(innodbcluster.Status{
		DefaultReplicaSet: innodbcluster.ReplicaSetStatus{
			Status: clusterStatus,
		},
	})
	if err != nil {
		return nil, err
	}
	var scripts []fakeClientScript
	if mysqlReady {
		host := fmt.Sprintf("%s.%s.%s", mysql.PodName(cr, 0), mysql.ServiceName(cr), cr.Namespace)
		scripts = append(scripts, []fakeClientScript{
			{
				cmd: []string{
					"mysqlsh",
					"--no-wizard",
					"--uri",
					fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, host),
					"-e",
					fmt.Sprintf("dba.getCluster('%s').status()", cr.InnoDBClusterName()),
				},
			},
			{
				cmd: []string{
					"mysqlsh",
					"--result-format",
					"json",
					"--uri",
					fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, host),
					"--cluster",
					"--",
					"cluster",
					"status",
				},
				stdout: status,
			}}...)
	}
	scripts = append(scripts, []fakeClientScript{
		{
			cmd: []string{
				"cat",
				"/var/lib/mysql/full-cluster-crash",
			},
			stderr:    []byte("No such file or directory"),
			shouldErr: true,
		},
		{
			cmd: []string{
				"cat",
				"/var/lib/mysql/full-cluster-crash",
			},
			stderr:    []byte("No such file or directory"),
			shouldErr: true,
		},
		{
			cmd: []string{
				"cat",
				"/var/lib/mysql/full-cluster-crash",
			},
			stderr:    []byte("No such file or directory"),
			shouldErr: true,
		},
	}...)
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
