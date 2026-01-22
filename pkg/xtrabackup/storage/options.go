package storage

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
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

func GetOptionsFromBackupStatus(ctx context.Context, cl client.Client, cluster *apiv1.PerconaServerMySQL, storageName string, status apiv1.PerconaServerMySQLBackupStatus) (Options, error) {
	switch {
	case status.Storage.S3 != nil:
		return getS3Options(ctx, cl, cluster, storageName, status)
	case status.Storage.GCS != nil:
		return getGCSOptions(ctx, cl, cluster, storageName, status)
	case status.Storage.Azure != nil:
		return getAzureOptions(ctx, cl, cluster.Namespace, status)
	default:
		return nil, errors.Errorf("unknown storage type %s", status.Storage.Type)
	}
}

func getGCSOptions(ctx context.Context, cl client.Client, cluster *apiv1.PerconaServerMySQL, storageName string, backupStatus apiv1.PerconaServerMySQLBackupStatus) (Options, error) {
	s := new(corev1.Secret)
	err := cl.Get(ctx, types.NamespacedName{
		Name:      backupStatus.Storage.GCS.CredentialsSecret,
		Namespace: cluster.Namespace,
	}, s)
	if client.IgnoreNotFound(err) != nil {
		return nil, errors.Wrap(err, "failed to get secret")
	}
	accessKeyID := string(s.Data[secret.CredentialsGCSAccessKey])
	secretAccessKey := string(s.Data[secret.CredentialsGCSSecretKey])

	bucket, prefix := backupStatus.Storage.GCS.BucketAndPrefix()
	if bucket == "" {
		bucket, prefix = backupStatus.Destination.BucketAndPrefix()
	}

	if bucket == "" {
		return nil, errors.New("bucket name is not set")
	}

	verifyTLS := true
	if backupStatus.Storage.VerifyTLS != nil && !*backupStatus.Storage.VerifyTLS {
		verifyTLS = false
	}
	if cluster != nil && cluster.Spec.Backup != nil && len(cluster.Spec.Backup.Storages) > 0 {
		storage, ok := cluster.Spec.Backup.Storages[storageName]
		if ok && storage.VerifyTLS != nil {
			verifyTLS = *storage.VerifyTLS
		}
	}

	return &GCSOptions{
		Endpoint:        backupStatus.Storage.GCS.EndpointURL,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		BucketName:      bucket,
		Prefix:          prefix,
		VerifyTLS:       verifyTLS,
	}, nil
}

func getAzureOptions(ctx context.Context, cl client.Client, ns string, backupStatus apiv1.PerconaServerMySQLBackupStatus) (*AzureOptions, error) {
	s := new(corev1.Secret)
	err := cl.Get(ctx, types.NamespacedName{
		Name:      backupStatus.Storage.Azure.CredentialsSecret,
		Namespace: ns,
	}, s)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get secret")
	}
	accountName := string(s.Data[secret.CredentialsAzureStorageAccount])
	accountKey := string(s.Data[secret.CredentialsAzureAccessKey])

	container, prefix := backupStatus.Storage.Azure.ContainerAndPrefix()
	if container == "" {
		container, prefix = backupStatus.Destination.BucketAndPrefix()
	}

	if container == "" {
		return nil, errors.New("container name is not set")
	}

	return &AzureOptions{
		StorageAccount: accountName,
		AccessKey:      accountKey,
		Endpoint:       backupStatus.Storage.Azure.EndpointURL,
		Container:      container,
		Prefix:         prefix,
	}, nil
}

func getS3Options(ctx context.Context, cl client.Client, cluster *apiv1.PerconaServerMySQL, storageName string, backupStatus apiv1.PerconaServerMySQLBackupStatus) (*S3Options, error) {
	s := new(corev1.Secret)
	err := cl.Get(ctx, types.NamespacedName{
		Name:      backupStatus.Storage.S3.CredentialsSecret,
		Namespace: cluster.Namespace,
	}, s)
	if client.IgnoreNotFound(err) != nil {
		return nil, errors.Wrap(err, "failed to get secret")
	}
	accessKeyID := string(s.Data[secret.CredentialsAWSAccessKey])
	secretAccessKey := string(s.Data[secret.CredentialsAWSSecretKey])

	bucket, prefix := backupStatus.Storage.S3.BucketAndPrefix()
	if bucket == "" {
		bucket, prefix = backupStatus.Destination.BucketAndPrefix()
	}

	if bucket == "" {
		return nil, errors.New("bucket name is not set")
	}

	region := backupStatus.Storage.S3.Region
	if region == "" {
		region = "us-east-1"
	}

	verifyTLS := true
	if backupStatus.Storage.VerifyTLS != nil && !*backupStatus.Storage.VerifyTLS {
		verifyTLS = false
	}
	if cluster != nil && cluster.Spec.Backup != nil && len(cluster.Spec.Backup.Storages) > 0 {
		storage, ok := cluster.Spec.Backup.Storages[storageName]
		if ok && storage.VerifyTLS != nil {
			verifyTLS = *storage.VerifyTLS
		}
	}

	return &S3Options{
		Endpoint:        backupStatus.Storage.S3.EndpointURL,
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
