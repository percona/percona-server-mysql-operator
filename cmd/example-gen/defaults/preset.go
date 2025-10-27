package defaults

import (
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/example-gen/internal/fill"
)

// PresetCluster assigns default values to fields in example cr.yaml based on their types.
// The internal presets slice defines the default value associated with each type.
func PresetCluster(cr *apiv1.PerconaServerMySQL) error {
	presets := []any{
		apiv1.Metadata{
			Labels: map[string]string{
				"rack": "rack-22",
			},
			Annotations: map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-backend-protocol": "tcp",
				"service.beta.kubernetes.io/aws-load-balancer-type":             "nlb",
			},
		},
		corev1.SecurityContext{
			Privileged: ptr.To(false),
			RunAsUser:  ptr.To(int64(1001)),
			RunAsGroup: ptr.To(int64(1001)),
		},
		corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("100M"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("200M"),
			},
		},
		apiv1.TLSSpec{
			SANs: []string{
				"mysql-1.example.com",
				"mysql-2.example.com",
				"mysql-3.example.com",
			},
			IssuerConf: &cmmeta.ObjectReference{
				Name:  "special-selfsigned-issuer",
				Kind:  "ClusterIssuer",
				Group: "cert-manager.io",
			},
		},
		apiv1.PodAffinity{
			TopologyKey: ptr.To("kubernetes.io/hostname"),
			Advanced: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "kubernetes.io/e2e-az-name",
										Operator: "In",
										Values: []string{
											"e2e-az1",
											"e2e-az2",
										},
									},
								},
							},
						},
					},
				},
			},
		},
		[]corev1.TopologySpreadConstraint{
			{
				MaxSkew:           1,
				TopologyKey:       "kubernetes.io/hostname",
				WhenUnsatisfiable: corev1.DoNotSchedule,
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app.kubernetes.io/name": "percona-server",
					},
				},
			},
		},
		apiv1.ServiceExpose{
			Type: corev1.ServiceTypeClusterIP,
			LoadBalancerSourceRanges: []string{
				"10.0.0.0/8",
			},
			Annotations: map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-backend-protocol": "tcp",
				"service.beta.kubernetes.io/aws-load-balancer-type":             "nlb",
			},
			Labels: map[string]string{
				"rack": "rack-22",
			},
			InternalTrafficPolicy: ptr.To(corev1.ServiceInternalTrafficPolicyCluster),
			ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyCluster,
		},
		apiv1.VolumeSpec{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
			HostPath: &corev1.HostPathVolumeSource{
				Path: "/data",
				Type: ptr.To(corev1.HostPathDirectory),
			},
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
				StorageClassName: ptr.To("standard"),
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("2Gi"),
					},
				},
			},
		},
		corev1.Probe{
			TimeoutSeconds:   3,
			PeriodSeconds:    5,
			SuccessThreshold: 1,
			FailureThreshold: 3,
		},
		apiv1.PodDisruptionBudgetSpec{
			MinAvailable:   ptr.To(intstr.FromInt(0)),
			MaxUnavailable: ptr.To(intstr.FromInt(1)),
		},
		corev1.PodSecurityContext{
			FSGroup:            ptr.To(int64(1001)),
			SupplementalGroups: []int64{1001, 1002, 1003},
		},
		apiv1.UnsafeFlags{
			MySQLSize:        false,
			Proxy:            false,
			ProxySize:        false,
			Orchestrator:     false,
			OrchestratorSize: false,
		},
		apiv1.UpgradeOptions{
			VersionServiceEndpoint: "https://check.percona.com",
			Apply:                  "disabled",
		},
		[]corev1.Toleration{
			{
				Key:               "node.alpha.kubernetes.io/unreachable",
				Operator:          "Exists",
				Effect:            "NoExecute",
				TolerationSeconds: ptr.To(int64(6000)),
			},
		},
		[]corev1.LocalObjectReference{
			{
				Name: "my-secret-1",
			},
			{
				Name: "my-secret-2",
			},
		},
		corev1.PullAlways,
		apiv1.InitContainerSpec{
			Image: "perconalab/percona-server-mysql-operator:main",
		},
		[]apiv1.SidecarPVC{
			{
				Name: "pvc-vol",
				Spec: corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},

		corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "kubernetes.io/e2e-az-name",
									Operator: "In",
									Values: []string{
										"e2e-az1",
										"e2e-az2",
									},
								},
							},
						},
					},
				},
			},
		},
		apiv1.BackupContainerOptions{
			Env: []corev1.EnvVar{
				{
					Name:  "CUSTOM_VAR",
					Value: "false",
				},
			},
			Args: apiv1.BackupContainerArgs{
				Xtrabackup: []string{
					"--someflag=abc",
				},
				Xbcloud: []string{
					"--someflag=abc",
				},
				Xbstream: []string{
					"--someflag=abc",
				},
			},
		},
	}

	return fill.ByType(cr, presets...)
}
