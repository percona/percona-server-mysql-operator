package pitr

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	appName           = "pitr"
	dataVolumeName    = "datadir"
	dataMountPath     = "/var/lib/mysql"
	credsVolumeName   = "users"
	credsMountPath    = "/etc/mysql/mysql-users-secret"
	tlsVolumeName     = "tls"
	tlsMountPath      = "/etc/mysql/mysql-tls-secret"
	binlogsVolumeName = "binlogs"
	binlogsMountPath  = "/etc/pitr"
	BinlogsConfigKey  = "binlogs.json"
)

func JobName(cluster *apiv1.PerconaServerMySQL, restore *apiv1.PerconaServerMySQLRestore) string {
	return fmt.Sprintf("pitr-restore-%s", restore.Name)
}

func BinlogsConfigMapName(restore *apiv1.PerconaServerMySQLRestore) string {
	return fmt.Sprintf("pitr-binlogs-%s", restore.Name)
}

func BinlogsConfigMap(cluster *apiv1.PerconaServerMySQL, restore *apiv1.PerconaServerMySQLRestore) *corev1.ConfigMap {
	labels := util.SSMapMerge(cluster.GlobalLabels(), restore.Labels(appName, naming.ComponentPITR))

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        BinlogsConfigMapName(restore),
			Namespace:   cluster.Namespace,
			Labels:      labels,
			Annotations: cluster.GlobalAnnotations(),
		},
	}
}

func RestoreJob(
	cluster *apiv1.PerconaServerMySQL,
	restore *apiv1.PerconaServerMySQLRestore,
	storage *apiv1.BackupStorageSpec,
	initImage string,
) *batchv1.Job {
	labels := util.SSMapMerge(cluster.GlobalLabels(), storage.Labels, restore.Labels(appName, naming.ComponentPITR))

	pvcName := fmt.Sprintf("%s-%s-mysql-0", mysql.DataVolumeName, cluster.Name)

	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        JobName(cluster, restore),
			Namespace:   cluster.Namespace,
			Labels:      labels,
			Annotations: util.SSMapMerge(cluster.GlobalAnnotations(), storage.Annotations),
		},
		Spec: batchv1.JobSpec{
			Parallelism: ptr.To(int32(1)),
			Completions: ptr.To(int32(1)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: cluster.GlobalAnnotations(),
				},
				Spec: corev1.PodSpec{
					RestartPolicy:    corev1.RestartPolicyNever,
					ImagePullSecrets: cluster.Spec.Backup.ImagePullSecrets,
					InitContainers: []corev1.Container{
						k8s.InitContainer(
							cluster,
							appName,
							initImage,
							cluster.Spec.Backup.InitContainer,
							cluster.Spec.Backup.ImagePullPolicy,
							storage.ContainerSecurityContext,
							cluster.Spec.Backup.Resources,
							[]corev1.VolumeMount{
								{
									Name:      dataVolumeName,
									MountPath: dataMountPath,
								},
								{
									Name:      credsVolumeName,
									MountPath: credsMountPath,
								},
								{
									Name:      tlsVolumeName,
									MountPath: tlsMountPath,
								},
							},
						),
					},
					Containers: []corev1.Container{
						restoreContainer(cluster, restore, storage),
					},
					Affinity:                  storage.Affinity,
					TopologySpreadConstraints: storage.TopologySpreadConstraints,
					Tolerations:               storage.Tolerations,
					NodeSelector:              storage.NodeSelector,
					SchedulerName:             storage.SchedulerName,
					PriorityClassName:         storage.PriorityClassName,
					RuntimeClassName:          storage.RuntimeClassName,
					DNSPolicy:                 corev1.DNSClusterFirst,
					SecurityContext:           storage.PodSecurityContext,
					Volumes: []corev1.Volume{
						{
							Name: apiv1.BinVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: dataVolumeName,
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvcName,
								},
							},
						},
						{
							Name: credsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: cluster.Spec.SecretsName,
								},
							},
						},
						{
							Name: tlsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: cluster.Spec.SSLSecretName,
								},
							},
						},
						{
							Name: binlogsVolumeName,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: BinlogsConfigMapName(restore),
									},
								},
							},
						},
					},
				},
			},
			BackoffLimit: cluster.Spec.Backup.BackoffLimit,
		},
	}
}

func restoreContainer(
	cluster *apiv1.PerconaServerMySQL,
	restore *apiv1.PerconaServerMySQLRestore,
	storage *apiv1.BackupStorageSpec,
) corev1.Container {
	binlogServer := cluster.Spec.Backup.PiTR.BinlogServer

	envs := []corev1.EnvVar{
		{
			Name:  "RESTORE_NAME",
			Value: restore.Name,
		},
		{
			Name:  "BINLOGS_PATH",
			Value: fmt.Sprintf("%s/%s", binlogsMountPath, BinlogsConfigKey),
		},
	}

	if restore.Spec.PITR != nil {
		envs = append(envs, corev1.EnvVar{
			Name:  "PITR_TYPE",
			Value: string(restore.Spec.PITR.Type),
		})
		switch restore.Spec.PITR.Type {
		case apiv1.PITRDate:
			envs = append(envs, corev1.EnvVar{
				Name:  "PITR_DATE",
				Value: restore.Spec.PITR.Date,
			})
		case apiv1.PITRGtid:
			envs = append(envs, corev1.EnvVar{
				Name:  "PITR_GTID",
				Value: restore.Spec.PITR.GTID,
			})
		}
	}

	if binlogServer.Storage.S3 != nil {
		s3 := binlogServer.Storage.S3
		bucket, _ := s3.BucketAndPrefix()
		envs = append(envs,
			corev1.EnvVar{
				Name:  "STORAGE_TYPE",
				Value: "s3",
			},
			corev1.EnvVar{
				Name: "AWS_ACCESS_KEY_ID",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: k8s.SecretKeySelector(s3.CredentialsSecret, secret.CredentialsAWSAccessKey),
				},
			},
			corev1.EnvVar{
				Name: "AWS_SECRET_ACCESS_KEY",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: k8s.SecretKeySelector(s3.CredentialsSecret, secret.CredentialsAWSSecretKey),
				},
			},
			corev1.EnvVar{
				Name:  "AWS_DEFAULT_REGION",
				Value: s3.Region,
			},
			corev1.EnvVar{
				Name:  "AWS_ENDPOINT",
				Value: s3.EndpointURL,
			},
			corev1.EnvVar{
				Name:  "S3_BUCKET",
				Value: bucket,
			},
		)
	}

	envs = append(envs, restore.GetContainerOptions(storage).GetEnv()...)

	return corev1.Container{
		Name:            appName,
		Image:           cluster.Spec.MySQL.Image,
		ImagePullPolicy: cluster.Spec.MySQL.ImagePullPolicy,
		Env:             envs,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      apiv1.BinVolumeName,
				MountPath: apiv1.BinVolumePath,
			},
			{
				Name:      dataVolumeName,
				MountPath: dataMountPath,
			},
			{
				Name:      credsVolumeName,
				MountPath: credsMountPath,
			},
			{
				Name:      tlsVolumeName,
				MountPath: tlsMountPath,
			},
			{
				Name:      binlogsVolumeName,
				MountPath: binlogsMountPath,
			},
		},
		Command:                  []string{"/opt/percona/run-pitr-restore.sh"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          storage.ContainerSecurityContext,
		Resources:                storage.Resources,
	}
}
