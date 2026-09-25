package logcollector

import (
	"path/filepath"

	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/logcollector/logrotate"
)

const (
	// ContainerName is the name of the Fluent Bit sidecar.
	ContainerName = apiv1.LogCollectorContainerName

	// Component is the last segment of the Fluent Bit tag.
	Component = "mysql"

	// EnabledEnvVar tells ps-entrypoint.sh to point log_error at a file on the
	// data volume instead of leaving it on stderr.
	EnabledEnvVar = "LOG_COLLECTOR_ENABLED"

	// LogDirEnvVar holds the directory that mysqld writes its logs to.
	LogDirEnvVar = "LOG_DIR"

	// HTTPPort is the port Fluent Bit's HTTP server listens on.
	HTTPPort = 2020

	// LogDirName is the log directory, relative to the MySQL data directory. It
	// lives on the data volume so the logs survive a pod restart.
	LogDirName = "log"

	configMapNameSuffix              = "log-collector-config"
	configVolumeName                 = "log-collector-volume"
	fluentBitCustomConfigurationFile = "fluentbit_custom.yaml"

	binDir                = apiv1.BinVolumePath + "/logcollector"
	entrypoint            = binDir + "/entrypoint.sh"
	customConfigMountPath = binDir + "/fluentbit/custom"
)

// LogDir returns the directory mysqld writes its logs to.
func LogDir(dataMountPath string) string {
	return filepath.Join(dataMountPath, LogDirName)
}

// ConfigMapName returns the name of the ConfigMap holding the custom Fluent Bit
// configuration.
func ConfigMapName(prefix string) string {
	if prefix == "" {
		return configMapNameSuffix
	}
	return prefix + "-" + configMapNameSuffix
}

// MySQLEnv returns the environment the mysqld container needs so its entrypoint
// writes the error log to a file the collector can tail.
func MySQLEnv(cr *apiv1.PerconaServerMySQL, dataMountPath string) []corev1.EnvVar {
	if !cr.LogCollectorEnabled() {
		return nil
	}

	return []corev1.EnvVar{
		{Name: EnabledEnvVar, Value: "true"},
		{Name: LogDirEnvVar, Value: LogDir(dataMountPath)},
	}
}

// InitEnv returns the environment the init container needs so it can fail loudly
// when log collection is on but the init image does not ship the log collector
// assets, instead of leaving the sidecars to crash-loop on a missing entrypoint.
func InitEnv(cr *apiv1.PerconaServerMySQL) []corev1.EnvVar {
	if !cr.LogCollectorEnabled() {
		return nil
	}

	return []corev1.EnvVar{{Name: EnabledEnvVar, Value: "true"}}
}

// Volumes returns the pod-level volumes the log collector sidecars require. The
// log directory itself needs no volume of its own: it lives on the data volume
// the MySQL pod already mounts.
func Volumes(cr *apiv1.PerconaServerMySQL) []corev1.Volume {
	if !cr.LogCollectorEnabled() {
		return nil
	}

	var vols []corev1.Volume

	if cr.Spec.LogCollector.Configuration != "" {
		vols = append(vols, corev1.Volume{
			Name: configVolumeName,
			ConfigMap: &corev1.ConfigMapVolumeSource{
				Name: ConfigMapName(cr.Name),
			},
		})
	}

	if v := logrotateVolume(cr); v != nil {
		vols = append(vols, *v)
	}

	return append(vols, cr.Spec.LogCollector.Volumes...)
}

func logrotateVolume(cr *apiv1.PerconaServerMySQL) *corev1.Volume {
	lr := cr.Spec.LogCollector.LogRotate
	if lr == nil || (lr.Configuration == "" && lr.ExtraConfig.Name == "") {
		return nil
	}

	var sources []corev1.VolumeProjection
	if lr.Configuration != "" {
		sources = append(sources, corev1.VolumeProjection{
			ConfigMap: &corev1.ConfigMapProjection{
				Name: logrotate.ConfigMapName(cr.Name),
			},
		})
	}
	if lr.ExtraConfig.Name != "" {
		sources = append(sources, corev1.VolumeProjection{
			ConfigMap: &corev1.ConfigMapProjection{
				LocalObjectReference: lr.ExtraConfig,
			},
		})
	}

	return &corev1.Volume{
		Name:      logrotate.VolumeName,
		Projected: &corev1.ProjectedVolumeSource{Sources: sources},
	}
}

// Containers returns the Fluent Bit and logrotate sidecars for a MySQL pod.
// dataMount is the MySQL data volume, which holds the logs to tail;
// backupLogsMount is the xtrabackup sidecar's log volume on the same pod.
func Containers(cr *apiv1.PerconaServerMySQL, dataMount, backupLogsMount corev1.VolumeMount) []corev1.Container {
	if !cr.LogCollectorEnabled() {
		return nil
	}

	// Each container gets its own mount slice: both append their own config
	// mount to it, and a shared backing array would let one overwrite the other.
	return []corev1.Container{
		logContainer(cr, dataMount, backupLogsMount, sidecarMounts(cr, dataMount, backupLogsMount)),
		logrotate.Container(cr, LogDir(dataMount.MountPath), sidecarMounts(cr, dataMount, backupLogsMount)),
	}
}

func logContainer(cr *apiv1.PerconaServerMySQL, dataMount, backupLogsMount corev1.VolumeMount, mounts []corev1.VolumeMount) corev1.Container {
	container := corev1.Container{
		Name:            ContainerName,
		Image:           cr.Spec.LogCollector.Image,
		ImagePullPolicy: cr.Spec.LogCollector.ImagePullPolicy,
		SecurityContext: cr.Spec.LogCollector.ContainerSecurityContext,
		Resources:       cr.Spec.LogCollector.Resources,
		LivenessProbe:   cr.Spec.LogCollector.LivenessProbe,
		ReadinessProbe:  cr.Spec.LogCollector.ReadinessProbe,
		Command:         []string{entrypoint},
		Args:            []string{"fluent-bit"},
		Env:             append(collectorEnv(dataMount, backupLogsMount), cr.Spec.LogCollector.Env...),
		EnvFrom:         cr.Spec.LogCollector.EnvFrom,
		VolumeMounts:    mounts,
	}

	if cr.Spec.LogCollector.Configuration != "" {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      configVolumeName,
			MountPath: customConfigMountPath,
		})
	}

	return container
}

func sidecarMounts(cr *apiv1.PerconaServerMySQL, dataMount, backupLogsMount corev1.VolumeMount) []corev1.VolumeMount {
	custom := cr.Spec.LogCollector.VolumeMounts

	mounts := make([]corev1.VolumeMount, 0, 3+len(custom))
	mounts = append(mounts,
		dataMount,
		corev1.VolumeMount{Name: apiv1.BinVolumeName, MountPath: apiv1.BinVolumePath},
		backupLogsMount,
	)

	return append(mounts, custom...)
}

func collectorEnv(dataMount, backupLogsMount corev1.VolumeMount) []corev1.EnvVar {
	return append([]corev1.EnvVar{
		{Name: "LOG_COMPONENT", Value: Component},
		{Name: LogDirEnvVar, Value: LogDir(dataMount.MountPath)},
		{Name: "XTRABACKUP_LOG_DIR", Value: backupLogsMount.MountPath},
	}, podEnv()...)
}

func podEnv() []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
		{
			Name: "POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		},
	}
}
