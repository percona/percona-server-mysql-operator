package xtrabackup

import (
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
	"github.com/pkg/errors"
)

const (
	componentName      = "xtrabackup"
	componentShortName = "xb"
	dataVolumeName     = "datadir"
	dataMountPath      = "/var/lib/mysql"
	credsVolumeName    = "users"
	credsMountPath     = "/etc/mysql/mysql-users-secret"
	tlsVolumeName      = "tls"
	tlsMountPath       = "/etc/mysql/mysql-tls-secret"
	backupVolumeName   = componentName
	backupMountPath    = "/backup"
)

func Name(cr *apiv1alpha1.PerconaServerMySQLBackup) string {
	return componentShortName + "-" + cr.Name + "-" + cr.Spec.StorageName
}

func RestoreName(cr *apiv1alpha1.PerconaServerMySQLRestore) string {
	return componentShortName + "-restore-" + cr.Name
}

func NamespacedName(cr *apiv1alpha1.PerconaServerMySQLBackup) types.NamespacedName {
	return types.NamespacedName{Name: Name(cr), Namespace: cr.Namespace}
}

func JobName(cr *apiv1alpha1.PerconaServerMySQLBackup) string {
	return Name(cr)
}

func RestoreJobName(cluster *apiv1alpha1.PerconaServerMySQL, cr *apiv1alpha1.PerconaServerMySQLRestore) string {
	return RestoreName(cr)
}

func MatchLabels(cluster *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(map[string]string{apiv1alpha1.ComponentLabel: componentName}, cluster.Labels())
}

func Job(
	cluster *apiv1alpha1.PerconaServerMySQL,
	cr *apiv1alpha1.PerconaServerMySQLBackup,
	destination, initImage string,
	storage *apiv1alpha1.BackupStorageSpec,
) *batchv1.Job {
	var one int32 = 1
	t := true

	labels := util.SSMapMerge(storage.Labels, MatchLabels(cluster))

	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        JobName(cr),
			Namespace:   cluster.Namespace,
			Labels:      labels,
			Annotations: storage.Annotations,
		},
		Spec: batchv1.JobSpec{
			Parallelism: &one,
			Completions: &one,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:         corev1.RestartPolicyNever,
					ShareProcessNamespace: &t,
					SetHostnameAsFQDN:     &t,
					InitContainers: []corev1.Container{
						k8s.InitContainer(
							componentName,
							initImage,
							cluster.Spec.Backup.ImagePullPolicy,
							cluster.Spec.Backup.ContainerSecurityContext,
						),
					},
					Containers: []corev1.Container{
						xtrabackupContainer(cluster, cr.Name, destination, storage),
					},
					SecurityContext:   storage.PodSecurityContext,
					Affinity:          storage.Affinity,
					Tolerations:       storage.Tolerations,
					NodeSelector:      storage.NodeSelector,
					SchedulerName:     storage.SchedulerName,
					PriorityClassName: storage.PriorityClassName,
					RuntimeClassName:  storage.RuntimeClassName,
					DNSPolicy:         corev1.DNSClusterFirst,
					Volumes: []corev1.Volume{
						{
							Name: apiv1alpha1.BinVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: dataVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
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
		},
	}
}

func xtrabackupContainer(cluster *apiv1alpha1.PerconaServerMySQL, backupName, destination string, storage *apiv1alpha1.BackupStorageSpec) corev1.Container {
	spec := cluster.Spec.Backup

	verifyTLS := true
	if storage.VerifyTLS != nil {
		verifyTLS = *storage.VerifyTLS
	}

	return corev1.Container{
		Name:            componentName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Env: []corev1.EnvVar{
			{
				Name:  "BACKUP_NAME",
				Value: backupName,
			},
			{
				Name:  "BACKUP_DEST",
				Value: destination,
			},
			{
				Name:  "VERIFY_TLS",
				Value: strconv.FormatBool(verifyTLS),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      apiv1alpha1.BinVolumeName,
				MountPath: apiv1alpha1.BinVolumePath,
			},
			{
				Name:      dataVolumeName,
				MountPath: dataMountPath,
			},
			{
				Name:      tlsVolumeName,
				MountPath: tlsMountPath,
			},
		},
		Command:                  []string{"/opt/percona/run-backup.sh"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          storage.ContainerSecurityContext,
		Resources:                storage.Resources,
	}
}

func RestoreJob(
	cluster *apiv1alpha1.PerconaServerMySQL,
	backup *apiv1alpha1.PerconaServerMySQLBackup,
	restore *apiv1alpha1.PerconaServerMySQLRestore,
	storage *apiv1alpha1.BackupStorageSpec,
	initImage string,
	pvcName string,
) *batchv1.Job {
	one := int32(1)

	labels := util.SSMapMerge(storage.Labels, MatchLabels(cluster))

	var destination string
	switch storage.Type {
	case apiv1alpha1.BackupStorageAzure:
		destination = strings.TrimPrefix(backup.Status.Destination, storage.Azure.ContainerName+"/")
	case apiv1alpha1.BackupStorageS3:
		destination = strings.TrimPrefix(backup.Status.Destination, "s3://"+storage.S3.Bucket+"/")
	case apiv1alpha1.BackupStorageGCS:
		destination = strings.TrimPrefix(backup.Status.Destination, "gs://"+storage.GCS.Bucket+"/")
	}

	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        RestoreJobName(cluster, restore),
			Namespace:   cluster.Namespace,
			Labels:      labels,
			Annotations: storage.Annotations,
		},
		Spec: batchv1.JobSpec{
			Parallelism: &one,
			Completions: &one,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					InitContainers: []corev1.Container{
						{
							Name:            componentName + "-init",
							Image:           initImage,
							ImagePullPolicy: cluster.Spec.MySQL.ImagePullPolicy,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      apiv1alpha1.BinVolumeName,
									MountPath: apiv1alpha1.BinVolumePath,
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
							},
							Command:                  []string{"/ps-init-entrypoint.sh"},
							TerminationMessagePath:   "/dev/termination-log",
							TerminationMessagePolicy: corev1.TerminationMessageReadFile,
							SecurityContext:          cluster.Spec.MySQL.ContainerSecurityContext,
						},
					},
					Containers: []corev1.Container{
						restoreContainer(cluster, restore.Name, backup.Name, destination, storage),
					},
					Affinity:          storage.Affinity,
					Tolerations:       storage.Tolerations,
					NodeSelector:      storage.NodeSelector,
					SchedulerName:     storage.SchedulerName,
					PriorityClassName: storage.PriorityClassName,
					RuntimeClassName:  storage.RuntimeClassName,
					DNSPolicy:         corev1.DNSClusterFirst,
					Volumes: []corev1.Volume{
						{
							Name: apiv1alpha1.BinVolumeName,
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
		},
	}
}

func restoreContainer(cluster *apiv1alpha1.PerconaServerMySQL, restoreName, backupName, destination string, storage *apiv1alpha1.BackupStorageSpec) corev1.Container {
	spec := cluster.Spec.Backup

	verifyTLS := true
	if storage.VerifyTLS != nil {
		verifyTLS = *storage.VerifyTLS
	}

	return corev1.Container{
		Name:            componentName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Env: []corev1.EnvVar{
			{
				Name:  "RESTORE_NAME",
				Value: restoreName,
			},
			{
				Name:  "BACKUP_NAME",
				Value: backupName,
			},
			{
				Name:  "BACKUP_DEST",
				Value: destination,
			},
			{
				Name:  "VERIFY_TLS",
				Value: strconv.FormatBool(verifyTLS),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      apiv1alpha1.BinVolumeName,
				MountPath: apiv1alpha1.BinVolumePath,
			},
			{
				Name:      dataVolumeName,
				MountPath: dataMountPath,
			},
			{
				Name:      tlsVolumeName,
				MountPath: tlsMountPath,
			},
		},
		Command:                  []string{"/opt/percona/run-restore.sh"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          storage.ContainerSecurityContext,
		Resources:                storage.Resources,
	}
}

func PVC(cluster *apiv1alpha1.PerconaServerMySQL, cr *apiv1alpha1.PerconaServerMySQLBackup, storage *apiv1alpha1.BackupStorageSpec) *corev1.PersistentVolumeClaim {
	if len(storage.Volume.PersistentVolumeClaim.AccessModes) == 0 {
		storage.Volume.PersistentVolumeClaim.AccessModes = []corev1.PersistentVolumeAccessMode{
			corev1.ReadWriteOnce,
		}
	}

	return &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      JobName(cr),
			Namespace: cluster.Namespace,
		},
		Spec: *storage.Volume.PersistentVolumeClaim,
	}
}

func SetStoragePVC(job *batchv1.Job, pvc *corev1.PersistentVolumeClaim) error {
	spec := &job.Spec.Template.Spec

	vol := corev1.Volume{Name: componentName}
	vol.PersistentVolumeClaim = &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc.Name}

	spec.Volumes = append(spec.Volumes, vol)

	for i := range spec.Containers {
		container := &spec.Containers[i]
		if container.Name == componentName {
			container.VolumeMounts = append(
				container.VolumeMounts,
				corev1.VolumeMount{Name: backupVolumeName, MountPath: backupMountPath},
			)
			return nil
		}
	}

	return errors.Errorf("no container named %s in Job spec", componentName)
}

func SetStorageS3(job *batchv1.Job, s3 *apiv1alpha1.BackupStorageS3Spec) error {
	spec := &job.Spec.Template.Spec

	env := []corev1.EnvVar{
		{
			Name:  "STORAGE_TYPE",
			Value: string(apiv1alpha1.BackupStorageS3),
		},
		{
			Name: "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(s3.CredentialsSecret, "AWS_ACCESS_KEY_ID"),
			},
		},
		{
			Name: "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(s3.CredentialsSecret, "AWS_SECRET_ACCESS_KEY"),
			},
		},
		{
			Name:  "AWS_DEFAULT_REGION",
			Value: s3.Region,
		},
		{
			Name:  "AWS_ENDPOINT",
			Value: s3.EndpointURL,
		},
		{
			Name:  "S3_BUCKET",
			Value: s3.Bucket,
		},
		{
			Name:  "S3_STORAGE_CLASS",
			Value: s3.StorageClass,
		},
	}

	for i := range spec.Containers {
		container := &spec.Containers[i]

		if container.Name == componentName {
			container.Env = append(container.Env, env...)
			return nil
		}
	}

	return errors.Errorf("no container named %s in Job spec", componentName)
}

func SetStorageGCS(job *batchv1.Job, gcs *apiv1alpha1.BackupStorageGCSSpec) error {
	spec := &job.Spec.Template.Spec

	env := []corev1.EnvVar{
		{
			Name:  "STORAGE_TYPE",
			Value: string(apiv1alpha1.BackupStorageGCS),
		},
		{
			Name: "ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(gcs.CredentialsSecret, "ACCESS_KEY_ID"),
			},
		},
		{
			Name: "SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(gcs.CredentialsSecret, "SECRET_ACCESS_KEY"),
			},
		},
		{
			Name:  "GCS_ENDPOINT",
			Value: gcs.EndpointURL,
		},
		{
			Name:  "GCS_BUCKET",
			Value: gcs.Bucket,
		},
		{
			Name:  "GCS_STORAGE_CLASS",
			Value: gcs.StorageClass,
		},
	}

	for i := range spec.Containers {
		container := &spec.Containers[i]

		if container.Name == componentName {
			container.Env = append(container.Env, env...)
			return nil
		}
	}

	return errors.Errorf("no container named %s in Job spec", componentName)
}

func SetStorageAzure(job *batchv1.Job, azure *apiv1alpha1.BackupStorageAzureSpec) error {
	spec := &job.Spec.Template.Spec

	env := []corev1.EnvVar{
		{
			Name:  "STORAGE_TYPE",
			Value: string(apiv1alpha1.BackupStorageAzure),
		},
		{
			Name:  "AZURE_CONTAINER_NAME",
			Value: azure.ContainerName,
		},
		{
			Name: "AZURE_STORAGE_ACCOUNT",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(azure.CredentialsSecret, "AZURE_STORAGE_ACCOUNT_NAME"),
			},
		},
		{
			Name: "AZURE_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(azure.CredentialsSecret, "AZURE_STORAGE_ACCOUNT_KEY"),
			},
		},
		{
			Name:  "AZURE_ENDPOINT",
			Value: azure.EndpointURL,
		},
		{
			Name:  "AZURE_STORAGE_CLASS",
			Value: azure.StorageClass,
		},
	}

	for i := range spec.Containers {
		container := &spec.Containers[i]

		if container.Name == componentName {
			container.Env = append(container.Env, env...)
			return nil
		}
	}

	return errors.Errorf("no container named %s in Job spec", componentName)
}

func SetSourceNode(job *batchv1.Job, src string) error {
	spec := &job.Spec.Template.Spec

	for i := range spec.Containers {
		container := &spec.Containers[i]

		if container.Name == componentName {
			container.Env = append(container.Env, corev1.EnvVar{Name: "SRC_NODE", Value: src})
			return nil
		}
	}

	return errors.Errorf("no container named %s in Job spec", componentName)
}
