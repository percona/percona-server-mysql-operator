package logcollector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/logcollector/logrotate"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

const (
	testDataMountPath   = "/var/lib/mysql"
	testBackupLogDir    = "/var/log/xtrabackup"
	testClusterName     = "cluster1"
	testCollectorImage  = "percona/fluentbit:test"
	testOldCRVersion    = "1.2.0"
	testDataVolumeName  = "datadir"
	testBackupLogVolume = "backup-logs"
)

func testDataMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: testDataVolumeName, MountPath: testDataMountPath}
}

func testBackupLogsMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: testBackupLogVolume, MountPath: testBackupLogDir}
}

func testCR(update ...func(cr *apiv1.PerconaServerMySQL)) *apiv1.PerconaServerMySQL {
	cr := &apiv1.PerconaServerMySQL{
		Name:      testClusterName,
		Namespace: "ns",
		Spec: apiv1.PerconaServerMySQLSpec{
			CRVersion: version.Version(),
			LogCollector: &apiv1.LogCollectorSpec{
				Enabled:         new(true),
				Image:           testCollectorImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
			},
		},
	}
	for _, f := range update {
		if f != nil {
			f(cr)
		}
	}
	return cr
}

func containerByName(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()

	for _, c := range containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("container %q not found", name)
	return corev1.Container{}
}

func volumeNames(vols []corev1.Volume) []string {
	if len(vols) == 0 {
		return nil
	}
	names := make([]string, 0, len(vols))
	for _, v := range vols {
		names = append(names, v.Name)
	}
	return names
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

func TestLogDir(t *testing.T) {
	assert.Equal(t, "/var/lib/mysql/log", LogDir("/var/lib/mysql"))
	assert.Equal(t, "/data/log", LogDir("/data"))
}

func TestConfigMapName(t *testing.T) {
	tests := map[string]struct {
		prefix string
		want   string
	}{
		"with cluster name": {prefix: "cluster1", want: "cluster1-log-collector-config"},
		"empty prefix":      {prefix: "", want: "log-collector-config"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, ConfigMapName(tc.prefix))
		})
	}
}

func TestMySQLEnv(t *testing.T) {
	tests := map[string]struct {
		cr   *apiv1.PerconaServerMySQL
		want []corev1.EnvVar
	}{
		"enabled": {
			cr: testCR(),
			want: []corev1.EnvVar{
				{Name: EnabledEnvVar, Value: "true"},
				{Name: LogDirEnvVar, Value: "/var/lib/mysql/log"},
			},
		},
		"explicitly disabled": {
			cr: testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.Enabled = new(false)
			}),
		},
		"enabled unset": {
			cr: testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.Enabled = nil
			}),
		},
		"spec absent": {
			cr: testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector = nil
			}),
		},
		"cr version too old": {
			cr: testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.CRVersion = testOldCRVersion
			}),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, MySQLEnv(tc.cr, testDataMountPath))
		})
	}
}

func TestVolumes(t *testing.T) {
	tests := map[string]struct {
		cr        *apiv1.PerconaServerMySQL
		wantNames []string
	}{
		"disabled": {
			cr: testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.Enabled = new(false)
			}),
		},
		"cr version too old": {
			cr: testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.CRVersion = testOldCRVersion
			}),
		},
		"no custom config": {
			cr: testCR(),
		},
		"fluent-bit config only": {
			cr: testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.Configuration = "pipeline: {}"
			}),
			wantNames: []string{configVolumeName},
		},
		"logrotate config only": {
			cr: testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{Configuration: "/x {}"}
			}),
			wantNames: []string{logrotate.VolumeName},
		},
		"logrotate extra config only": {
			cr: testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{
					ExtraConfig: corev1.LocalObjectReference{Name: "extra"},
				}
			}),
			wantNames: []string{logrotate.VolumeName},
		},
		"empty logrotate spec adds no volume": {
			cr: testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{Schedule: "0 0 * * *"}
			}),
		},
		"all sources plus user volumes": {
			cr: testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.Configuration = "pipeline: {}"
				cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{
					Configuration: "/x {}",
					ExtraConfig:   corev1.LocalObjectReference{Name: "extra"},
				}
				cr.Spec.LogCollector.Volumes = []corev1.Volume{{Name: "s3-ca"}}
			}),
			wantNames: []string{configVolumeName, logrotate.VolumeName, "s3-ca"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := Volumes(tc.cr)
			assert.Equal(t, tc.wantNames, volumeNames(got))
		})
	}
}

func TestVolumesLogRotateProjection(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{
			Configuration: "/x {}",
			ExtraConfig:   corev1.LocalObjectReference{Name: "extra"},
		}
	})

	vols := Volumes(cr)
	require.Len(t, vols, 1)
	require.NotNil(t, vols[0].Projected)
	require.Len(t, vols[0].Projected.Sources, 2)

	assert.Equal(t, logrotate.ConfigMapName(testClusterName), vols[0].Projected.Sources[0].ConfigMap.Name)
	assert.Equal(t, "extra", vols[0].Projected.Sources[1].ConfigMap.Name)
}

func TestContainersDisabled(t *testing.T) {
	tests := map[string]*apiv1.PerconaServerMySQL{
		"explicitly disabled": testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Enabled = new(false)
		}),
		"enabled unset": testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Enabled = nil
		}),
		"spec absent": testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector = nil
		}),
		"cr version too old": testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.CRVersion = testOldCRVersion
		}),
	}

	for name, cr := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, Containers(cr, testDataMount(), testBackupLogsMount()))
		})
	}
}

func TestContainersLogContainer(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.LogCollector.Resources = corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("200M")},
		}
		cr.Spec.LogCollector.LivenessProbe = &corev1.Probe{InitialDelaySeconds: 30}
		cr.Spec.LogCollector.ReadinessProbe = &corev1.Probe{InitialDelaySeconds: 5}
		cr.Spec.LogCollector.ContainerSecurityContext = &corev1.SecurityContext{Privileged: new(false)}
		cr.Spec.LogCollector.Env = []corev1.EnvVar{{Name: "EXTRA", Value: "1"}}
		cr.Spec.LogCollector.EnvFrom = []corev1.EnvFromSource{{
			SecretRef: &corev1.SecretEnvSource{Name: "s"},
		}}
	})

	containers := Containers(cr, testDataMount(), testBackupLogsMount())
	require.Len(t, containers, 2)

	logs := containerByName(t, containers, ContainerName)

	assert.Equal(t, testCollectorImage, logs.Image)
	assert.Equal(t, corev1.PullIfNotPresent, logs.ImagePullPolicy)
	assert.Equal(t, []string{"/opt/percona/logcollector/entrypoint.sh"}, logs.Command)
	assert.Equal(t, []string{"fluent-bit"}, logs.Args)
	assert.Equal(t, cr.Spec.LogCollector.Resources, logs.Resources)
	assert.Equal(t, cr.Spec.LogCollector.LivenessProbe, logs.LivenessProbe)
	assert.Equal(t, cr.Spec.LogCollector.ReadinessProbe, logs.ReadinessProbe)
	assert.Equal(t, cr.Spec.LogCollector.ContainerSecurityContext, logs.SecurityContext)
	assert.Equal(t, cr.Spec.LogCollector.EnvFrom, logs.EnvFrom)

	assert.Equal(t, "mysql", envValue(logs.Env, "LOG_COMPONENT"))
	assert.Equal(t, "/var/lib/mysql/log", envValue(logs.Env, LogDirEnvVar))
	assert.Equal(t, testBackupLogDir, envValue(logs.Env, "XTRABACKUP_LOG_DIR"))
	assert.Equal(t, "1", envValue(logs.Env, "EXTRA"))

	// POD_NAME and POD_NAMESPACE come from the downward API, not a literal value.
	for _, name := range []string{"POD_NAME", "POD_NAMESPACE"} {
		var found bool
		for _, e := range logs.Env {
			if e.Name == name {
				found = true
				require.NotNil(t, e.ValueFrom)
				assert.NotNil(t, e.ValueFrom.FieldRef)
			}
		}
		assert.True(t, found, "env %s missing", name)
	}

	assert.Equal(t,
		[]string{testDataVolumeName, apiv1.BinVolumeName, testBackupLogVolume},
		mountNames(logs.VolumeMounts),
	)
}

func TestContainersCustomConfigMount(t *testing.T) {
	tests := map[string]struct {
		configuration string
		wantMounts    []string
	}{
		"no custom config": {
			wantMounts: []string{testDataVolumeName, apiv1.BinVolumeName, testBackupLogVolume},
		},
		"custom config mounted": {
			configuration: "pipeline: {}",
			wantMounts:    []string{testDataVolumeName, apiv1.BinVolumeName, testBackupLogVolume, configVolumeName},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.Configuration = tc.configuration
			})

			logs := containerByName(t, Containers(cr, testDataMount(), testBackupLogsMount()), ContainerName)
			assert.Equal(t, tc.wantMounts, mountNames(logs.VolumeMounts))
		})
	}
}

func TestContainersUserVolumeMounts(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.LogCollector.VolumeMounts = []corev1.VolumeMount{{Name: "s3-ca", MountPath: "/etc/fluentbit/tls"}}
	})

	containers := Containers(cr, testDataMount(), testBackupLogsMount())
	require.Len(t, containers, 2)

	for _, c := range containers {
		assert.Contains(t, mountNames(c.VolumeMounts), "s3-ca", "container %s", c.Name)
	}
}

func TestContainersLogRotateContainer(t *testing.T) {
	tests := map[string]struct {
		logRotate    *apiv1.LogRotateSpec
		wantSchedule string
		wantMounts   []string
	}{
		"no logrotate spec": {
			wantSchedule: "0 0 * * *",
			wantMounts:   []string{testDataVolumeName, apiv1.BinVolumeName, testBackupLogVolume},
		},
		"custom schedule": {
			logRotate:    &apiv1.LogRotateSpec{Schedule: "30 3 * * *"},
			wantSchedule: "30 3 * * *",
			wantMounts:   []string{testDataVolumeName, apiv1.BinVolumeName, testBackupLogVolume},
		},
		"custom configuration mounts conf.d": {
			logRotate:    &apiv1.LogRotateSpec{Configuration: "/x {}"},
			wantSchedule: "0 0 * * *",
			wantMounts:   []string{testDataVolumeName, apiv1.BinVolumeName, testBackupLogVolume, logrotate.VolumeName},
		},
		"extra config mounts conf.d": {
			logRotate: &apiv1.LogRotateSpec{
				ExtraConfig: corev1.LocalObjectReference{Name: "extra"},
			},
			wantSchedule: "0 0 * * *",
			wantMounts:   []string{testDataVolumeName, apiv1.BinVolumeName, testBackupLogVolume, logrotate.VolumeName},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.LogRotate = tc.logRotate
			})

			rotate := containerByName(t, Containers(cr, testDataMount(), testBackupLogsMount()), logrotate.ContainerName)

			assert.Equal(t, []string{"/opt/percona/logcollector/entrypoint.sh"}, rotate.Command)
			assert.Equal(t, []string{"logrotate"}, rotate.Args)
			assert.Equal(t, tc.wantSchedule, envValue(rotate.Env, "LOGROTATE_SCHEDULE"))
			assert.Equal(t, tc.wantMounts, mountNames(rotate.VolumeMounts))
		})
	}
}

func TestContainersDoNotShareVolumeMounts(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.LogCollector.Configuration = "pipeline: {}"
		cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{Configuration: "/x {}"}
		cr.Spec.LogCollector.VolumeMounts = []corev1.VolumeMount{
			{Name: "s3-ca", MountPath: "/etc/fluentbit/tls"},
		}
	})

	logs := containerByName(t, Containers(cr, testDataMount(), testBackupLogsMount()), ContainerName)
	rotate := containerByName(t, Containers(cr, testDataMount(), testBackupLogsMount()), logrotate.ContainerName)

	assert.Equal(t,
		[]string{testDataVolumeName, apiv1.BinVolumeName, testBackupLogVolume, "s3-ca", configVolumeName},
		mountNames(logs.VolumeMounts),
	)
	assert.Equal(t,
		[]string{testDataVolumeName, apiv1.BinVolumeName, testBackupLogVolume, "s3-ca", logrotate.VolumeName},
		mountNames(rotate.VolumeMounts),
	)
}

func TestContainersLogRotateLogDir(t *testing.T) {
	rotate := containerByName(t,
		Containers(testCR(), testDataMount(), testBackupLogsMount()),
		logrotate.ContainerName,
	)

	assert.Equal(t, LogDir(testDataMountPath), envValue(rotate.Env, LogDirEnvVar))
}
