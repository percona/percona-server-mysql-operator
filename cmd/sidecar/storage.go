package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
	"github.com/pkg/errors"
)

type Storage interface {
	GetObject(ctx context.Context, objectName string) (io.ReadCloser, error)
	ListObjects(ctx context.Context, prefix string) ([]string, error)
}

func NewStorage(cfg *xtrabackup.BackupConfig) (Storage, error) {
	switch cfg.Type {
	case apiv1alpha1.BackupStorageAzure:
		a := cfg.Azure
		return NewAzure(a.StorageAccount, a.AccessKey, a.EndpointURL, a.ContainerName)
	case apiv1alpha1.BackupStorageS3:
		s3 := cfg.S3
		return NewS3(s3.EndpointURL, s3.AccessKey, s3.SecretKey, s3.Bucket, s3.Region, cfg.VerifyTLS)
	}
	return nil, errors.Errorf("storage type %s is not supported", cfg.Type)
}

// S3 is a type for working with S3 storages
type S3 struct {
	client     *minio.Client // minio client for work with storage
	bucketName string        // S3 bucket name where binlogs will be stored
}

// NewS3 return new Manager, useSSL using ssl for connection with storage
func NewS3(endpoint, accessKeyID, secretAccessKey, bucketName, region string, verifyTLS bool) (*S3, error) {
	useSSL := strings.Contains(endpoint, "https")
	endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	transport := http.DefaultTransport
	transport.(*http.Transport).TLSClientConfig = &tls.Config{
		InsecureSkipVerify: !verifyTLS,
	}
	minioClient, err := minio.New(strings.TrimRight(endpoint, "/"), &minio.Options{
		Creds:     credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure:    useSSL,
		Region:    region,
		Transport: transport,
	})
	if err != nil {
		return nil, errors.Wrap(err, "new minio client")
	}

	return &S3{
		client:     minioClient,
		bucketName: bucketName,
	}, nil
}

// GetObject return content by given object name
func (s *S3) GetObject(ctx context.Context, objectName string) (io.ReadCloser, error) {
	oldObj, err := s.client.GetObject(ctx, s.bucketName, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, errors.Wrapf(err, "get object %s", objectName)
	}

	return oldObj, nil
}

func (s *S3) ListObjects(ctx context.Context, prefix string) ([]string, error) {
	opts := minio.ListObjectsOptions{
		UseV1:  true,
		Prefix: prefix,
	}
	list := []string{}

	for object := range s.client.ListObjects(ctx, s.bucketName, opts) {
		if object.Err != nil {
			return nil, errors.Wrapf(object.Err, "list object %s", object.Key)
		}
		list = append(list, object.Key)
	}

	return list, nil
}

// Azure is a type for working with Azure Blob storages
type Azure struct {
	client    *azblob.Client // azure client for work with storage
	container string
}

func NewAzure(storageAccount, accessKey, endpoint, container string) (*Azure, error) {
	credential, err := azblob.NewSharedKeyCredential(storageAccount, accessKey)
	if err != nil {
		return nil, errors.Wrap(err, "new credentials")
	}
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.blob.core.windows.net/", storageAccount)
	}
	cli, err := azblob.NewClientWithSharedKeyCredential(endpoint, credential, nil)
	if err != nil {
		return nil, errors.Wrap(err, "new client")
	}

	return &Azure{
		client:    cli,
		container: container,
	}, nil
}

func (a *Azure) GetObject(ctx context.Context, name string) (io.ReadCloser, error) {
	resp, err := a.client.DownloadStream(ctx, a.container, name, &azblob.DownloadStreamOptions{})
	if err != nil {
		return nil, errors.Wrapf(err, "download stream: %s", name)
	}
	return resp.Body, nil
}

func (a *Azure) ListObjects(ctx context.Context, prefix string) ([]string, error) {
	pg := a.client.NewListBlobsFlatPager(a.container, &container.ListBlobsFlatOptions{
		Prefix: &prefix,
	})
	var blobs []string
	for pg.More() {
		resp, err := pg.NextPage(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "next page: %s", prefix)
		}
		if resp.Segment != nil {
			for _, item := range resp.Segment.BlobItems {
				if item != nil && item.Name != nil {
					blobs = append(blobs, *item.Name)
				}
			}
		}
	}
	return blobs, nil
}
