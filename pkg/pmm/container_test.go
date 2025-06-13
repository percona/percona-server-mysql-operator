package pmm

import (
	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestContainer(t *testing.T) {
	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			PMM: &apiv1alpha1.PMMSpec{
				Image:           "percona/pmm-client:latest",
				ImagePullPolicy: corev1.PullIfNotPresent,
				ServerHost:      "pmm-server",
			},
		},
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
		Data: map[string][]byte{
			string(apiv1alpha1.UserPMMServerToken): []byte("token"),
			string(apiv1alpha1.UserMonitor):        []byte("monitor-pass"),
		},
	}

	container := Container(cr, secret, "mysql", "")

	assert.Equal(t, "pmm-client", container.Name)
	assert.Equal(t, "percona/pmm-client:latest", container.Image)
	assert.Equal(t, corev1.PullIfNotPresent, container.ImagePullPolicy)
	assert.Len(t, container.Ports, 7)

	expectedPorts := []int32{7777, 30100, 30101, 30102, 30103, 30104, 30105}
	for i, port := range container.Ports {
		assert.Equal(t, expectedPorts[i], port.ContainerPort)
	}

	assert.Equal(t, 1, len(container.VolumeMounts))
	assert.Equal(t, apiv1alpha1.BinVolumeName, container.VolumeMounts[0].Name)
	assert.Equal(t, apiv1alpha1.BinVolumePath, container.VolumeMounts[0].MountPath)

	foundEnv := map[string]bool{}
	for _, env := range container.Env {
		foundEnv[env.Name] = true
	}

	expectedEnvs := []string{
		"POD_NAME", "POD_NAMESPACE", "CLUSTER_NAME", "PMM_AGENT_SERVER_ADDRESS", "PMM_AGENT_SERVER_USERNAME",
		"PMM_AGENT_SERVER_PASSWORD", "PMM_AGENT_LISTEN_PORT", "PMM_AGENT_PORTS_MIN", "PMM_AGENT_PORTS_MAX", "PMM_AGENT_PRERUN_SCRIPT",
		"PMM_AGENT_CONFIG_FILE", "PMM_AGENT_SERVER_INSECURE_TLS", "PMM_AGENT_LISTEN_ADDRESS", "PMM_AGENT_SETUP_NODE_NAME",
		"PMM_AGENT_SETUP_METRICS_MODE", "PMM_AGENT_SETUP", "PMM_AGENT_SETUP_FORCE", "PMM_AGENT_SETUP_NODE_TYPE",
		"PMM_AGENT_SIDECAR", "PMM_AGENT_SIDECAR_SLEEP", "PMM_AGENT_PATHS_TEMPDIR", "DB_CLUSTER",
		"DB_TYPE", "DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_ARGS",
	}

	for _, env := range expectedEnvs {
		assert.True(t, foundEnv[env])
	}
}
