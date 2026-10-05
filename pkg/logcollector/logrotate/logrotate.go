package logrotate

import (
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

const (
	// ContainerName is the name of the logrotate sidecar.
	ContainerName = apiv1.LogRotateContainerName

	// VolumeName holds the operator-managed logrotate config and any extra
	// config supplied through logRotate.extraConfig.
	VolumeName = "log-collector-logrotate-volume"

	// MySQLConfig is the ConfigMap key holding the logrotate configuration.
	MySQLConfig = "mysql.conf"

	// logDirEnvVar mirrors logcollector.LogDirEnvVar, which this package cannot
	// import without an import cycle. The entrypoint resolves the logrotate
	// status file under it, so the container fails to start when it is unset.
	logDirEnvVar = "LOG_DIR"

	configMapNameSuffix = "log-collector-logrotate-config"

	defaultSchedule = "0 0 * * *"

	binDir          = apiv1.BinVolumePath + "/logcollector"
	entrypoint      = binDir + "/entrypoint.sh"
	configMountPath = binDir + "/logrotate/conf.d"
)

// ConfigMapName returns the name of the ConfigMap holding the logrotate
// configuration.
func ConfigMapName(prefix string) string {
	if prefix == "" {
		return configMapNameSuffix
	}
	return prefix + "-" + configMapNameSuffix
}

// Container returns the logrotate sidecar. logDir is the directory holding the
// logs to rotate; mounts must already contain the volume it lives on.
func Container(cr *apiv1.PerconaServerMySQL, logDir string, mounts []corev1.VolumeMount) corev1.Container {
	container := corev1.Container{
		Name:            ContainerName,
		Image:           cr.Spec.LogCollector.Image,
		ImagePullPolicy: cr.Spec.LogCollector.ImagePullPolicy,
		SecurityContext: cr.Spec.LogCollector.ContainerSecurityContext,
		Resources:       cr.Spec.LogCollector.Resources,
		LivenessProbe:   livenessProbe(cr.Spec.LogCollector.LogRotate),
		ReadinessProbe:  readinessProbe(cr.Spec.LogCollector.LogRotate),
		Command:         []string{entrypoint},
		Args:            []string{"logrotate"},
		Env: []corev1.EnvVar{
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
			{
				Name:  logDirEnvVar,
				Value: logDir,
			},
			{
				Name:  "LOGROTATE_SCHEDULE",
				Value: schedule(cr.Spec.LogCollector.LogRotate),
			},
		},
		VolumeMounts: mounts,
	}

	if lr := cr.Spec.LogCollector.LogRotate; lr != nil {
		if lr.Configuration != "" || lr.ExtraConfig.Name != "" {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      VolumeName,
				MountPath: configMountPath,
			})
		}
	}

	return container
}

func schedule(lr *apiv1.LogRotateSpec) string {
	if lr != nil && lr.Schedule != "" {
		return lr.Schedule
	}
	return defaultSchedule
}

func livenessProbe(lr *apiv1.LogRotateSpec) *corev1.Probe {
	if lr == nil {
		return nil
	}
	return lr.LivenessProbe
}

func readinessProbe(lr *apiv1.LogRotateSpec) *corev1.Probe {
	if lr == nil {
		return nil
	}
	return lr.ReadinessProbe
}
