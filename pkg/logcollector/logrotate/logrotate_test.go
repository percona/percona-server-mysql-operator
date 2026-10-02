package logrotate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func testCR(lr *apiv1.LogRotateSpec) *apiv1.PerconaServerMySQL {
	return &apiv1.PerconaServerMySQL{
		Spec: apiv1.PerconaServerMySQLSpec{
			LogCollector: &apiv1.LogCollectorSpec{
				Image:           "percona/fluentbit:test",
				ImagePullPolicy: corev1.PullIfNotPresent,
				LogRotate:       lr,
			},
		},
	}
}

func mountNames(mounts []corev1.VolumeMount) []string {
	names := make([]string, 0, len(mounts))
	for _, m := range mounts {
		names = append(names, m.Name)
	}
	return names
}

func envValue(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func TestConfigMapName(t *testing.T) {
	tests := map[string]struct {
		prefix string
		want   string
	}{
		"with cluster name": {prefix: "cluster1", want: "cluster1-log-collector-logrotate-config"},
		"empty prefix":      {prefix: "", want: "log-collector-logrotate-config"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, ConfigMapName(tc.prefix))
		})
	}
}

func TestContainer(t *testing.T) {
	liveness := &corev1.Probe{InitialDelaySeconds: 30}
	readiness := &corev1.Probe{InitialDelaySeconds: 5}

	baseMounts := []corev1.VolumeMount{{Name: "datadir", MountPath: "/var/lib/mysql"}}

	tests := map[string]struct {
		logRotate     *apiv1.LogRotateSpec
		wantSchedule  string
		wantMounts    []string
		wantLiveness  *corev1.Probe
		wantReadiness *corev1.Probe
	}{
		"no logrotate spec": {
			wantSchedule: defaultSchedule,
			wantMounts:   []string{"datadir"},
		},
		"empty logrotate spec": {
			logRotate:    new(apiv1.LogRotateSpec),
			wantSchedule: defaultSchedule,
			wantMounts:   []string{"datadir"},
		},
		"custom schedule": {
			logRotate:    &apiv1.LogRotateSpec{Schedule: "30 3 * * *"},
			wantSchedule: "30 3 * * *",
			wantMounts:   []string{"datadir"},
		},
		"custom configuration mounts conf.d": {
			logRotate:    &apiv1.LogRotateSpec{Configuration: "/x {}"},
			wantSchedule: defaultSchedule,
			wantMounts:   []string{"datadir", VolumeName},
		},
		"extra config mounts conf.d": {
			logRotate: &apiv1.LogRotateSpec{
				ExtraConfig: corev1.LocalObjectReference{Name: "extra"},
			},
			wantSchedule: defaultSchedule,
			wantMounts:   []string{"datadir", VolumeName},
		},
		"probes are taken from the spec": {
			logRotate:     &apiv1.LogRotateSpec{LivenessProbe: liveness, ReadinessProbe: readiness},
			wantSchedule:  defaultSchedule,
			wantMounts:    []string{"datadir"},
			wantLiveness:  liveness,
			wantReadiness: readiness,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := testCR(tc.logRotate)

			c := Container(cr, "/var/lib/mysql/log", baseMounts)

			assert.Equal(t, ContainerName, c.Name)
			assert.Equal(t, cr.Spec.LogCollector.Image, c.Image)
			assert.Equal(t, cr.Spec.LogCollector.ImagePullPolicy, c.ImagePullPolicy)
			assert.Equal(t, []string{entrypoint}, c.Command)
			assert.Equal(t, []string{"logrotate"}, c.Args)
			assert.Equal(t, tc.wantSchedule, envValue(c.Env, "LOGROTATE_SCHEDULE"))
			assert.Equal(t, "/var/lib/mysql/log", envValue(c.Env, logDirEnvVar),
				"the entrypoint resolves its status file under LOG_DIR")
			assert.Equal(t, tc.wantMounts, mountNames(c.VolumeMounts))
			assert.Equal(t, tc.wantLiveness, c.LivenessProbe)
			assert.Equal(t, tc.wantReadiness, c.ReadinessProbe)
		})
	}
}

func TestContainerPodEnvUsesDownwardAPI(t *testing.T) {
	c := Container(testCR(nil), "/var/lib/mysql/log", nil)

	for _, name := range []string{"POD_NAME", "POD_NAMESPACE"} {
		var found bool
		for _, e := range c.Env {
			if e.Name != name {
				continue
			}
			found = true
			require.NotNil(t, e.ValueFrom)
			require.NotNil(t, e.ValueFrom.FieldRef)
			assert.Empty(t, e.Value)
		}
		assert.True(t, found, "env %s missing", name)
	}
}
