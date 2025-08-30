package router

import (
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

func TestDeployment(t *testing.T) {
	const ns = "router-ns"

	const initImage = "init-image"
	const configHash = "config-hash"
	const tlsHash = "config-hash"

	cr := readDefaultCluster(t, "cluster", ns)
	if err := cr.CheckNSetDefaults(t.Context(), &platform.ServerVersion{
		Platform: platform.PlatformKubernetes,
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("object meta", func(t *testing.T) {
		cluster := cr.DeepCopy()

		deployment := Deployment(cluster, initImage, configHash, tlsHash)

		assert.NotNil(t, deployment)
		assert.Equal(t, "cluster-router", deployment.Name)
		assert.Equal(t, "router-ns", deployment.Namespace)
		labels := map[string]string{
			"app.kubernetes.io/name":       "router",
			"app.kubernetes.io/part-of":    "percona-server",
			"app.kubernetes.io/instance":   "cluster",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/component":  "proxy",
		}
		assert.Equal(t, labels, deployment.Labels)
	})

	t.Run("defaults", func(t *testing.T) {
		cluster := cr.DeepCopy()

		deployment := Deployment(cluster, initImage, configHash, tlsHash)

		assert.Equal(t, int32(3), *deployment.Spec.Replicas)
		initContainers := deployment.Spec.Template.Spec.InitContainers
		assert.Len(t, initContainers, 1)
		assert.Equal(t, initImage, initContainers[0].Image)

		assert.Equal(t, map[string]string{
			"percona.com/last-applied-tls":   tlsHash,
			"percona.com/configuration-hash": configHash,
		}, deployment.Spec.Template.Annotations)
	})

	t.Run("image pull secrets", func(t *testing.T) {
		cluster := cr.DeepCopy()

		deployment := Deployment(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, []corev1.LocalObjectReference(nil), deployment.Spec.Template.Spec.ImagePullSecrets)

		imagePullSecrets := []corev1.LocalObjectReference{
			{
				Name: "secret-1",
			},
			{
				Name: "secret-2",
			},
		}
		cluster.Spec.Proxy.Router.ImagePullSecrets = imagePullSecrets

		deployment = Deployment(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, imagePullSecrets, deployment.Spec.Template.Spec.ImagePullSecrets)
	})

	t.Run("runtime class name", func(t *testing.T) {
		cluster := cr.DeepCopy()
		deployment := Deployment(cluster, initImage, configHash, tlsHash)
		var e *string
		assert.Equal(t, e, deployment.Spec.Template.Spec.RuntimeClassName)

		const runtimeClassName = "runtimeClassName"
		cluster.Spec.Proxy.Router.RuntimeClassName = ptr.To(runtimeClassName)

		deployment = Deployment(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, runtimeClassName, *deployment.Spec.Template.Spec.RuntimeClassName)
	})

	t.Run("tolerations", func(t *testing.T) {
		cluster := cr.DeepCopy()
		deployment := Deployment(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, []corev1.Toleration(nil), deployment.Spec.Template.Spec.Tolerations)

		tolerations := []corev1.Toleration{
			{
				Key:               "node.alpha.kubernetes.io/unreachable",
				Operator:          "Exideployment",
				Value:             "value",
				Effect:            "NoExecute",
				TolerationSeconds: ptr.To(int64(1001)),
			},
		}
		cluster.Spec.Proxy.Router.Tolerations = tolerations

		deployment = Deployment(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, tolerations, deployment.Spec.Template.Spec.Tolerations)
	})
}

func TestPorts(t *testing.T) {
	defaultPorts := func() []corev1.ServicePort {
		return []corev1.ServicePort{
			{
				Name: "http",
				Port: 8443,
			},
			{
				Name: "rw-default",
				Port: 3306,
				TargetPort: intstr.IntOrString{
					IntVal: 6446,
				},
			},
			{
				Name: "read-write",
				Port: 6446,
			},
			{
				Name: "read-only",
				Port: 6447,
			},
			{
				Name: "x-read-write",
				Port: 6448,
			},
			{
				Name: "x-read-only",
				Port: 6449,
			},
			{
				Name: "x-default",
				Port: 33060,
			},
			{
				Name: "rw-admin",
				Port: 33062,
			},
		}
	}

	tests := []struct {
		name           string
		specifiedPorts []corev1.ServicePort
		expectedPorts  []corev1.ServicePort
	}{
		{
			name:          "default ports",
			expectedPorts: defaultPorts(),
		},
		{
			name: "additional ports",
			specifiedPorts: []corev1.ServicePort{
				{
					Name: "additional port",
					Port: 4308,
				},
				{
					Name: "additional port with target port",
					Port: 1337,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 20,
					},
				},
			},
			expectedPorts: updateObject(defaultPorts(), func(ports []corev1.ServicePort) []corev1.ServicePort {
				ports = append(ports, []corev1.ServicePort{
					{
						Name: "additional port",
						Port: 4308,
					},
					{
						Name: "additional port with target port",
						Port: 1337,
						TargetPort: intstr.IntOrString{
							Type:   intstr.Int,
							IntVal: 20,
						},
					},
				}...)
				return ports
			}),
		},
		{
			name: "modified ports with additional ports",
			specifiedPorts: []corev1.ServicePort{
				{
					Name: "http",
					Port: 5555,
				},
				{
					Name: "rw-default",
					Port: 6666,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 30,
					},
				},
				{
					Name: "additional port",
					Port: 4308,
				},
				{
					Name: "additional port with target port",
					Port: 1337,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 20,
					},
				},
			},
			expectedPorts: updateObject(defaultPorts(), func(ports []corev1.ServicePort) []corev1.ServicePort {
				for i, v := range ports {
					if v.Name == "http" {
						ports[i].Port = 5555
						continue
					}
					if v.Name == "rw-default" {
						ports[i].Port = 6666
						ports[i].TargetPort = intstr.IntOrString{
							Type:   intstr.Int,
							IntVal: 30,
						}
						continue
					}
				}
				ports = append(ports, []corev1.ServicePort{
					{
						Name: "additional port",
						Port: 4308,
					},
					{
						Name: "additional port with target port",
						Port: 1337,
						TargetPort: intstr.IntOrString{
							Type:   intstr.Int,
							IntVal: 20,
						},
					},
				}...)
				return ports
			}),
		},
		{
			name: "modified port with default targetPort",
			specifiedPorts: []corev1.ServicePort{
				{
					Name: "rw-default",
					Port: 6666,
				},
			},
			expectedPorts: updateObject(defaultPorts(), func(ports []corev1.ServicePort) []corev1.ServicePort {
				for i, v := range ports {
					if v.Name == "rw-default" {
						ports[i].Port = 6666
						break
					}
				}
				return ports
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ports(tt.specifiedPorts)
			if !reflect.DeepEqual(got, tt.expectedPorts) {
				gotBytes, err := yaml.Marshal(got)
				if err != nil {
					t.Fatalf("error marshaling got: %v", err)
				}
				wantBytes, err := yaml.Marshal(tt.expectedPorts)
				if err != nil {
					t.Fatalf("error marshaling want: %v", err)
				}
				t.Fatal(cmp.Diff(string(wantBytes), string(gotBytes)))
			}
		})
	}
}

func TestService(t *testing.T) {
	podName := "test-cluster-router"

	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			Proxy: apiv1alpha1.ProxySpec{
				Router: &apiv1alpha1.MySQLRouterSpec{
					Expose: apiv1alpha1.ServiceExpose{
						Type: corev1.ServiceTypeLoadBalancer,
						Labels: map[string]string{
							"custom-label": "custom-value",
						},
						Annotations: map[string]string{
							"custom-annotation": "custom-annotation-value",
						},
						LoadBalancerSourceRanges: []string{"10.0.0.0/8"},
					},
				},
			},
		},
	}

	tests := map[string]struct {
		serviceType                 corev1.ServiceType
		expectLoadBalancer          bool
		expectExternalTrafficPolicy bool
	}{
		"LoadBalancer service": {
			serviceType:                 corev1.ServiceTypeLoadBalancer,
			expectLoadBalancer:          true,
			expectExternalTrafficPolicy: true,
		},
		"NodePort service": {
			serviceType:                 corev1.ServiceTypeNodePort,
			expectLoadBalancer:          false,
			expectExternalTrafficPolicy: true,
		},
		"ClusterIP service": {
			serviceType:                 corev1.ServiceTypeClusterIP,
			expectLoadBalancer:          false,
			expectExternalTrafficPolicy: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr.Spec.Proxy.Router.Expose.Type = tt.serviceType

			service := Service(cr)

			assert.Equal(t, "v1", service.APIVersion)
			assert.Equal(t, "Service", service.Kind)
			assert.Equal(t, podName, service.Name)
			assert.Equal(t, "test-namespace", service.Namespace)

			assert.Equal(t, tt.serviceType, service.Spec.Type)

			expectedLabels := MatchLabels(cr)
			expectedLabels["custom-label"] = "custom-value"
			assert.Equal(t, expectedLabels, service.Labels)

			expectedSelector := MatchLabels(cr)
			assert.Equal(t, expectedSelector, service.Spec.Selector)

			assert.Equal(t, cr.Spec.Proxy.Router.Expose.Annotations, service.Annotations)

			if tt.expectLoadBalancer {
				assert.Equal(t, cr.Spec.Proxy.Router.Expose.LoadBalancerSourceRanges, service.Spec.LoadBalancerSourceRanges)
			} else {
				assert.Empty(t, service.Spec.LoadBalancerSourceRanges)
			}

			if tt.expectExternalTrafficPolicy {
				assert.Equal(t, cr.Spec.Proxy.Router.Expose.ExternalTrafficPolicy, service.Spec.ExternalTrafficPolicy)
			} else {
				assert.Empty(t, service.Spec.ExternalTrafficPolicy)
			}

			assert.Equal(t, cr.Spec.Proxy.Router.Expose.InternalTrafficPolicy, service.Spec.InternalTrafficPolicy)
		})
	}
}

func updateObject[T any](obj T, updateFuncs ...func(obj T) T) T {
	for _, f := range updateFuncs {
		obj = f(obj)
	}
	return obj
}
