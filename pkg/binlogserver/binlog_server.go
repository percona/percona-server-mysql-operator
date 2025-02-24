package binlogserver

import (
	"path"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	ComponentName          = "binlog-server"
	credsVolumeName        = "users"
	CredsMountPath         = "/etc/mysql/mysql-users-secret"
	tlsVolumeName          = "tls"
	tlsMountPath           = "/etc/mysql/mysql-tls-secret"
	configVolumeName       = "config"
	configMountPath        = "/etc/binlog_server/config"
	storageCredsVolumeName = "storage"
	ConfigKey              = "config.json"
	CustomConfigKey        = "custom.json"
)

func Name(cr *apiv1alpha1.PerconaServerMySQL) string {
	return cr.Name + "-" + ComponentName
}

func CustomConfigMapName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return cr.Name + "-" + ComponentName + "-config"
}

func ConfigSecretName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return cr.Name + "-" + ComponentName + "-config"
}

func MatchLabels(cr *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(
		cr.MySQLSpec().Labels,
		map[string]string{naming.LabelComponent: ComponentName},
		cr.Labels(),
	)
}

func StatefulSet(cr *apiv1alpha1.PerconaServerMySQL, initImage, configHash string) *appsv1.StatefulSet {
	spec := cr.Spec.Backup.PiTR.BinlogServer

	labels := MatchLabels(cr)

	annotations := make(map[string]string)
	if configHash != "" {
		annotations[string(naming.AnnotationConfigHash)] = configHash
	}

	t := true

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      Name(cr),
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &spec.Size,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						k8s.InitContainer(
							ComponentName,
							initImage,
							spec.ImagePullPolicy,
							spec.ContainerSecurityContext,
							spec.Resources,
							nil,
						),
					},
					Containers:                    containers(cr),
					ServiceAccountName:            spec.ServiceAccountName,
					NodeSelector:                  spec.NodeSelector,
					Tolerations:                   spec.Tolerations,
					Affinity:                      spec.GetAffinity(labels),
					TopologySpreadConstraints:     spec.GetTopologySpreadConstraints(labels),
					ImagePullSecrets:              spec.ImagePullSecrets,
					TerminationGracePeriodSeconds: spec.TerminationGracePeriodSeconds,
					PriorityClassName:             spec.PriorityClassName,
					RuntimeClassName:              spec.RuntimeClassName,
					RestartPolicy:                 corev1.RestartPolicyAlways,
					SchedulerName:                 spec.SchedulerName,
					DNSPolicy:                     corev1.DNSClusterFirst,
					Volumes: []corev1.Volume{
						{
							Name: apiv1alpha1.BinVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: credsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: cr.InternalSecretName(),
								},
							},
						},
						{
							Name: tlsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: cr.Spec.SSLSecretName,
								},
							},
						},
						{
							Name: storageCredsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: spec.Storage.S3.CredentialsSecret,
								},
							},
						},
						{
							Name: configVolumeName,
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											Secret: &corev1.SecretProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: ConfigSecretName(cr),
												},
												Items: []corev1.KeyToPath{
													{
														Key:  ConfigKey,
														Path: ConfigKey,
													},
												},
											},
										},
										{
											ConfigMap: &corev1.ConfigMapProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: CustomConfigMapName(cr),
												},
												Items: []corev1.KeyToPath{
													{
														Key:  CustomConfigKey,
														Path: CustomConfigKey,
													},
												},
												Optional: &t,
											},
										},
									},
								},
							},
						},
					},
					SecurityContext: spec.PodSecurityContext,
				},
			},
		},
	}
}

func containers(cr *apiv1alpha1.PerconaServerMySQL) []corev1.Container {
	return []corev1.Container{binlogServerContainer(cr)}
}

func binlogServerContainer(cr *apiv1alpha1.PerconaServerMySQL) corev1.Container {
	spec := cr.Spec.Backup.PiTR.BinlogServer

	env := []corev1.EnvVar{
		{
			Name:  "CONFIG_PATH",
			Value: path.Join(configMountPath, ConfigKey),
		},
		{
			Name:  "CUSTOM_CONFIG_PATH",
			Value: path.Join(configMountPath, CustomConfigKey),
		},
	}
	env = append(env, spec.Env...)

	return corev1.Container{
		Name:            ComponentName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Resources:       spec.Resources,
		Env:             env,
		EnvFrom:         spec.EnvFrom,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      apiv1alpha1.BinVolumeName,
				MountPath: apiv1alpha1.BinVolumePath,
			},
			{
				Name:      credsVolumeName,
				MountPath: CredsMountPath,
			},
			{
				Name:      tlsVolumeName,
				MountPath: tlsMountPath,
			},
			{
				Name:      configVolumeName,
				MountPath: configMountPath,
			},
		},
		Command:                  []string{"/opt/percona/binlog-server-entrypoint.sh"},
		Args:                     []string{"/usr/local/bin/binlog_server", "pull", path.Join(configMountPath, ConfigKey)},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
	}
}
