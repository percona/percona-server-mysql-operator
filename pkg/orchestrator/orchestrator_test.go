package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

func containerByName(t *testing.T, containers []corev1.Container, name string) *corev1.Container {
	t.Helper()
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	require.Fail(t, "container not found", "name: %s", name)
	return nil
}

func envValue(t *testing.T, env []corev1.EnvVar, name string) string {
	t.Helper()
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	require.Fail(t, "environment variable not found", "name: %s", name)
	return ""
}

func TestStatefulSet(t *testing.T) {
	const (
		ns         = "orc-ns"
		initImage  = "init-image"
		tlsHash    = "tls-hash"
		configHash = "config-hash"
	)

	cr := readDefaultCluster(t, "cluster", ns)
	if err := cr.CheckNSetDefaults(t.Context(), &platform.ServerVersion{
		Platform: platform.PlatformKubernetes,
	}); err != nil {
		t.Fatal(err)
	}

	cr.Spec.Metadata = &apiv1.Metadata{
		Labels: map[string]string{
			"global-label": "global-value",
		},
		Annotations: map[string]string{
			"global-annotation": "global-annotation-value",
		},
	}

	t.Run("object meta", func(t *testing.T) {
		cluster := cr.DeepCopy()

		sts := StatefulSet(cluster, initImage, configHash, tlsHash)

		assert.NotNil(t, sts)
		assert.Equal(t, "cluster-orc", sts.Name)
		assert.Equal(t, "orc-ns", sts.Namespace)
		labels := map[string]string{
			"app.kubernetes.io/name":       "orchestrator",
			"app.kubernetes.io/part-of":    "percona-server",
			"app.kubernetes.io/instance":   "cluster",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/component":  "orchestrator",
			"global-label":                 "global-value",
		}
		assert.Equal(t, labels, sts.Labels)

		annotations := map[string]string{
			"global-annotation": "global-annotation-value",
		}
		assert.Equal(t, annotations, sts.Annotations)
	})

	t.Run("defaults", func(t *testing.T) {
		cluster := cr.DeepCopy()

		sts := StatefulSet(cluster, initImage, configHash, tlsHash)

		assert.Equal(t, "apps/v1", sts.APIVersion)
		assert.Equal(t, "StatefulSet", sts.Kind)
		assert.Equal(t, int32(3), *sts.Spec.Replicas)
		assert.Equal(t, "cluster-orc", sts.Spec.ServiceName)
		assert.Equal(t, MatchLabels(cluster), sts.Spec.Selector.MatchLabels)
		assert.Equal(t, Labels(cluster), sts.Spec.Template.Labels)
		assert.Equal(t, appsv1.RollingUpdateStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
		require.NotNil(t, sts.Spec.UpdateStrategy.RollingUpdate)
		assert.Equal(t, int32(0), *sts.Spec.UpdateStrategy.RollingUpdate.Partition)

		initContainers := sts.Spec.Template.Spec.InitContainers
		assert.Len(t, initContainers, 1)
		assert.Equal(t, initImage, initContainers[0].Image)

		assert.Equal(t, map[string]string{
			"percona.com/configuration-hash": configHash,
			"percona.com/last-applied-tls":   tlsHash,
			"global-annotation":              "global-annotation-value",
		}, sts.Spec.Template.Annotations)
	})

	t.Run("update strategy", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cluster.Spec.UpdateStrategy = appsv1.OnDeleteStatefulSetStrategyType

		sts := StatefulSet(cluster, initImage, configHash, tlsHash)

		assert.Equal(t, appsv1.OnDeleteStatefulSetStrategyType, sts.Spec.UpdateStrategy.Type)
		assert.Nil(t, sts.Spec.UpdateStrategy.RollingUpdate)
	})

	t.Run("volumes", func(t *testing.T) {
		cluster := cr.DeepCopy()
		sts := StatefulSet(cluster, initImage, configHash, tlsHash)

		volumes := make(map[string]corev1.Volume, len(sts.Spec.Template.Spec.Volumes))
		for _, volume := range sts.Spec.Template.Spec.Volumes {
			volumes[volume.Name] = volume
		}

		require.Len(t, volumes, 5)
		assert.NotNil(t, volumes[apiv1.BinVolumeName].EmptyDir)
		assert.NotNil(t, volumes[configVolumeName].EmptyDir)
		require.NotNil(t, volumes[credsVolumeName].Secret)
		assert.Equal(t, cluster.InternalSecretName(), volumes[credsVolumeName].Secret.SecretName)
		require.NotNil(t, volumes[tlsVolumeName].Secret)
		assert.Equal(t, cluster.Spec.SSLSecretName, volumes[tlsVolumeName].Secret.SecretName)
		require.NotNil(t, volumes[customConfigVolumeName].ConfigMap)
		assert.Equal(t, ConfigMapName(cluster), volumes[customConfigVolumeName].ConfigMap.Name)
	})

	t.Run("termination grace period seconds", func(t *testing.T) {
		cluster := cr.DeepCopy()

		cluster.Spec.Orchestrator.TerminationGracePeriodSeconds = nil
		sts := StatefulSet(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, int64(600), *sts.Spec.Template.Spec.TerminationGracePeriodSeconds)

		cluster.Spec.Orchestrator.TerminationGracePeriodSeconds = new(int64(30))

		sts = StatefulSet(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, int64(30), *sts.Spec.Template.Spec.TerminationGracePeriodSeconds)
	})

	t.Run("image pull secrets", func(t *testing.T) {
		cluster := cr.DeepCopy()

		sts := StatefulSet(cluster, initImage, configHash, tlsHash)
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

		sts = StatefulSet(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, imagePullSecrets, sts.Spec.Template.Spec.ImagePullSecrets)
	})

	t.Run("runtime class name", func(t *testing.T) {
		cluster := cr.DeepCopy()
		sts := StatefulSet(cluster, initImage, configHash, tlsHash)
		assert.Empty(t, sts.Spec.Template.Spec.RuntimeClassName)

		const runtimeClassName = "runtimeClassName"
		cluster.Spec.Orchestrator.RuntimeClassName = ptr.To(runtimeClassName)

		sts = StatefulSet(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, runtimeClassName, *sts.Spec.Template.Spec.RuntimeClassName)
	})

	t.Run("service account name", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cluster.Spec.Orchestrator.ServiceAccountName = ""
		sts := StatefulSet(cluster, initImage, configHash, tlsHash)
		assert.Empty(t, sts.Spec.Template.Spec.ServiceAccountName)

		const serviceAccountName = "service"
		cluster.Spec.Orchestrator.ServiceAccountName = serviceAccountName

		sts = StatefulSet(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, serviceAccountName, sts.Spec.Template.Spec.ServiceAccountName)
	})

	t.Run("tolerations", func(t *testing.T) {
		cluster := cr.DeepCopy()
		sts := StatefulSet(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, []corev1.Toleration(nil), sts.Spec.Template.Spec.Tolerations)

		tolerations := []corev1.Toleration{
			{
				Key:               "node.alpha.kubernetes.io/unreachable",
				Operator:          "Exists",
				Value:             "value",
				Effect:            "NoExecute",
				TolerationSeconds: new(int64(1001)),
			},
		}
		cluster.Spec.Orchestrator.Tolerations = tolerations

		sts = StatefulSet(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, tolerations, sts.Spec.Template.Spec.Tolerations)
	})

	t.Run("containers", func(t *testing.T) {
		t.Run("orchestrator", func(t *testing.T) {
			cluster := cr.DeepCopy()
			cluster.Spec.CRVersion = "1.3.0"

			sts := StatefulSet(cluster, initImage, configHash, tlsHash)
			require.Len(t, sts.Spec.Template.Spec.Containers, 2)
			orchestrator := containerByName(t, sts.Spec.Template.Spec.Containers, AppName)

			assert.Equal(t, "cluster-orc", envValue(t, orchestrator.Env, "ORC_SERVICE"))
			assert.Equal(t, "cluster-mysql", envValue(t, orchestrator.Env, "MYSQL_SERVICE"))
			assert.Equal(t, "true", envValue(t, orchestrator.Env, "RAFT_ENABLED"))
			assert.Equal(t, "cluster", envValue(t, orchestrator.Env, "CLUSTER_NAME"))
			assert.Contains(t, orchestrator.VolumeMounts, corev1.VolumeMount{
				Name: "config", MountPath: "/etc/orchestrator/config",
			})
		})

		t.Run("mysql-monit", func(t *testing.T) {
			tests := []struct {
				name               string
				crVersion          string
				expectedOrcService string
			}{
				{
					name:               "before 1.3.0",
					crVersion:          "1.2.0",
					expectedOrcService: "cluster-mysql",
				},
				{
					name:               "from 1.3.0",
					crVersion:          "1.3.0",
					expectedOrcService: "cluster-orc",
				},
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					cluster := cr.DeepCopy()
					cluster.Spec.CRVersion = tt.crVersion

					sts := StatefulSet(cluster, initImage, configHash, tlsHash)
					require.Len(t, sts.Spec.Template.Spec.Containers, 2)
					mysqlMonit := containerByName(t, sts.Spec.Template.Spec.Containers, "mysql-monit")

					assert.Equal(t, tt.expectedOrcService, envValue(t, mysqlMonit.Env, "ORC_SERVICE"))
					assert.Equal(t, "cluster-mysql", envValue(t, mysqlMonit.Env, "MYSQL_SERVICE"))
					assert.Contains(t, mysqlMonit.Args, "-service=$(MYSQL_SERVICE)")
					assert.Contains(t, mysqlMonit.Args, "-on-change=/opt/percona/orc-add_mysql_nodes.sh")
					assert.Contains(t, mysqlMonit.VolumeMounts, corev1.VolumeMount{
						Name: "config", MountPath: "/etc/orchestrator/config",
					})
				})
			}
		})
	})
}

func TestService(t *testing.T) {
	cr := &apiv1.PerconaServerMySQL{
		Name:      "cluster",
		Namespace: "orc-ns",
	}

	t.Run("defaults", func(t *testing.T) {
		service := Service(cr)

		assert.Equal(t, "v1", service.APIVersion)
		assert.Equal(t, "Service", service.Kind)
		assert.Equal(t, "cluster-orc", service.Name)
		assert.Equal(t, "orc-ns", service.Namespace)
		assert.Equal(t, map[string]string{
			"app.kubernetes.io/name":       "orchestrator",
			"app.kubernetes.io/part-of":    "percona-server",
			"app.kubernetes.io/instance":   "cluster",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/component":  "orchestrator",
		}, service.Labels)
		assert.Nil(t, service.Annotations)
		assert.Equal(t, corev1.ClusterIPNone, service.Spec.ClusterIP)
		assert.True(t, service.Spec.PublishNotReadyAddresses)
		assert.Equal(t, MatchLabels(cr), service.Spec.Selector)
		assert.Equal(t, []corev1.ServicePort{
			{Name: "web", Port: defaultWebPort},
			{Name: "raft", Port: defaultRaftPort},
		}, service.Spec.Ports)
	})

	t.Run("global labels", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cluster.Spec.Metadata = &apiv1.Metadata{
			Labels: map[string]string{
				"global-label": "global-value",
			},
		}

		service := Service(cluster)

		assert.Equal(t, map[string]string{
			"app.kubernetes.io/name":       "orchestrator",
			"app.kubernetes.io/part-of":    "percona-server",
			"app.kubernetes.io/instance":   "cluster",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/component":  "orchestrator",
			"global-label":                 "global-value",
		}, service.Labels)
	})

	t.Run("global annotations", func(t *testing.T) {
		cluster := cr.DeepCopy()
		cluster.Spec.Metadata = &apiv1.Metadata{
			Annotations: map[string]string{
				"global-annotation": "global-value",
			},
		}

		service := Service(cluster)

		assert.Equal(t, map[string]string{
			"global-annotation": "global-value",
		}, service.Annotations)
	})
}

func TestPodService(t *testing.T) {
	podName := "test-pod"

	cr := &apiv1.PerconaServerMySQL{
		Name:      "test-cluster",
		Namespace: "test-namespace",
		Spec: apiv1.PerconaServerMySQLSpec{
			Metadata: &apiv1.Metadata{
				Labels: map[string]string{
					"global-label": "global-value",
				},
				Annotations: map[string]string{
					"global-annotation": "global-annotation-value",
				},
			},
			Orchestrator: apiv1.OrchestratorSpec{
				Expose: apiv1.ServiceExpose{
					Type: corev1.ServiceTypeLoadBalancer,
					Labels: map[string]string{
						"custom-label": "custom-value",
					},
					Annotations: map[string]string{
						"custom-annotation": "custom-annotation-value",
					},
					LoadBalancerSourceRanges:      []string{"10.0.0.0/8"},
					AllocateLoadBalancerNodePorts: new(false),
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
			expectedLabels["global-label"] = "global-value"
			expectedLabels[naming.LabelExposed] = "true"
			assert.Equal(t, expectedLabels, service.Labels)

			expectedAnnotations := cr.DeepCopy().Spec.Orchestrator.Expose.Annotations
			expectedAnnotations["global-annotation"] = "global-annotation-value"
			assert.Equal(t, expectedAnnotations, service.Annotations)

			expectedSelector := MatchLabels(cr)
			expectedSelector["statefulset.kubernetes.io/pod-name"] = podName
			assert.Equal(t, expectedSelector, service.Spec.Selector)

			if tt.expectLoadBalancer {
				assert.Equal(t, cr.Spec.Orchestrator.Expose.LoadBalancerSourceRanges, service.Spec.LoadBalancerSourceRanges)
				assert.Equal(t, cr.Spec.Orchestrator.Expose.AllocateLoadBalancerNodePorts, service.Spec.AllocateLoadBalancerNodePorts)
			} else {
				assert.Empty(t, service.Spec.LoadBalancerSourceRanges)
				assert.Nil(t, service.Spec.AllocateLoadBalancerNodePorts)
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
