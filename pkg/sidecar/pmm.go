package sidecar

import (
	corev1 "k8s.io/api/core/v1"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func PMMContainer(cr *v2.PerconaServerForMySQL) corev1.Container {
	spec := cr.Spec.PMM

	ports := []corev1.ContainerPort{{ContainerPort: 7777}}
	for i := 30100; i <= 30105; i++ {
		ports = append(ports, corev1.ContainerPort{ContainerPort: int32(i)})
	}

	// TODO: resources
	return corev1.Container{
		Name:            "pmm-client",
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		SecurityContext: spec.ContainerSecurityContext,
		Ports:           ports,
		Env: []corev1.EnvVar{
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
				Name:  "CLIENT_PORT_LISTEN",
				Value: "7777",
			},
			{
				Name:  "CLIENT_PORT_MIN",
				Value: "30100",
			},
			{
				Name:  "CLIENT_PORT_MAX",
				Value: "30105",
			},
			{
				Name:  "PMM_AGENT_SERVER_ADDRESS",
				Value: spec.ServerHost,
			},
			{
				Name:  "PMM_AGENT_SERVER_USERNAME",
				Value: spec.ServerUser,
			},
			{
				Name: "PMM_AGENT_SERVER_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: k8s.SecretKeySelector(cr.Spec.SecretsName, "pmmserver"),
				},
			},
			{
				Name:  "PMM_SERVER",
				Value: spec.ServerHost,
			},
			{
				Name:  "PMM_USER",
				Value: spec.ServerUser,
			},
			{
				Name: "PMM_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: k8s.SecretKeySelector(cr.Spec.SecretsName, "pmmserver"),
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
				Value: "/usr/local/percona/pmm2/config/pmm-agent.yaml",
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
				Name:  "PMM_AGENT_PRERUN_SCRIPT",
				Value: "pmm-admin status --wait=10s;\npmm-admin add ${DB_TYPE} ${PMM_ADMIN_CUSTOM_PARAMS} --skip-connection-check --metrics-mode=${PMM_AGENT_SETUP_METRICS_MODE} --username=${DB_USER} --password=${DB_PASSWORD} --cluster=${CLUSTER_NAME} --service-name=${PMM_AGENT_SETUP_NODE_NAME} --host=${POD_NAME} --port=${DB_PORT} ${DB_ARGS};\npmm-admin annotate --service-name=${PMM_AGENT_SETUP_NODE_NAME} 'Service restarted'",
			},
			{
				Name:  "PMM_AGENT_SIDECAR",
				Value: "true",
			},
			{
				Name:  "PMM_AGENT_SIDECAR_SLEEP",
				Value: "5",
			},
		},
	}
}
