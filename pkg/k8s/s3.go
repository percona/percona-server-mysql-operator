package k8s

import (
	"crypto/sha256"
	"fmt"
	"path"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func S3CertVolumes(selectors []apiv1.CABundleSecretSelector) []corev1.Volume {
	if len(selectors) == 0 {
		return nil
	}

	projections := make([]corev1.VolumeProjection, 0, len(selectors))
	for _, selector := range selectors {
		projections = append(projections, corev1.VolumeProjection{
			Secret: &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: selector.Name},
				Items: []corev1.KeyToPath{
					{
						Key:  selector.Key,
						Path: s3CertFileName(selector),
					},
				},
			},
		})
	}

	return []corev1.Volume{{
		Name: naming.S3CertsInputVolumeName,
		VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: projections,
		}},
	}}
}

func PrepareJobWithS3CA(job *batchv1.Job, cluster *apiv1.PerconaServerMySQL, storage *apiv1.BackupStorageS3Spec) {
	if storage == nil || storage.CABundle == nil || cluster.CompareVersion("1.3.0") < 0 {
		return
	}

	selector := *storage.CABundle
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, S3CertVolumes([]apiv1.CABundleSecretSelector{selector})...)
	container := &job.Spec.Template.Spec.Containers[0]
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
		Name:      naming.S3CertsInputVolumeName,
		MountPath: naming.S3CertsInputMountPath,
		ReadOnly:  true,
	})
	container.Env = append(container.Env, corev1.EnvVar{
		Name:  naming.EnvSSLCertFile,
		Value: S3CAPath(selector),
	})
}

func S3CAPath(selector apiv1.CABundleSecretSelector) string {
	return path.Join(naming.S3CertsInputMountPath, s3CertFileName(selector))
}

func s3CertFileName(selector apiv1.CABundleSecretSelector) string {
	sum := sha256.Sum256([]byte(selector.Name + "\x00" + selector.Key))
	return fmt.Sprintf("ca-%x.crt", sum)
}
