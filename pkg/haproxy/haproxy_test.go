package haproxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

func TestStatefulset(t *testing.T) {
	const ns = "haproxy-ns"
	const initImage = "init-image"
	const configHash = "config-hash"
	const tlsHash = "tls-hash"

	cr := readDefaultCluster(t, "cluster", ns)
	if err := cr.CheckNSetDefaults(t.Context(), &platform.ServerVersion{
		Platform: platform.PlatformKubernetes,
	}); err != nil {
		t.Fatal(err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "some-secret",
			Namespace: ns,
		},
		StringData: map[string]string{},
	}

	t.Run("object meta", func(t *testing.T) {
		cluster := cr.DeepCopy()

		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)

		assert.NotNil(t, sts)
		assert.Equal(t, "cluster-haproxy", sts.Name)
		assert.Equal(t, "haproxy-ns", sts.Namespace)
		labels := map[string]string{
			"app.kubernetes.io/name":       "haproxy",
			"app.kubernetes.io/part-of":    "percona-server",
			"app.kubernetes.io/instance":   "cluster",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/component":  "proxy",
		}
		assert.Equal(t, labels, sts.Labels)
	})

	t.Run("defaults", func(t *testing.T) {
		cluster := cr.DeepCopy()

		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)

		assert.Equal(t, int32(3), *sts.Spec.Replicas)
		initContainers := sts.Spec.Template.Spec.InitContainers
		assert.Len(t, initContainers, 1)
		assert.Equal(t, initImage, initContainers[0].Image)

		assert.Equal(t, map[string]string{
			"percona.com/last-applied-tls":   tlsHash,
			"percona.com/configuration-hash": configHash,
		}, sts.Spec.Template.Annotations)
	})

	t.Run("termination grace period seconds", func(t *testing.T) {
		cluster := cr.DeepCopy()

		cluster.Spec.Proxy.HAProxy.TerminationGracePeriodSeconds = nil
		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, int64(600), *sts.Spec.Template.Spec.TerminationGracePeriodSeconds)

		cluster.Spec.Proxy.HAProxy.TerminationGracePeriodSeconds = ptr.To(int64(30))

		sts = StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, int64(30), *sts.Spec.Template.Spec.TerminationGracePeriodSeconds)
	})

	t.Run("image pull secrets", func(t *testing.T) {
		cluster := cr.DeepCopy()

		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, []corev1.LocalObjectReference(nil), sts.Spec.Template.Spec.ImagePullSecrets)

		imagePullSecrets := []corev1.LocalObjectReference{
			{
				Name: "secret-1",
			},
			{
				Name: "secret-2",
			},
		}
		cluster.Spec.Proxy.HAProxy.ImagePullSecrets = imagePullSecrets

		sts = StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, imagePullSecrets, sts.Spec.Template.Spec.ImagePullSecrets)
	})

	t.Run("runtime class name", func(t *testing.T) {
		cluster := cr.DeepCopy()
		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		var e *string
		assert.Equal(t, e, sts.Spec.Template.Spec.RuntimeClassName)

		const runtimeClassName = "runtimeClassName"
		cluster.Spec.Proxy.HAProxy.RuntimeClassName = ptr.To(runtimeClassName)

		sts = StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, runtimeClassName, *sts.Spec.Template.Spec.RuntimeClassName)
	})

	t.Run("tolerations", func(t *testing.T) {
		cluster := cr.DeepCopy()
		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, []corev1.Toleration(nil), sts.Spec.Template.Spec.Tolerations)

		tolerations := []corev1.Toleration{
			{
				Key:               "node.alpha.kubernetes.io/unreachable",
				Operator:          "Exists",
				Value:             "value",
				Effect:            "NoExecute",
				TolerationSeconds: ptr.To(int64(1001)),
			},
		}
		cluster.Spec.Proxy.HAProxy.Tolerations = tolerations

		sts = StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, tolerations, sts.Spec.Template.Spec.Tolerations)
	})

	t.Run("probes", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cluster.Spec.CRVersion = "0.12.0"
		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		var hContainer *corev1.Container
		for _, c := range sts.Spec.Template.Spec.Containers {
			if c.Name == AppName {
				hContainer = &c
			}
		}
		if hContainer == nil {
			t.Fatal("haproxy container is not found")
		}

		expectedReadinessProbe := corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{Command: []string{"/opt/percona/haproxy_readiness_check.sh"}},
			},
			InitialDelaySeconds: 15,
			TimeoutSeconds:      1,
			PeriodSeconds:       5,
			SuccessThreshold:    1,
			FailureThreshold:    3,
		}
		expectedLivenessProbe := corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{Command: []string{"/opt/percona/haproxy_liveness_check.sh"}},
			},
			InitialDelaySeconds: 60,
			TimeoutSeconds:      3,
			PeriodSeconds:       30,
			SuccessThreshold:    1,
			FailureThreshold:    4,
		}

		assert.Equal(t, expectedReadinessProbe, *hContainer.ReadinessProbe)
		assert.Equal(t, expectedLivenessProbe, *hContainer.LivenessProbe)

		cluster.Spec.Proxy.HAProxy.ReadinessProbe = corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"invalid-command"},
				},
			},
			InitialDelaySeconds:           10,
			TimeoutSeconds:                20,
			PeriodSeconds:                 30,
			SuccessThreshold:              40,
			FailureThreshold:              50,
			TerminationGracePeriodSeconds: new(int64),
		}
		cluster.Spec.Proxy.HAProxy.LivenessProbe = corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"invalid-command"},
				},
			},
			InitialDelaySeconds:           11,
			TimeoutSeconds:                21,
			PeriodSeconds:                 31,
			SuccessThreshold:              41,
			FailureThreshold:              51,
			TerminationGracePeriodSeconds: new(int64),
		}
		sts = StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		for _, c := range sts.Spec.Template.Spec.Containers {
			if c.Name == AppName {
				hContainer = &c
			}
		}
		if hContainer == nil {
			t.Fatal("haproxy container is not found")
		}

		expectedReadinessProbe = corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{Command: []string{"/opt/percona/haproxy_readiness_check.sh"}},
			},
			InitialDelaySeconds:           10,
			TimeoutSeconds:                20,
			PeriodSeconds:                 30,
			SuccessThreshold:              40,
			FailureThreshold:              50,
			TerminationGracePeriodSeconds: new(int64),
		}
		expectedLivenessProbe = corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{Command: []string{"/opt/percona/haproxy_liveness_check.sh"}},
			},
			InitialDelaySeconds:           11,
			TimeoutSeconds:                21,
			PeriodSeconds:                 31,
			SuccessThreshold:              41,
			FailureThreshold:              51,
			TerminationGracePeriodSeconds: new(int64),
		}

		assert.Equal(t, expectedReadinessProbe, *hContainer.ReadinessProbe)
		assert.Equal(t, expectedLivenessProbe, *hContainer.LivenessProbe)
	})
}

func TestService(t *testing.T) {
	podName := "test-cluster-haproxy"

	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			Proxy: apiv1alpha1.ProxySpec{
				HAProxy: &apiv1alpha1.HAProxySpec{
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
			cr.Spec.Proxy.HAProxy.Expose.Type = tt.serviceType

			service := Service(cr, nil)

			assert.Equal(t, "v1", service.APIVersion)
			assert.Equal(t, "Service", service.Kind)
			assert.Equal(t, podName, service.Name)
			assert.Equal(t, "test-namespace", service.Namespace)

			assert.Equal(t, tt.serviceType, service.Spec.Type)

			expectedLabels := Labels(cr)
			expectedLabels["custom-label"] = "custom-value"
			assert.Equal(t, expectedLabels, service.Labels)

			expectedSelector := MatchLabels(cr)
			assert.Equal(t, expectedSelector, service.Spec.Selector)

			assert.Equal(t, cr.Spec.Proxy.HAProxy.Expose.Annotations, service.Annotations)

			if tt.expectLoadBalancer {
				assert.Equal(t, cr.Spec.Proxy.HAProxy.Expose.LoadBalancerSourceRanges, service.Spec.LoadBalancerSourceRanges)
			} else {
				assert.Empty(t, service.Spec.LoadBalancerSourceRanges)
			}

			if tt.expectExternalTrafficPolicy {
				assert.Equal(t, cr.Spec.Proxy.HAProxy.Expose.ExternalTrafficPolicy, service.Spec.ExternalTrafficPolicy)
			} else {
				assert.Empty(t, service.Spec.ExternalTrafficPolicy)
			}

			assert.Equal(t, cr.Spec.Proxy.HAProxy.Expose.InternalTrafficPolicy, service.Spec.InternalTrafficPolicy)
		})
	}
}
