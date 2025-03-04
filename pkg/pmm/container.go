package pmm

import (
	corev1 "k8s.io/api/core/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func Container(cr *apiv1alpha1.PerconaServerMySQL, secret *corev1.Secret, dbType string) corev1.Container {
	ports := []corev1.ContainerPort{{ContainerPort: 7777}}
	for port := 30100; port <= 30105; port++ {
		ports = append(ports, corev1.ContainerPort{ContainerPort: int32(port)})
	}

	pmmSpec := cr.PMMSpec()
	envs := pmmEnvs(cr, secret, dbType)

	return corev1.Container{
		Name:            "pmm-client",
		Image:           pmmSpec.Image,
		ImagePullPolicy: pmmSpec.ImagePullPolicy,
		SecurityContext: pmmSpec.ContainerSecurityContext,
		Ports:           ports,
		Resources:       pmmSpec.Resources,
		Env:             envs,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      apiv1alpha1.BinVolumeName,
				MountPath: apiv1alpha1.BinVolumePath,
			},
		},
	}
}

func pmmEnvs(cr *apiv1alpha1.PerconaServerMySQL, secret *corev1.Secret, dbType string) []corev1.EnvVar {
	user := "api_key"
	passwordKey := string(apiv1alpha1.UserPMMServerKey)
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
				SecretKeyRef: k8s.SecretKeySelector(secret.Name, passwordKey),
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
			Value: string(apiv1alpha1.UserMonitor),
		},
		{
			Name: "DB_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(secret.Name, string(apiv1alpha1.UserMonitor)),
			},
		},
		{
			Name:  "DB_ARGS",
			Value: "--query-source=perfschema",
		},
	}
}
