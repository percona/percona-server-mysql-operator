package orchestrator

import (
	"testing"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

func TestStatefulSet(t *testing.T) {
	const ns = "orc-ns"
	const initImage = "init-image"
	const tlsHash = "tls-hash"

	cr := readDefaultCluster(t, "cluster", ns)
	if err := cr.CheckNSetDefaults(t.Context(), &platform.ServerVersion{
		Platform: platform.PlatformKubernetes,
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("object meta", func(t *testing.T) {
		cluster := cr.DeepCopy()

		sts := StatefulSet(cluster, initImage, tlsHash)

		assert.NotNil(t, sts)
		assert.Equal(t, "cluster-orc", sts.Name)
		assert.Equal(t, "orc-ns", sts.Namespace)
		labels := map[string]string{
			"app.kubernetes.io/name":       "orchestrator",
			"app.kubernetes.io/part-of":    "percona-server",
			"app.kubernetes.io/instance":   "cluster",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/component":  "orchestrator",
		}
		assert.Equal(t, labels, sts.Labels)
	})

	t.Run("defaults", func(t *testing.T) {
		cluster := cr.DeepCopy()

		sts := StatefulSet(cluster, initImage, tlsHash)

		assert.Equal(t, int32(3), *sts.Spec.Replicas)
		initContainers := sts.Spec.Template.Spec.InitContainers
		assert.Len(t, initContainers, 1)
		assert.Equal(t, initImage, initContainers[0].Image)

		assert.Equal(t, map[string]string{
			"percona.com/last-applied-tls": tlsHash,
		}, sts.Spec.Template.Annotations)
	})

	t.Run("termination grace period seconds", func(t *testing.T) {
		cluster := cr.DeepCopy()

		cluster.Spec.Orchestrator.TerminationGracePeriodSeconds = nil
		sts := StatefulSet(cluster, initImage, tlsHash)
		assert.Equal(t, int64(600), *sts.Spec.Template.Spec.TerminationGracePeriodSeconds)

		cluster.Spec.Orchestrator.TerminationGracePeriodSeconds = ptr.To(int64(30))

		sts = StatefulSet(cluster, initImage, tlsHash)
		assert.Equal(t, int64(30), *sts.Spec.Template.Spec.TerminationGracePeriodSeconds)
	})

	t.Run("image pull secrets", func(t *testing.T) {
		cluster := cr.DeepCopy()

		sts := StatefulSet(cluster, initImage, tlsHash)
		assert.Equal(t, []corev1.LocalObjectReference(nil), sts.Spec.Template.Spec.ImagePullSecrets)

		imagePullSecrets := []corev1.LocalObjectReference{
			{
				Name: "secret-1",
			},
			{
				Name: "secret-2",
			},
		}
		cluster.Spec.Orchestrator.ImagePullSecrets = imagePullSecrets

		sts = StatefulSet(cluster, initImage, tlsHash)
		assert.Equal(t, imagePullSecrets, sts.Spec.Template.Spec.ImagePullSecrets)
	})

	t.Run("runtime class name", func(t *testing.T) {
		cluster := cr.DeepCopy()
		sts := StatefulSet(cluster, initImage, tlsHash)
		var e *string
		assert.Equal(t, e, sts.Spec.Template.Spec.RuntimeClassName)

		const runtimeClassName = "runtimeClassName"
		cluster.Spec.Orchestrator.RuntimeClassName = ptr.To(runtimeClassName)

		sts = StatefulSet(cluster, initImage, tlsHash)
		assert.Equal(t, runtimeClassName, *sts.Spec.Template.Spec.RuntimeClassName)
	})

	t.Run("tolerations", func(t *testing.T) {
		cluster := cr.DeepCopy()
		sts := StatefulSet(cluster, initImage, tlsHash)
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
		cluster.Spec.Orchestrator.Tolerations = tolerations

		sts = StatefulSet(cluster, initImage, tlsHash)
		assert.Equal(t, tolerations, sts.Spec.Template.Spec.Tolerations)
	})
}

func TestPodService(t *testing.T) {
	podName := "test-pod"

	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			Orchestrator: apiv1alpha1.OrchestratorSpec{
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
			cr.Spec.MySQL.Expose.Type = tt.serviceType

			service := PodService(cr, tt.serviceType, podName)

			assert.Equal(t, "v1", service.APIVersion)
			assert.Equal(t, "Service", service.Kind)
			assert.Equal(t, podName, service.Name)
			assert.Equal(t, "test-namespace", service.Namespace)

			assert.Equal(t, tt.serviceType, service.Spec.Type)

			expectedLabels := MatchLabels(cr)
			expectedLabels["custom-label"] = "custom-value"
			expectedLabels[naming.LabelExposed] = "true"
			assert.Equal(t, expectedLabels, service.Labels)

			expectedSelector := MatchLabels(cr)
			expectedSelector["statefulset.kubernetes.io/pod-name"] = podName
			assert.Equal(t, expectedSelector, service.Spec.Selector)

			assert.Equal(t, cr.Spec.Orchestrator.Expose.Annotations, service.Annotations)

			if tt.expectLoadBalancer {
				assert.Equal(t, cr.Spec.Orchestrator.Expose.LoadBalancerSourceRanges, service.Spec.LoadBalancerSourceRanges)
			} else {
				assert.Empty(t, service.Spec.LoadBalancerSourceRanges)
			}

			if tt.expectExternalTrafficPolicy {
				assert.Equal(t, cr.Spec.Orchestrator.Expose.ExternalTrafficPolicy, service.Spec.ExternalTrafficPolicy)
			} else {
				assert.Empty(t, service.Spec.ExternalTrafficPolicy)
			}

			assert.Equal(t, cr.Spec.Orchestrator.Expose.InternalTrafficPolicy, service.Spec.InternalTrafficPolicy)
		})
	}
}
