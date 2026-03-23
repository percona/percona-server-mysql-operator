package pmm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

func TestContainer(t *testing.T) {
	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
		Spec: apiv1.PerconaServerMySQLSpec{
			CRVersion: version.Version(),
			PMM: &apiv1.PMMSpec{
				Image:           "percona/pmm-client:latest",
				ImagePullPolicy: corev1.PullIfNotPresent,
				ServerHost:      "pmm-server",
			},
		},
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
		Data: map[string][]byte{
			string(apiv1.UserPMMServerToken): []byte("token"),
			string(apiv1.UserMonitor):        []byte("monitor-pass"),
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
	assert.Equal(t, apiv1.BinVolumeName, container.VolumeMounts[0].Name)
	assert.Equal(t, apiv1.BinVolumePath, container.VolumeMounts[0].MountPath)

	envMap := map[string]corev1.EnvVar{}
	for _, env := range container.Env {
		envMap[env.Name] = env
	}

	expectedEnvs := []corev1.EnvVar{
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		{Name: "CLUSTER_NAME", Value: "test-cluster"},
		{Name: "PMM_AGENT_SERVER_ADDRESS", Value: "pmm-server"},
		{Name: "PMM_AGENT_SERVER_USERNAME", Value: "service_token"},
		{Name: "PMM_AGENT_SERVER_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: k8s.SecretKeySelector("test-secret", string(apiv1.UserPMMServerToken))}},
		{Name: "PMM_AGENT_LISTEN_PORT", Value: "7777"},
		{Name: "PMM_AGENT_PORTS_MIN", Value: "30100"},
		{Name: "PMM_AGENT_PORTS_MAX", Value: "30105"},
		{Name: "PMM_AGENT_PRERUN_SCRIPT", Value: "/opt/percona/pmm-prerun.sh"},
		{Name: "PMM_AGENT_CONFIG_FILE", Value: "/usr/local/percona/pmm/config/pmm-agent.yaml"},
		{Name: "PMM_AGENT_SERVER_INSECURE_TLS", Value: "1"},
		{Name: "PMM_AGENT_LISTEN_ADDRESS", Value: "0.0.0.0"},
		{Name: "PMM_AGENT_SETUP_NODE_NAME", Value: "$(POD_NAMESPACE)-$(POD_NAME)"},
		{Name: "PMM_AGENT_SETUP_METRICS_MODE", Value: "push"},
		{Name: "PMM_AGENT_SETUP", Value: "1"},
		{Name: "PMM_AGENT_SETUP_FORCE", Value: "1"},
		{Name: "PMM_AGENT_SETUP_NODE_TYPE", Value: "container"},
		{Name: "PMM_AGENT_SIDECAR", Value: "true"},
		{Name: "PMM_AGENT_SIDECAR_SLEEP", Value: "5"},
		{Name: "PMM_AGENT_PATHS_TEMPDIR", Value: "/tmp/pmm"},
		{Name: "DB_CLUSTER", Value: "test-cluster"},
		{Name: "DB_TYPE", Value: "mysql"},
		{Name: "DB_HOST", Value: "localhost"},
		{Name: "DB_PORT", Value: "33062"},
		{Name: "DB_USER", Value: string(apiv1.UserMonitor)},
		{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: k8s.SecretKeySelector("test-secret", string(apiv1.UserMonitor))}},
		{Name: "DB_ARGS", Value: "--query-source=perfschema"},
	}

	for _, expected := range expectedEnvs {
		assert.Equal(t, expected, envMap[expected.Name])
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

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
		Spec: apiv1.PerconaServerMySQLSpec{
			CRVersion: version.Version(),
			PMM: &apiv1.PMMSpec{
				Image:           "percona/pmm-client:latest",
				ImagePullPolicy: corev1.PullIfNotPresent,
				ServerHost:      "pmm-server",
				LivenessProbe:   lp,
				ReadinessProbe:  rp,
			},
		},
	}

	tests := map[string]struct {
		cr func() *apiv1.PerconaServerMySQL
	}{
		"custom probes >=1.0.0": {
			cr: func() *apiv1.PerconaServerMySQL {
				return cr.DeepCopy()
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "test-secret"},
				Data: map[string][]byte{
					string(apiv1.UserPMMServerToken): []byte("token"),
					string(apiv1.UserMonitor):        []byte("monitor-pass"),
				},
			}

			c := Container(tt.cr(), secret, "mysql", "")

			assert.Equal(t, int32(15), c.LivenessProbe.InitialDelaySeconds)
			assert.Equal(t, int32(7), c.LivenessProbe.TimeoutSeconds)
			assert.Equal(t, int32(11), c.LivenessProbe.PeriodSeconds)
			assert.Equal(t, intstr.FromInt32(7777), c.LivenessProbe.HTTPGet.Port)
			assert.Equal(t, "/local/Status", c.LivenessProbe.HTTPGet.Path)

			assert.Equal(t, int32(5), c.ReadinessProbe.InitialDelaySeconds)
			assert.Equal(t, int32(4), c.ReadinessProbe.TimeoutSeconds)
			assert.Equal(t, int32(12), c.ReadinessProbe.PeriodSeconds)
			assert.Equal(t, intstr.FromInt32(7777), c.ReadinessProbe.HTTPGet.Port)
			assert.Equal(t, "/local/Status", c.ReadinessProbe.HTTPGet.Path)
		})
	}
}
