package storage

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

type Options interface {
	Type() apiv1.BackupStorageType
}

func GetOptionsFromBackupConfig(cfg *xtrabackup.BackupConfig) (Options, error) {
	switch cfg.Type {
	case apiv1.BackupStorageAzure:
		a := cfg.Azure
		return &AzureOptions{
			StorageAccount: a.StorageAccount,
			AccessKey:      a.AccessKey,
			Endpoint:       a.EndpointURL,
			Container:      a.ContainerName,
		}, nil
	case apiv1.BackupStorageGCS:
		g := cfg.GCS
		return &GCSOptions{
			Endpoint:        g.EndpointURL,
			AccessKeyID:     g.AccessKey,
			SecretAccessKey: g.SecretKey,
			BucketName:      g.Bucket,
			VerifyTLS:       cfg.VerifyTLS,
		}, nil
	case apiv1.BackupStorageS3:
		s3 := cfg.S3
		return &S3Options{
			Endpoint:        s3.EndpointURL,
			AccessKeyID:     s3.AccessKey,
			SecretAccessKey: s3.SecretKey,
			BucketName:      s3.Bucket,
			Region:          s3.Region,
			VerifyTLS:       cfg.VerifyTLS,
		}, nil
	}
	return nil, errors.Errorf("storage type %s is not supported", cfg.Type)
}

func GetOptionsFromBackup(ctx context.Context, cl client.Client, cluster *apiv1.PerconaServerMySQL, backup *apiv1.PerconaServerMySQLBackup) (Options, error) {
	switch {
	case backup.Status.Storage.S3 != nil:
		return getS3Options(ctx, cl, cluster, backup)
	case backup.Status.Storage.GCS != nil:
		return getGCSOptions(ctx, cl, cluster, backup)
	case backup.Status.Storage.Azure != nil:
		return getAzureOptions(ctx, cl, backup)
	default:
		return nil, errors.Errorf("unknown storage type %s", backup.Status.Storage.Type)
	}
}

func getGCSOptions(ctx context.Context, cl client.Client, cluster *apiv1.PerconaServerMySQL, backup *apiv1.PerconaServerMySQLBackup) (Options, error) {
	secret := new(corev1.Secret)
	err := cl.Get(ctx, types.NamespacedName{
		Name:      backup.Status.Storage.GCS.CredentialsSecret,
		Namespace: backup.Namespace,
	}, secret)
	if client.IgnoreNotFound(err) != nil {
		return nil, errors.Wrap(err, "failed to get secret")
	}
	accessKeyID := string(secret.Data["AWS_ACCESS_KEY_ID"])
	secretAccessKey := string(secret.Data["AWS_SECRET_ACCESS_KEY"])

	bucket, prefix := backup.Status.Storage.GCS.BucketAndPrefix()
	if bucket == "" {
		bucket, prefix = backup.Status.Destination.BucketAndPrefix()
	}

	if bucket == "" {
		return nil, errors.New("bucket name is not set")
	}

	verifyTLS := true
	if backup.Status.Storage.VerifyTLS != nil && !*backup.Status.Storage.VerifyTLS {
		verifyTLS = false
	}
	if cluster != nil && cluster.Spec.Backup != nil && len(cluster.Spec.Backup.Storages) > 0 {
		storage, ok := cluster.Spec.Backup.Storages[backup.Spec.StorageName]
		if ok && storage.VerifyTLS != nil {
			verifyTLS = *storage.VerifyTLS
		}
	}

	return &GCSOptions{
		Endpoint:        backup.Status.Storage.GCS.EndpointURL,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		BucketName:      bucket,
		Prefix:          prefix,
		VerifyTLS:       verifyTLS,
	}, nil
}

func getAzureOptions(ctx context.Context, cl client.Client, backup *apiv1.PerconaServerMySQLBackup) (*AzureOptions, error) {
	secret := new(corev1.Secret)
	err := cl.Get(ctx, types.NamespacedName{
		Name:      backup.Status.Storage.Azure.CredentialsSecret,
		Namespace: backup.Namespace,
	}, secret)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get secret")
	}
	accountName := string(secret.Data["AZURE_STORAGE_ACCOUNT_NAME"])
	accountKey := string(secret.Data["AZURE_STORAGE_ACCOUNT_KEY"])

	container, prefix := backup.Status.Storage.Azure.ContainerAndPrefix()
	if container == "" {
		container, prefix = backup.Status.Destination.BucketAndPrefix()
	}

	if container == "" {
		return nil, errors.New("container name is not set")
	}

	return &AzureOptions{
		StorageAccount: accountName,
		AccessKey:      accountKey,
		Endpoint:       backup.Status.Storage.Azure.EndpointURL,
		Container:      container,
		Prefix:         prefix,
	}, nil
}

func getS3Options(ctx context.Context, cl client.Client, cluster *apiv1.PerconaServerMySQL, backup *apiv1.PerconaServerMySQLBackup) (*S3Options, error) {
	secret := new(corev1.Secret)
	err := cl.Get(ctx, types.NamespacedName{
		Name:      backup.Status.Storage.S3.CredentialsSecret,
		Namespace: backup.Namespace,
	}, secret)
	if client.IgnoreNotFound(err) != nil {
		return nil, errors.Wrap(err, "failed to get secret")
	}
	accessKeyID := string(secret.Data["AWS_ACCESS_KEY_ID"])
	secretAccessKey := string(secret.Data["AWS_SECRET_ACCESS_KEY"])

	bucket, prefix := backup.Status.Storage.S3.BucketAndPrefix()
	if bucket == "" {
		bucket, prefix = backup.Status.Destination.BucketAndPrefix()
	}

	if bucket == "" {
		return nil, errors.New("bucket name is not set")
	}

	region := backup.Status.Storage.S3.Region
	if region == "" {
		region = "us-east-1"
	}

	verifyTLS := true
	if backup.Status.Storage.VerifyTLS != nil && !*backup.Status.Storage.VerifyTLS {
		verifyTLS = false
	}
	if cluster != nil && cluster.Spec.Backup != nil && len(cluster.Spec.Backup.Storages) > 0 {
		storage, ok := cluster.Spec.Backup.Storages[backup.Spec.StorageName]
		if ok && storage.VerifyTLS != nil {
			verifyTLS = *storage.VerifyTLS
		}
	}

	return &S3Options{
		Endpoint:        backup.Status.Storage.S3.EndpointURL,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		BucketName:      bucket,
		Prefix:          prefix,
		Region:          region,
		VerifyTLS:       verifyTLS,
	}, nil
}

var _ = Options(new(S3Options))

type S3Options struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	BucketName      string
	Prefix          string
	Region          string
	VerifyTLS       bool
}

func (o *S3Options) Type() apiv1.BackupStorageType {
	return apiv1.BackupStorageS3
}

var _ = Options(new(GCSOptions))

type GCSOptions struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	BucketName      string
	Prefix          string
	VerifyTLS       bool
}

func (o *GCSOptions) Type() apiv1.BackupStorageType {
	return apiv1.BackupStorageGCS
}

var _ = Options(new(AzureOptions))

type AzureOptions struct {
	StorageAccount string
	AccessKey      string
	Endpoint       string
	Container      string
	Prefix         string
}

func (o *AzureOptions) Type() apiv1.BackupStorageType {
	return apiv1.BackupStorageAzure
}
