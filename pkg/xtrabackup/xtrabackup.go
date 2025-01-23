package xtrabackup

import (
	"fmt"
	"strconv"

	"github.com/pkg/errors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
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

func DeleteName(cr *apiv1alpha1.PerconaServerMySQLBackup) string {
	return componentShortName + "-delete-" + cr.Name
}

func JobNamespacedName(cr *apiv1alpha1.PerconaServerMySQLBackup) types.NamespacedName {
	return types.NamespacedName{Name: JobName(cr), Namespace: cr.Namespace}
}

func JobName(cr *apiv1alpha1.PerconaServerMySQLBackup) string {
	return trimJobName(Name(cr))
}

func RestoreJobName(cluster *apiv1alpha1.PerconaServerMySQL, cr *apiv1alpha1.PerconaServerMySQLRestore) string {
	return trimJobName(RestoreName(cr))
}

func DeleteJobName(cr *apiv1alpha1.PerconaServerMySQLBackup) string {
	return trimJobName(DeleteName(cr))
}

// trimJobName trims the provided string to ensure it stays within the 63-character limit.
// The job name will be included in the "batch.kubernetes.io/job-name" label in the ".spec.template" section of the job.
// Labels have a maximum length of 63 characters, so this function ensures the job name fits within that limit.
// Also it ensures that
func trimJobName(name string) string {
	trimLeft := func(name string) string {
		for i := 0; i < len(name); i++ {
			if (name[i] < 'a' || name[i] > 'z') && (name[i] < '0' || name[i] > '9') {
				continue
			}
			return name[i:]
		}
		return ""
	}

	trimRight := func(name string) string {
		for i := len(name) - 1; i >= 0; i-- {
			if (name[i] < 'a' || name[i] > 'z') && (name[i] < '0' || name[i] > '9') {
				continue
			}
			return name[:i+1]
		}
		return ""
	}

	name = trimLeft(name)
	name = trimRight(name)
	if len(name) > validation.DNS1035LabelMaxLength {
		name = name[:validation.DNS1035LabelMaxLength]
		name = trimRight(name)
	}

	return name
}

func MatchLabels(cluster *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(map[string]string{naming.LabelComponent: componentName}, cluster.Labels())
}

func Job(
	cluster *apiv1alpha1.PerconaServerMySQL,
	cr *apiv1alpha1.PerconaServerMySQLBackup,
	destination apiv1alpha1.BackupDestination, initImage string,
	storage *apiv1alpha1.BackupStorageSpec,
) *batchv1.Job {
	var one int32 = 1
	t := true

	labels := util.SSMapMerge(storage.Labels, MatchLabels(cluster))
	backoffLimit := int32(6)
	if cluster.Spec.Backup.BackoffLimit != nil {
		backoffLimit = *cluster.Spec.Backup.BackoffLimit
	}
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
			Parallelism:  &one,
			Completions:  &one,
			BackoffLimit: &backoffLimit,
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
							storage.ContainerSecurityContext,
							cluster.Spec.Backup.Resources,
							nil,
						),
					},
					Containers: []corev1.Container{
						xtrabackupContainer(cluster, cr.Name, destination, storage),
					},
					SecurityContext:           storage.PodSecurityContext,
					Affinity:                  storage.Affinity,
					TopologySpreadConstraints: storage.TopologySpreadConstraints,
					Tolerations:               storage.Tolerations,
					NodeSelector:              storage.NodeSelector,
					SchedulerName:             storage.SchedulerName,
					PriorityClassName:         storage.PriorityClassName,
					RuntimeClassName:          storage.RuntimeClassName,
					DNSPolicy:                 corev1.DNSClusterFirst,
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

func xtrabackupContainer(cluster *apiv1alpha1.PerconaServerMySQL, backupName string, destination apiv1alpha1.BackupDestination, storage *apiv1alpha1.BackupStorageSpec) corev1.Container {
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
				Value: destination.PathWithoutBucket(),
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

type XBCloudAction string

const (
	XBCloudActionPut    XBCloudAction = "put"
	XBCloudActionDelete XBCloudAction = "delete"
)

func XBCloudArgs(action XBCloudAction, conf *BackupConfig) []string {
	args := []string{string(action), "--parallel=10", "--curl-retriable-errors=7"}

	if !conf.VerifyTLS {
		args = append(args, "--insecure")
	}

	switch conf.Type {
	case apiv1alpha1.BackupStorageGCS:
		args = append(
			args,
			[]string{
				"--md5",
				"--storage=google",
				fmt.Sprintf("--google-bucket=%s", conf.GCS.Bucket),
				fmt.Sprintf("--google-access-key=%s", conf.GCS.AccessKey),
				fmt.Sprintf("--google-secret-key=%s", conf.GCS.SecretKey),
			}...,
		)
		if len(conf.GCS.EndpointURL) > 0 {
			args = append(args, fmt.Sprintf("--google-endpoint=%s", conf.GCS.EndpointURL))
		}
	case apiv1alpha1.BackupStorageS3:
		args = append(
			args,
			[]string{
				"--md5",
				"--storage=s3",
				fmt.Sprintf("--s3-bucket=%s", conf.S3.Bucket),
				fmt.Sprintf("--s3-region=%s", conf.S3.Region),
				fmt.Sprintf("--s3-access-key=%s", conf.S3.AccessKey),
				fmt.Sprintf("--s3-secret-key=%s", conf.S3.SecretKey),
			}...,
		)
		if len(conf.S3.EndpointURL) > 0 {
			args = append(args, fmt.Sprintf("--s3-endpoint=%s", conf.S3.EndpointURL))
		}
	case apiv1alpha1.BackupStorageAzure:
		args = append(
			args,
			[]string{
				"--storage=azure",
				fmt.Sprintf("--azure-storage-account=%s", conf.Azure.StorageAccount),
				fmt.Sprintf("--azure-container-name=%s", conf.Azure.ContainerName),
				fmt.Sprintf("--azure-access-key=%s", conf.Azure.AccessKey),
			}...,
		)
		if len(conf.Azure.EndpointURL) > 0 {
			args = append(args, fmt.Sprintf("--azure-endpoint=%s", conf.Azure.EndpointURL))
		}
	}

	args = append(args, conf.Destination)

	return args
}

func deleteContainer(image string, conf *BackupConfig, storage *apiv1alpha1.BackupStorageSpec) corev1.Container {
	return corev1.Container{
		Name:            componentName,
		Image:           image,
		ImagePullPolicy: corev1.PullNever,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      apiv1alpha1.BinVolumeName,
				MountPath: apiv1alpha1.BinVolumePath,
			},
		},
		Command:                  append([]string{"xbcloud"}, XBCloudArgs(XBCloudActionDelete, conf)...),
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          storage.ContainerSecurityContext,
		Resources:                storage.Resources,
	}
}

func RestoreJob(
	cluster *apiv1alpha1.PerconaServerMySQL,
	destination apiv1alpha1.BackupDestination,
	restore *apiv1alpha1.PerconaServerMySQLRestore,
	storage *apiv1alpha1.BackupStorageSpec,
	initImage string,
	pvcName string,
) *batchv1.Job {
	one := int32(1)

	labels := util.SSMapMerge(storage.Labels, MatchLabels(cluster))

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
						k8s.InitContainer(componentName, initImage,
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
							}),
					},
					Containers: []corev1.Container{
						restoreContainer(cluster, restore, destination, storage),
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
			BackoffLimit: func(i int32) *int32 { return &i }(4),
		},
	}
}

func GetDeleteJob(cr *apiv1alpha1.PerconaServerMySQLBackup, conf *BackupConfig) *batchv1.Job {
	var one int32 = 1
	t := true

	storage := cr.Status.Storage
	labels := util.SSMapMerge(storage.Labels, cr.Labels, map[string]string{naming.LabelComponent: componentName})

	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        DeleteJobName(cr),
			Namespace:   cr.Namespace,
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
					Containers: []corev1.Container{
						deleteContainer(cr.Status.Image, conf, storage),
					},
					SecurityContext:           storage.PodSecurityContext,
					Affinity:                  storage.Affinity,
					TopologySpreadConstraints: storage.TopologySpreadConstraints,
					Tolerations:               storage.Tolerations,
					NodeSelector:              storage.NodeSelector,
					SchedulerName:             storage.SchedulerName,
					PriorityClassName:         storage.PriorityClassName,
					RuntimeClassName:          storage.RuntimeClassName,
					DNSPolicy:                 corev1.DNSClusterFirst,
					Volumes: []corev1.Volume{
						{
							Name: apiv1alpha1.BinVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}
}

func restoreContainer(cluster *apiv1alpha1.PerconaServerMySQL, restore *apiv1alpha1.PerconaServerMySQLRestore, destination apiv1alpha1.BackupDestination, storage *apiv1alpha1.BackupStorageSpec) corev1.Container {
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
				Value: restore.Name,
			},
			{
				Name:  "BACKUP_DEST",
				Value: destination.PathWithoutBucket(),
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

	bucket, _ := s3.BucketAndPrefix()

	env := []corev1.EnvVar{
		{
			Name:  "STORAGE_TYPE",
			Value: string(apiv1alpha1.BackupStorageS3),
		},
		{
			Name: "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(s3.CredentialsSecret, secret.CredentialsAWSAccessKey),
			},
		},
		{
			Name: "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(s3.CredentialsSecret, secret.CredentialsAWSSecretKey),
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
			Value: bucket,
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

	bucket, _ := gcs.BucketAndPrefix()

	env := []corev1.EnvVar{
		{
			Name:  "STORAGE_TYPE",
			Value: string(apiv1alpha1.BackupStorageGCS),
		},
		{
			Name: "ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(gcs.CredentialsSecret, secret.CredentialsGCSAccessKey),
			},
		},
		{
			Name: "SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(gcs.CredentialsSecret, secret.CredentialsGCSSecretKey),
			},
		},
		{
			Name:  "GCS_ENDPOINT",
			Value: gcs.EndpointURL,
		},
		{
			Name:  "GCS_BUCKET",
			Value: bucket,
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

	container, _ := azure.ContainerAndPrefix()

	env := []corev1.EnvVar{
		{
			Name:  "STORAGE_TYPE",
			Value: string(apiv1alpha1.BackupStorageAzure),
		},
		{
			Name:  "AZURE_CONTAINER_NAME",
			Value: container,
		},
		{
			Name: "AZURE_STORAGE_ACCOUNT",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(azure.CredentialsSecret, secret.CredentialsAzureStorageAccount),
			},
		},
		{
			Name: "AZURE_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(azure.CredentialsSecret, secret.CredentialsAzureAccessKey),
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

type BackupConfig struct {
	Destination string                        `json:"destination"`
	Type        apiv1alpha1.BackupStorageType `json:"type"`
	VerifyTLS   bool                          `json:"verifyTLS,omitempty"`
	S3          struct {
		Bucket       string `json:"bucket"`
		Region       string `json:"region,omitempty"`
		EndpointURL  string `json:"endpointUrl,omitempty"`
		StorageClass string `json:"storageClass,omitempty"`
		AccessKey    string `json:"accessKey,omitempty"`
		SecretKey    string `json:"secretKey,omitempty"`
	} `json:"s3,omitempty"`
	GCS struct {
		Bucket       string `json:"bucket"`
		EndpointURL  string `json:"endpointUrl,omitempty"`
		StorageClass string `json:"storageClass,omitempty"`
		AccessKey    string `json:"accessKey,omitempty"`
		SecretKey    string `json:"secretKey,omitempty"`
	} `json:"gcs,omitempty"`
	Azure struct {
		ContainerName  string `json:"containerName"`
		EndpointURL    string `json:"endpointUrl,omitempty"`
		StorageClass   string `json:"storageClass,omitempty"`
		StorageAccount string `json:"storageAccount,omitempty"`
		AccessKey      string `json:"accessKey,omitempty"`
	} `json:"azure,omitempty"`
}
