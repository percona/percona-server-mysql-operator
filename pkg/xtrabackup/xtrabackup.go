package xtrabackup

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
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

func Name(cluster *apiv1alpha1.PerconaServerMySQL) string {
	return cluster.Name + "-" + componentShortName
}

func NamespacedName(cluster *apiv1alpha1.PerconaServerMySQL) types.NamespacedName {
	return types.NamespacedName{Name: Name(cluster), Namespace: cluster.Namespace}
}

func JobName(cluster *apiv1alpha1.PerconaServerMySQL, cr *apiv1alpha1.PerconaServerMySQLBackup) string {
	return Name(cluster) + "-" + cr.Name
}

func MatchLabels(cluster *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(map[string]string{apiv1alpha1.ComponentLabel: componentName}, cluster.Labels())
}

func Job(cluster *apiv1alpha1.PerconaServerMySQL, cr *apiv1alpha1.PerconaServerMySQLBackup, initImage string) *batchv1.Job {
	var one int32 = 1
	t := true

	labels := MatchLabels(cluster)

	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      JobName(cluster, cr),
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			Parallelism: &one,
			Completions: &one,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:         corev1.RestartPolicyOnFailure,
					ShareProcessNamespace: &t,
					SetHostnameAsFQDN:     &t,
					InitContainers: []corev1.Container{
						{
							Name:            componentName + "-init",
							Image:           initImage,
							ImagePullPolicy: cluster.Spec.MySQL.ImagePullPolicy,
							VolumeMounts: []corev1.VolumeMount{
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
						mysqldContainer(cluster, cr),
						xtrabackupContainer(cluster),
					},
					DNSPolicy: corev1.DNSClusterFirst,
					Volumes: []corev1.Volume{
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

func mysqldContainer(cluster *apiv1alpha1.PerconaServerMySQL, cr *apiv1alpha1.PerconaServerMySQLBackup) corev1.Container {
	spec := cluster.MySQLSpec()

	return corev1.Container{
		Name:            "mysql",
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Resources:       spec.Resources,
		Env: []corev1.EnvVar{
			{
				Name:  "SERVICE_NAME_UNREADY",
				Value: mysql.UnreadyServiceName(cluster),
			},
			{
				Name:  "SERVER_ID",
				Value: cr.Hash(),
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Name:          "mysql",
				ContainerPort: mysql.DefaultPort,
			},
		},
		VolumeMounts: []corev1.VolumeMount{
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
		Command:                  []string{"/var/lib/mysql/ps-entrypoint.sh"},
		Args:                     []string{"mysqld"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
		StartupProbe:             k8s.ExecProbe(spec.StartupProbe, []string{"/var/lib/mysql/bootstrap"}),
		LivenessProbe:            k8s.ExecProbe(spec.LivenessProbe, []string{"/var/lib/mysql/healthcheck", "liveness"}),
	}
}

func xtrabackupContainer(cluster *apiv1alpha1.PerconaServerMySQL) corev1.Container {
	spec := cluster.Spec.Backup

	return corev1.Container{
		Name:            componentName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Env:             []corev1.EnvVar{},
		Ports: []corev1.ContainerPort{
			{
				Name:          "mysql",
				ContainerPort: mysql.DefaultPort,
			},
		},
		VolumeMounts: []corev1.VolumeMount{
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
		Command:                  []string{"/var/lib/mysql/run-backup.sh"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
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
			Name:      JobName(cluster, cr),
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
