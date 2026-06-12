package haproxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
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
		assert.Empty(t, sts.Spec.Template.Spec.RuntimeClassName)

		const runtimeClassName = "runtimeClassName"
		cluster.Spec.Proxy.HAProxy.RuntimeClassName = ptr.To(runtimeClassName)

		sts = StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, runtimeClassName, *sts.Spec.Template.Spec.RuntimeClassName)
	})

	t.Run("service account name", func(t *testing.T) {
		cluster := cr.DeepCopy()
		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Empty(t, sts.Spec.Template.Spec.ServiceAccountName)

		const serviceAccountName = "service"
		cluster.Spec.Proxy.HAProxy.ServiceAccountName = serviceAccountName

		sts = StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, serviceAccountName, sts.Spec.Template.Spec.ServiceAccountName)
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

	t.Run("annotations", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cluster.Spec.Proxy.HAProxy.Annotations = map[string]string{
			"haproxy-annotation": "haproxy-annotation",
		}

		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, "haproxy-annotation", sts.Annotations["haproxy-annotation"])
		assert.Equal(t, "haproxy-annotation", sts.Spec.Template.Annotations["haproxy-annotation"])
	})

	t.Run("sidecar resources", func(t *testing.T) {
		haproxyResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    mustParseQuantity("600m"),
				corev1.ResourceMemory: mustParseQuantity("1Gi"),
			},
		}
		monitResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    mustParseQuantity("50m"),
				corev1.ResourceMemory: mustParseQuantity("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    mustParseQuantity("100m"),
				corev1.ResourceMemory: mustParseQuantity("128Mi"),
			},
		}

		getContainers := func(cluster *apiv1.PerconaServerMySQL) (haproxy, monit corev1.Container) {
			sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)
			for _, c := range sts.Spec.Template.Spec.Containers {
				switch c.Name {
				case AppName:
					haproxy = c
				case "mysql-monit":
					monit = c
				}
			}
			return
		}

		t.Run("cr < 1.2.0 mysql-monit inherits haproxy resources", func(t *testing.T) {
			cluster := cr.DeepCopy()
			cluster.Spec.CRVersion = "1.1.0"
			cluster.Spec.Proxy.HAProxy.Resources = haproxyResources

			_, monit := getContainers(cluster)
			assert.Equal(t, haproxyResources, monit.Resources)
		})

		t.Run("cr >= 1.2.0 mysql-monit has empty resources by default", func(t *testing.T) {
			cluster := cr.DeepCopy()
			cluster.Spec.CRVersion = "1.2.0"
			cluster.Spec.Proxy.HAProxy.Resources = haproxyResources
			cluster.Spec.Proxy.HAProxy.SidecarResources = nil

			_, monit := getContainers(cluster)
			assert.Equal(t, corev1.ResourceRequirements{}, monit.Resources)
		})

		t.Run("cr >= 1.2.0 mysql-monit uses sidecarResources independently", func(t *testing.T) {
			cluster := cr.DeepCopy()
			cluster.Spec.CRVersion = "1.2.0"
			cluster.Spec.Proxy.HAProxy.Resources = haproxyResources
			cluster.Spec.Proxy.HAProxy.SidecarResources = map[string]corev1.ResourceRequirements{
				"mysql-monit": monitResources,
			}

			haproxy, monit := getContainers(cluster)
			assert.Equal(t, haproxyResources, haproxy.Resources)
			assert.Equal(t, monitResources, monit.Resources)
		})
	})

	t.Run("volumes", func(t *testing.T) {
		volumeNames := func(sts *appsv1.StatefulSet) []string {
			names := make([]string, 0, len(sts.Spec.Template.Spec.Volumes))
			for _, v := range sts.Spec.Template.Spec.Volumes {
				names = append(names, v.Name)
			}
			return names
		}
		mountsByContainer := func(sts *appsv1.StatefulSet) map[string][]corev1.VolumeMount {
			mounts := make(map[string][]corev1.VolumeMount)
			for _, c := range sts.Spec.Template.Spec.Containers {
				mounts[c.Name] = c.VolumeMounts
			}
			return mounts
		}

		t.Run("cr < 1.2.0 has no internal-config volume", func(t *testing.T) {
			cluster := cr.DeepCopy()
			cluster.Spec.CRVersion = "1.1.0"

			sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)

			assert.Equal(t, []string{"bin", "haproxy-config", "users", "tls", "config"}, volumeNames(sts))
			for name, mounts := range mountsByContainer(sts) {
				for _, m := range mounts {
					assert.NotEqual(t, "internal-config", m.Name, "container %s should not mount internal-config", name)
				}
			}
		})

		t.Run("cr >= 1.2.0 has internal-config volume", func(t *testing.T) {
			cluster := cr.DeepCopy()
			cluster.Spec.CRVersion = "1.2.0"

			sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)

			assert.Equal(t, []string{"bin", "haproxy-config", "users", "tls", "config", "internal-config"}, volumeNames(sts))

			var internalConfig *corev1.Volume
			for i, v := range sts.Spec.Template.Spec.Volumes {
				if v.Name == "internal-config" {
					internalConfig = &sts.Spec.Template.Spec.Volumes[i]
				}
			}
			if internalConfig == nil {
				t.Fatal("internal-config volume is not found")
			}
			assert.NotNil(t, internalConfig.ConfigMap)
			assert.Equal(t, naming.InternalHAProxyConfigMapName(cluster.Name), internalConfig.ConfigMap.Name)
			assert.Equal(t, new(true), internalConfig.ConfigMap.Optional)

			expectedMount := corev1.VolumeMount{
				Name:      "internal-config",
				MountPath: "/etc/haproxy/internal-config",
			}
			mounts := mountsByContainer(sts)
			assert.Contains(t, mounts[AppName], expectedMount)
			assert.Contains(t, mounts["mysql-monit"], expectedMount)
		})
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

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			Proxy: apiv1.ProxySpec{
				HAProxy: &apiv1.HAProxySpec{
					Expose: apiv1.ServiceExpose{
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
