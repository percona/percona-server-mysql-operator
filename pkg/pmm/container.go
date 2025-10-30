package pmm

import (
	"reflect"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func Container(
	cr *apiv1.PerconaServerMySQL,
	secret *corev1.Secret,
	dbType string,
	customParams string) corev1.Container {
	// Default ports
	ports := []corev1.ContainerPort{{ContainerPort: 7777}}
	for port := 30100; port <= 30105; port++ {
		ports = append(ports, corev1.ContainerPort{ContainerPort: int32(port)})
	}

	pmmSpec := cr.PMMSpec()
	envs := pmmEnvs(cr, secret, dbType)

	if customParams != "" {
		envs = append(envs, corev1.EnvVar{
			Name:  "PMM_ADMIN_CUSTOM_PARAMS",
			Value: customParams,
		})
	}

	container := corev1.Container{
		Name:            "pmm-client",
		Image:           pmmSpec.Image,
		ImagePullPolicy: pmmSpec.ImagePullPolicy,
		SecurityContext: pmmSpec.ContainerSecurityContext,
		Ports:           ports,
		Resources:       pmmSpec.Resources,
		Env:             envs,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      apiv1.BinVolumeName,
				MountPath: apiv1.BinVolumePath,
			},
		},
	}

	if cr.CompareVersion("0.12.0") >= 0 {
		container.Lifecycle = &corev1.Lifecycle{
			PreStop: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"bash",
						"-c",
						"pmm-admin unregister --force",
					},
				},
			},
		}

		container.LivenessProbe = &corev1.Probe{
			InitialDelaySeconds: 60,
			TimeoutSeconds:      5,
			PeriodSeconds:       10,
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Port: intstr.FromInt32(7777),
					Path: "/local/Status",
				},
			},
		}

		if probe := pmmLivenessProbe(cr); probe != nil {
			container.LivenessProbe = probe
			if reflect.DeepEqual(container.LivenessProbe.ProbeHandler, corev1.ProbeHandler{}) {
				container.LivenessProbe.HTTPGet = &corev1.HTTPGetAction{
					Port: intstr.FromInt32(7777),
					Path: "/local/Status",
				}
			}
		}

		if probe := pmmReadinessProbe(cr); probe != nil {
			container.ReadinessProbe = probe
			if reflect.DeepEqual(container.ReadinessProbe.ProbeHandler, corev1.ProbeHandler{}) {
				container.ReadinessProbe.HTTPGet = &corev1.HTTPGetAction{
					Port: intstr.FromInt32(7777),
					Path: "/local/Status",
				}
			}
		}
	}

	return container
}

func pmmLivenessProbe(cr *apiv1.PerconaServerMySQL) *corev1.Probe {
	if cr.Spec.PMM.LivenessProbe != nil {
		return cr.Spec.PMM.LivenessProbe
	}
	return nil
}

func pmmReadinessProbe(cr *apiv1.PerconaServerMySQL) *corev1.Probe {
	if cr.Spec.PMM.ReadinessProbe != nil {
		return cr.Spec.PMM.ReadinessProbe
	}
	return nil
}

func pmmEnvs(cr *apiv1.PerconaServerMySQL, secret *corev1.Secret, dbType string) []corev1.EnvVar {
	user := "service_token"
	token := string(apiv1.UserPMMServerToken)
	pmmSpec := cr.PMMSpec()

	return []corev1.EnvVar{
		{
			Name: "POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		},
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
		{
			Name:  "CLUSTER_NAME",
			Value: cr.Name,
		},
		{
			Name:  "PMM_AGENT_SERVER_ADDRESS",
			Value: pmmSpec.ServerHost,
		},
		{
			Name:  "PMM_AGENT_SERVER_USERNAME",
			Value: user,
		},
		{
			Name: "PMM_AGENT_SERVER_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(secret.Name, token),
			},
		},
		{
			Name:  "PMM_AGENT_LISTEN_PORT",
			Value: "7777",
		},
		{
			Name:  "PMM_AGENT_PORTS_MIN",
			Value: "30100",
		},
		{
			Name:  "PMM_AGENT_PORTS_MAX",
			Value: "30105",
		},
		{
			Name:  "PMM_AGENT_CONFIG_FILE",
			Value: "/usr/local/percona/pmm/config/pmm-agent.yaml",
		},
		{
			Name:  "PMM_AGENT_SERVER_INSECURE_TLS",
			Value: "1",
		},
		{
			Name:  "PMM_AGENT_LISTEN_ADDRESS",
			Value: "0.0.0.0",
		},
		{
			Name:  "PMM_AGENT_SETUP_NODE_NAME",
			Value: "$(POD_NAMESPACE)-$(POD_NAME)",
		},
		{
			Name:  "PMM_AGENT_SETUP_METRICS_MODE",
			Value: "push",
		},
		{
			Name:  "PMM_AGENT_SETUP",
			Value: "1",
		},
		{
			Name:  "PMM_AGENT_SETUP_FORCE",
			Value: "1",
		},
		{
			Name:  "PMM_AGENT_SETUP_NODE_TYPE",
			Value: "container",
		},
		{
			Name:  "PMM_AGENT_SIDECAR",
			Value: "true",
		},
		{
			Name:  "PMM_AGENT_SIDECAR_SLEEP",
			Value: "5",
		},
		{
			Name:  "PMM_AGENT_PATHS_TEMPDIR",
			Value: "/tmp",
		},
		{
			Name:  "PMM_AGENT_PRERUN_SCRIPT",
			Value: "/opt/percona/pmm-prerun.sh",
		},
		{
			Name:  "DB_CLUSTER",
			Value: cr.Name,
		},
		{
			Name:  "DB_TYPE",
			Value: dbType,
		},
		{
			Name:  "DB_HOST",
			Value: "localhost",
		},
		{
			Name:  "DB_PORT",
			Value: "33062",
		},
		{
			Name:  "DB_USER",
			Value: string(apiv1.UserMonitor),
		},
		{
			Name: "DB_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(secret.Name, string(apiv1.UserMonitor)),
			},
		},
		{
			Name:  "DB_ARGS",
			Value: "--query-source=perfschema",
		},
	}
}
