package pitr

import (
	"fmt"
	"path"
	"path/filepath"

	"github.com/pkg/errors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver"
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
	keyringVolumeName = "keyring"
	keyringMountPath  = "/etc/binlog_server/keyring"

	binlogsVolumeName = "binlogs"
	binlogsMountPath  = "/var/lib/pitr-binlogs"
	binlogsFileName   = "binlogs.json"

	vaultSecretVolumeName = "vault-keyring-secret"
	vaultSecretMountPath  = "/etc/mysql/vault-keyring-secret"
)

func JobName(restore *apiv1.PerconaServerMySQLRestore) string {
	return fmt.Sprintf("pitr-restore-%s", restore.Name)
}

func getKeyringSecretRef(
	cluster *apiv1.PerconaServerMySQL,
	restore *apiv1.PerconaServerMySQLRestore,
) *apiv1.BinlogServerKeyringSecretSelector {
	if restore.Spec.PITR != nil && restore.Spec.PITR.KeyringSecret != nil {
		return restore.Spec.PITR.KeyringSecret
	}

	binlogSrv := cluster.Spec.Backup.PiTR.BinlogServer
	if binlogSrv != nil && binlogSrv.KeyringSecret != nil {
		return binlogSrv.KeyringSecret
	}
	return nil
}

func RestoreJob(
	cluster *apiv1.PerconaServerMySQL,
	restore *apiv1.PerconaServerMySQLRestore,
	storage *apiv1.BackupStorageSpec,
	initImage string,
) (*batchv1.Job, error) {
	labels := util.SSMapMerge(cluster.GlobalLabels(), storage.Labels, restore.Labels(appName, naming.ComponentPITR))
	binlogServer := binlogserver.RestoreSpec(cluster, restore)
	subcommand, arg, err := binlogserver.SearchArgs(restore)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get search args")
	}

	pvcName := fmt.Sprintf("%s-%s-mysql-0", mysql.DataVolumeName, cluster.Name)

	job := &batchv1.Job{
		APIVersion:  "batch/v1",
		Kind:        "Job",
		Name:        JobName(restore),
		Namespace:   cluster.Namespace,
		Labels:      labels,
		Annotations: util.SSMapMerge(cluster.GlobalAnnotations(), restore.Annotations, storage.Annotations),
		Spec: batchv1.JobSpec{
			Parallelism: new(int32(1)),
			Completions: new(int32(1)),
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
						restoreContainer(cluster, restore, storage, arg),
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
							Name:     apiv1.BinVolumeName,
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
						{
							Name: dataVolumeName,
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: pvcName,
							},
						},
						{
							Name: credsVolumeName,
							Secret: &corev1.SecretVolumeSource{
								SecretName: cluster.Spec.SecretsName,
							},
						},
						{
							Name: tlsVolumeName,
							Secret: &corev1.SecretVolumeSource{
								SecretName: cluster.Spec.SSLSecretName,
							},
						},
						{
							Name:     binlogsVolumeName,
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					},
				},
			},
			BackoffLimit: cluster.Spec.Backup.BackoffLimit,
		},
	}

	if binlogServer != nil {
		job.Spec.Template.Spec.InitContainers = append(job.Spec.Template.Spec.InitContainers, binlogserver.SearchContainer(binlogServer, subcommand, arg, binlogsVolumeName, binlogsMountPath, binlogsFileName))
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes,
			binlogserver.SearchVolumes(cluster, binlogServer, binlogserver.RestoreConfigSecretName(cluster, restore))...)

		var imagePullSecrets []corev1.LocalObjectReference
		seen := make(map[string]struct{})
		for _, list := range [][]corev1.LocalObjectReference{job.Spec.Template.Spec.ImagePullSecrets, binlogServer.ImagePullSecrets} {
			for _, secret := range list {
				if _, ok := seen[secret.Name]; ok {
					continue
				}
				seen[secret.Name] = struct{}{}
				imagePullSecrets = append(imagePullSecrets, secret)
			}
		}
		job.Spec.Template.Spec.ImagePullSecrets = imagePullSecrets

		k8s.PrepareJobWithS3CA(job, binlogServer.Storage.S3)
		k8s.PrepareInitContainersWithS3CA(job, cluster, binlogServer.Storage.S3)
	}

	if keyringSecretRef := getKeyringSecretRef(cluster, restore); keyringSecretRef != nil {
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: keyringVolumeName,
			Secret: &corev1.SecretVolumeSource{
				SecretName: keyringSecretRef.Name,
			},
		})
	}

	// mysqld replays the binlogs on the restored datadir, so it needs the same
	// vault keyring the cluster runs with to open its encrypted tablespaces.
	if cluster.Spec.MySQL.VaultSecretName != "" {
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: vaultSecretVolumeName,
			Secret: &corev1.SecretVolumeSource{
				SecretName: cluster.Spec.MySQL.VaultSecretName,
				Optional:   new(true),
			},
		})
	}

	return job, nil
}

func restoreContainer(
	cluster *apiv1.PerconaServerMySQL,
	restore *apiv1.PerconaServerMySQLRestore,
	storage *apiv1.BackupStorageSpec,
	searchArg string,
) corev1.Container {
	binlogServer := binlogserver.RestoreSpec(cluster, restore)

	envs := []corev1.EnvVar{
		{
			Name:  "RESTORE_NAME",
			Value: restore.Name,
		},
		{
			Name:  "BINLOGS_PATH",
			Value: path.Join(binlogsMountPath, binlogsFileName),
		},
	}

	if _, ok := restore.Annotations["percona.com/pitr-sleep-forever"]; ok {
		envs = append(envs, corev1.EnvVar{
			Name:  "SLEEP_FOREVER",
			Value: "true",
		})
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
				Value: searchArg,
			})
		case apiv1.PITRGtid:
			envs = append(envs, corev1.EnvVar{
				Name:  "PITR_GTID",
				Value: searchArg,
			})
		}
		if restore.Spec.PITR.Force {
			envs = append(envs, corev1.EnvVar{
				Name:  "PITR_FORCE",
				Value: "true",
			})
		}
	}

	if binlogServer.Storage.S3 != nil {
		s3 := binlogServer.Storage.S3
		bucket, _ := s3.BucketAndPrefix()
		envs = append(
			envs,
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

	c := corev1.Container{
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

	if keyringSecretRef := getKeyringSecretRef(cluster, restore); keyringSecretRef != nil {
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      keyringVolumeName,
			MountPath: keyringMountPath,
		})
		c.Env = append(c.Env, corev1.EnvVar{
			Name:  "KEYRING_PATH",
			Value: filepath.Join(keyringMountPath, keyringSecretRef.Key),
		})
	}

	if cluster.Spec.MySQL.VaultSecretName != "" {
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      vaultSecretVolumeName,
			MountPath: vaultSecretMountPath,
		})
		c.Env = append(c.Env, corev1.EnvVar{
			Name:  "KEYRING_VAULT_PATH",
			Value: filepath.Join(vaultSecretMountPath, "keyring_vault.cnf"),
		})
	}

	return c
}
