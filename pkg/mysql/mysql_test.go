package mysql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

func TestStatefulSet(t *testing.T) {
	const ns = "mysql-ns"

	const initImage = "init-image"
	const configHash = "config-hash"
	const tlsHash = "config-hash"

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "some-secret",
			Namespace: ns,
		},
		StringData: map[string]string{},
	}

	cr := readDefaultCluster(t, "cluster", ns)
	if err := cr.CheckNSetDefaults(t.Context(), &platform.ServerVersion{
		Platform: platform.PlatformKubernetes,
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("object meta", func(t *testing.T) {
		cluster := cr.DeepCopy()

		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)

		assert.NotNil(t, sts)
		assert.Equal(t, "cluster-mysql", sts.Name)
		assert.Equal(t, "mysql-ns", sts.Namespace)
		labels := map[string]string{
			"app.kubernetes.io/name":       "mysql",
			"app.kubernetes.io/part-of":    "percona-server",
			"app.kubernetes.io/instance":   "cluster",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/component":  "database",
			"app.kubernetes.io/version":    "v" + version.Version(),
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

		cluster.Spec.MySQL.TerminationGracePeriodSeconds = nil
		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, int64(600), *sts.Spec.Template.Spec.TerminationGracePeriodSeconds)

		cluster.Spec.MySQL.TerminationGracePeriodSeconds = ptr.To(int64(30))

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
		cluster.Spec.MySQL.ImagePullSecrets = imagePullSecrets

		sts = StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, imagePullSecrets, sts.Spec.Template.Spec.ImagePullSecrets)
	})

	t.Run("runtime class name", func(t *testing.T) {
		cluster := cr.DeepCopy()
		sts := StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		var e *string
		assert.Equal(t, e, sts.Spec.Template.Spec.RuntimeClassName)

		const runtimeClassName = "runtimeClassName"
		cluster.Spec.MySQL.RuntimeClassName = ptr.To(runtimeClassName)

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
		cluster.Spec.MySQL.Tolerations = tolerations

		sts = StatefulSet(cluster, initImage, configHash, tlsHash, secret)
		assert.Equal(t, tolerations, sts.Spec.Template.Spec.Tolerations)
	})
}

func TestStatefulsetVolumes(t *testing.T) {
	configHash := "123abc"
	tlsHash := "123abc"
	initImage := "percona/init:latest"

	expectedAnnotations := map[string]string{
		string(naming.AnnotationTLSHash):    tlsHash,
		string(naming.AnnotationConfigHash): configHash,
	}

	expectedPVCs := []corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "datadir",
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						"storage": resource.MustParse("1Gi"),
					},
				},
			},
		},
	}

	tests := map[string]struct {
		mysqlSpec           apiv1alpha1.MySQLSpec
		expectedStatefulSet appsv1.StatefulSet
	}{
		"pvc configured": {
			mysqlSpec: apiv1alpha1.MySQLSpec{
				PodSpec: apiv1alpha1.PodSpec{
					Size:                          3,
					TerminationGracePeriodSeconds: ptr.To(int64(30)),
					VolumeSpec: &apiv1alpha1.VolumeSpec{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: map[corev1.ResourceName]resource.Quantity{
									corev1.ResourceStorage: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
			expectedStatefulSet: appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1-mysql",
					Namespace: "test-ns",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							InitContainers: []corev1.Container{
								{
									Image: initImage,
								},
							},
							TerminationGracePeriodSeconds: ptr.To(int64(30)),
							Volumes:                       expectedVolumes(),
						},
					},
					VolumeClaimTemplates: expectedPVCs,
					ServiceName:          "ps-cluster1-mysql",
				},
			},
		},
		"host path configured": {
			mysqlSpec: apiv1alpha1.MySQLSpec{
				PodSpec: apiv1alpha1.PodSpec{
					Size:                          3,
					TerminationGracePeriodSeconds: ptr.To(int64(30)),
					VolumeSpec: &apiv1alpha1.VolumeSpec{
						HostPath: &corev1.HostPathVolumeSource{},
					},
				},
			},
			expectedStatefulSet: appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1-mysql",
					Namespace: "test-ns",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							InitContainers: []corev1.Container{
								{
									Image: initImage,
								},
							},
							TerminationGracePeriodSeconds: ptr.To(int64(30)),
							Volumes: append(expectedVolumes(),
								corev1.Volume{
									Name: "datadir",
									VolumeSource: corev1.VolumeSource{
										HostPath: &corev1.HostPathVolumeSource{},
									},
								},
							),
						},
					},
					ServiceName: "ps-cluster1-mysql",
				},
			},
		},
		"empty dir configured": {
			mysqlSpec: apiv1alpha1.MySQLSpec{
				PodSpec: apiv1alpha1.PodSpec{
					Size:                          3,
					TerminationGracePeriodSeconds: ptr.To(int64(30)),
					VolumeSpec: &apiv1alpha1.VolumeSpec{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
			expectedStatefulSet: appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1-mysql",
					Namespace: "test-ns",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							InitContainers: []corev1.Container{
								{
									Image: initImage,
								},
							},
							TerminationGracePeriodSeconds: ptr.To(int64(30)),
							Volumes: append(expectedVolumes(),
								corev1.Volume{
									Name: "datadir",
									VolumeSource: corev1.VolumeSource{
										EmptyDir: &corev1.EmptyDirVolumeSource{},
									},
								},
							),
						},
					},
					ServiceName: "ps-cluster1-mysql",
				},
			},
		},
		"both entry dir and host path provided - only host path is actually configured due to higher priority": {
			mysqlSpec: apiv1alpha1.MySQLSpec{
				PodSpec: apiv1alpha1.PodSpec{
					Size:                          3,
					TerminationGracePeriodSeconds: ptr.To(int64(30)),
					VolumeSpec: &apiv1alpha1.VolumeSpec{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
						HostPath: &corev1.HostPathVolumeSource{},
					},
				},
			},
			expectedStatefulSet: appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1-mysql",
					Namespace: "test-ns",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							InitContainers: []corev1.Container{
								{
									Image: initImage,
								},
							},
							TerminationGracePeriodSeconds: ptr.To(int64(30)),
							Volumes: append(expectedVolumes(),
								corev1.Volume{
									Name: "datadir",
									VolumeSource: corev1.VolumeSource{
										HostPath: &corev1.HostPathVolumeSource{},
									},
								},
							),
						},
					},
					ServiceName: "ps-cluster1-mysql",
				},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "test-ns",
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					CRVersion: version.Version(),
					MySQL:     tt.mysqlSpec,
				},
			}

			sts := StatefulSet(cr, initImage, configHash, tlsHash, nil)

			assert.NotNil(t, sts)
			assert.Equal(t, tt.expectedStatefulSet.Name, sts.Name)
			assert.Equal(t, tt.expectedStatefulSet.Namespace, sts.Namespace)
			assert.Equal(t, tt.expectedStatefulSet.Spec.Replicas, sts.Spec.Replicas)

			assert.Equal(t, tt.expectedStatefulSet.Spec.Template.Annotations, sts.Spec.Template.Annotations)

			assert.Equal(t, tt.expectedStatefulSet.Spec.ServiceName, sts.Spec.ServiceName)

			assert.Equal(t, tt.expectedStatefulSet.Spec.Template.Spec.Volumes, sts.Spec.Template.Spec.Volumes)

			initContainers := sts.Spec.Template.Spec.InitContainers
			assert.Len(t, initContainers, 1)
			assert.Equal(t, initImage, initContainers[0].Image)

			assert.Equal(t, tt.expectedStatefulSet.Spec.Template.Spec.TerminationGracePeriodSeconds, sts.Spec.Template.Spec.TerminationGracePeriodSeconds)

			assert.Equal(t, tt.expectedStatefulSet.Spec.VolumeClaimTemplates, sts.Spec.VolumeClaimTemplates)
		})
	}
}

func expectedVolumes() []corev1.Volume {
	return []corev1.Volume{
		{
			Name: "bin",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "mysqlsh",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "users",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: "internal-ps-cluster1",
				},
			},
		},
		{
			Name: "tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: "",
				},
			},
		},
		{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ConfigMap: &corev1.ConfigMapProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "ps-cluster1-mysql",
								},
								Items: []corev1.KeyToPath{
									{
										Key:  "my.cnf",
										Path: "my-config.cnf",
									},
								},
								Optional: ptr.To(true),
							},
						},
						{
							ConfigMap: &corev1.ConfigMapProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "auto-ps-cluster1-mysql",
								},
								Items: []corev1.KeyToPath{
									{
										Key:  "my.cnf",
										Path: "auto-config.cnf",
									},
								},
								Optional: ptr.To(true),
							},
						},
						{
							Secret: &corev1.SecretProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "ps-cluster1-mysql",
								},
								Items: []corev1.KeyToPath{
									{
										Key:  "my.cnf",
										Path: "my-secret.cnf",
									},
								},
								Optional: ptr.To(true),
							},
						},
					},
				},
			},
		},
		{
			Name: "backup-logs",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "vault-keyring-secret",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: "",
					Optional:   ptr.To(true),
				},
			},
		},
	}
}

func TestPrimaryService_GroupReplication(t *testing.T) {
	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			MySQL: apiv1alpha1.MySQLSpec{
				ClusterType: apiv1alpha1.ClusterTypeGR,
				ExposePrimary: apiv1alpha1.ServiceExposeTogglable{
					Enabled: true,
					ServiceExpose: apiv1alpha1.ServiceExpose{
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
		"LoadBalancer service for GR": {
			serviceType:                 corev1.ServiceTypeLoadBalancer,
			expectLoadBalancer:          true,
			expectExternalTrafficPolicy: true,
		},
		"NodePort service for GR": {
			serviceType:                 corev1.ServiceTypeNodePort,
			expectLoadBalancer:          false,
			expectExternalTrafficPolicy: true,
		},
		"ClusterIP service for GR": {
			serviceType:                 corev1.ServiceTypeClusterIP,
			expectLoadBalancer:          false,
			expectExternalTrafficPolicy: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr.Spec.MySQL.ExposePrimary.Type = tt.serviceType

			service := PrimaryService(cr)

			assert.Equal(t, "v1", service.APIVersion)
			assert.Equal(t, "Service", service.Kind)
			assert.Equal(t, "test-cluster-mysql-primary", service.Name)
			assert.Equal(t, "test-namespace", service.Namespace)

			assert.Equal(t, tt.serviceType, service.Spec.Type)

			expectedLabels := MatchLabels(cr)
			expectedLabels["custom-label"] = "custom-value"
			assert.Equal(t, expectedLabels, service.Labels)

			expectedSelector := MatchLabels(cr)
			expectedSelector[naming.LabelMySQLPrimary] = "true"
			assert.Equal(t, expectedSelector, service.Spec.Selector)

			assert.Equal(t, cr.Spec.MySQL.ExposePrimary.Annotations, service.Annotations)

			if tt.expectLoadBalancer {
				assert.Equal(t, cr.Spec.MySQL.ExposePrimary.LoadBalancerSourceRanges, service.Spec.LoadBalancerSourceRanges)
			} else {
				assert.Empty(t, service.Spec.LoadBalancerSourceRanges)
			}

			if tt.expectExternalTrafficPolicy {
				assert.Equal(t, cr.Spec.MySQL.ExposePrimary.ExternalTrafficPolicy, service.Spec.ExternalTrafficPolicy)
			} else {
				assert.Empty(t, service.Spec.ExternalTrafficPolicy)
			}

			assert.Equal(t, cr.Spec.MySQL.ExposePrimary.InternalTrafficPolicy, service.Spec.InternalTrafficPolicy)
		})
	}
}

func TestPodService(t *testing.T) {
	podName := "test-pod"

	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			MySQL: apiv1alpha1.MySQLSpec{
				ClusterType: apiv1alpha1.ClusterTypeGR,
				Expose: apiv1alpha1.ServiceExposeTogglable{
					Enabled: true,
					ServiceExpose: apiv1alpha1.ServiceExpose{
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
		"LoadBalancer service for GR": {
			serviceType:                 corev1.ServiceTypeLoadBalancer,
			expectLoadBalancer:          true,
			expectExternalTrafficPolicy: true,
		},
		"NodePort service for GR": {
			serviceType:                 corev1.ServiceTypeNodePort,
			expectLoadBalancer:          false,
			expectExternalTrafficPolicy: true,
		},
		"ClusterIP service for GR": {
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

			assert.Equal(t, cr.Spec.MySQL.Expose.Annotations, service.Annotations)

			if tt.expectLoadBalancer {
				assert.Equal(t, cr.Spec.MySQL.Expose.LoadBalancerSourceRanges, service.Spec.LoadBalancerSourceRanges)
			} else {
				assert.Empty(t, service.Spec.LoadBalancerSourceRanges)
			}

			if tt.expectExternalTrafficPolicy {
				assert.Equal(t, cr.Spec.MySQL.Expose.ExternalTrafficPolicy, service.Spec.ExternalTrafficPolicy)
			} else {
				assert.Empty(t, service.Spec.ExternalTrafficPolicy)
			}

			assert.Equal(t, cr.Spec.MySQL.Expose.InternalTrafficPolicy, service.Spec.InternalTrafficPolicy)
		})
	}
}

func TestPrimaryServiceName(t *testing.T) {
	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-cluster",
		},
	}
	serviceName := PrimaryServiceName(cr)
	assert.Equal(t, "my-cluster-mysql-primary", serviceName)
}
