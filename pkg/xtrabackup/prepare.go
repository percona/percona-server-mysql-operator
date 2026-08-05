package xtrabackup

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
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

func PrepareJobName(restore *apiv1.PerconaServerMySQLRestore) string {
	return fmt.Sprintf("prepare-%s", restore.Name)
}

func PrepareJob(
	cluster *apiv1.PerconaServerMySQL,
	restore *apiv1.PerconaServerMySQLRestore,
	storage *apiv1.BackupStorageSpec,
	initImage string,
) *batchv1.Job {
	labels := util.SSMapMerge(cluster.GlobalLabels(), storage.Labels, restore.Labels(appName, naming.ComponentPrepare))
	pvcName := fmt.Sprintf("%s-%s-mysql-0", mysql.DataVolumeName, cluster.Name)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        PrepareJobName(restore),
			Namespace:   cluster.Namespace,
			Labels:      labels,
			Annotations: util.SSMapMerge(cluster.GlobalAnnotations(), restore.Annotations, storage.Annotations),
		},
		Spec: batchv1.JobSpec{
			Parallelism: ptr.To(int32(1)),
			Completions: ptr.To(int32(1)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: util.SSMapMerge(cluster.GlobalAnnotations(), restore.Annotations, storage.Annotations),
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
						prepareContainer(cluster, storage),
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
					},
				},
			},
			BackoffLimit: cluster.Spec.Backup.BackoffLimit,
		},
	}

	if cluster.Spec.MySQL.VaultSecretName != "" {
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: vaultSecretVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: cluster.Spec.MySQL.VaultSecretName,
					Optional:   ptr.To(true),
				},
			},
		})
	}

	return job
}

func prepareContainer(
	cluster *apiv1.PerconaServerMySQL,
	storage *apiv1.BackupStorageSpec,
) corev1.Container {
	volumeMounts := []corev1.VolumeMount{
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
	}
	if cluster.Spec.MySQL.VaultSecretName != "" {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      vaultSecretVolumeName,
			MountPath: vaultSecretMountPath,
		})
	}

	return corev1.Container{
		Name:            "prepare",
		Image:           cluster.Spec.MySQL.Image,
		ImagePullPolicy: cluster.Spec.MySQL.ImagePullPolicy,
		Env: []corev1.EnvVar{
			{
				Name:  "KEYRING_VAULT_PATH",
				Value: fmt.Sprintf("%s/keyring_vault.cnf", vaultSecretMountPath),
			},
		},
		VolumeMounts:             volumeMounts,
		Command:                  []string{"/opt/percona/run-prepare-restore.sh"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          storage.ContainerSecurityContext,
		Resources:                storage.Resources,
	}
}
