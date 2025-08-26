package pmm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

func TestContainer(t *testing.T) {
	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			CRVersion: version.Version(),
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

	assert.NotNil(t, container.Lifecycle)
	assert.Equal(t, []string{"bash", "-c", "pmm-admin unregister --force"}, container.Lifecycle.PreStop.Exec.Command)

	assert.NotNil(t, container.LivenessProbe)
	assert.Equal(t, intstr.FromInt32(7777), container.LivenessProbe.HTTPGet.Port)
	assert.Equal(t, "/local/Status", container.LivenessProbe.HTTPGet.Path)
}

func TestContainer_CustomProbes(t *testing.T) {
	lp := &corev1.Probe{
		InitialDelaySeconds: 15,
		TimeoutSeconds:      7,
		PeriodSeconds:       11,
		SuccessThreshold:    1,
		FailureThreshold:    5,
	}
	rp := &corev1.Probe{
		InitialDelaySeconds: 5,
		TimeoutSeconds:      4,
		PeriodSeconds:       12,
		SuccessThreshold:    1,
		FailureThreshold:    3,
	}

	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			CRVersion: version.Version(),
			PMM: &apiv1alpha1.PMMSpec{
				Image:           "percona/pmm-client:latest",
				ImagePullPolicy: corev1.PullIfNotPresent,
				ServerHost:      "pmm-server",
				LivenessProbes:  lp,
				ReadinessProbes: rp,
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

	c := Container(cr, secret, "mysql", "")

	assert.Equal(t, int32(15), c.LivenessProbe.InitialDelaySeconds)
	assert.Equal(t, int32(7), c.LivenessProbe.TimeoutSeconds)
	assert.Equal(t, int32(11), c.LivenessProbe.PeriodSeconds)
	assert.Equal(t, intstr.FromInt32(7777), c.LivenessProbe.HTTPGet.Port)
	assert.Equal(t, "/local/Status", c.LivenessProbe.HTTPGet.Path)

	if assert.NotNil(t, c.ReadinessProbe) && assert.NotNil(t, c.ReadinessProbe.HTTPGet) {
		assert.Equal(t, int32(5), c.ReadinessProbe.InitialDelaySeconds)
		assert.Equal(t, int32(4), c.ReadinessProbe.TimeoutSeconds)
		assert.Equal(t, int32(12), c.ReadinessProbe.PeriodSeconds)
		assert.Equal(t, intstr.FromInt32(7777), c.ReadinessProbe.HTTPGet.Port)
		assert.Equal(t, "/local/Status", c.ReadinessProbe.HTTPGet.Path)
	}
}
