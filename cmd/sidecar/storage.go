package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

type storage interface {
	getObject(ctx context.Context, objectName string) (io.ReadCloser, error)
	listObjects(ctx context.Context, prefix string) ([]string, error)
}

// newStorage initializes a storage interface based on the provided backup configuration type.
func newStorage(cfg *xtrabackup.BackupConfig) (storage, error) {
	switch cfg.Type {
	case apiv1alpha1.BackupStorageAzure:
		a := cfg.Azure
		endpoint := a.EndpointURL
		if endpoint == "" {
			endpoint = fmt.Sprintf("https://%s.blob.core.windows.net/", a.StorageAccount)
		}
		return newAzure(a.StorageAccount, a.AccessKey, endpoint, a.ContainerName)
	case apiv1alpha1.BackupStorageGCS:
		gcs := cfg.GCS
		endpoint := gcs.EndpointURL
		if endpoint == "" {
			endpoint = "https://storage.googleapis.com"
		}
		return newS3(endpoint, gcs.AccessKey, gcs.SecretKey, gcs.Bucket, "", cfg.VerifyTLS)
	case apiv1alpha1.BackupStorageS3:
		s3 := cfg.S3
		endpoint := s3.EndpointURL
		if endpoint == "" {
			endpoint = "https://s3.amazonaws.com"
		}
		return newS3(endpoint, s3.AccessKey, s3.SecretKey, s3.Bucket, s3.Region, cfg.VerifyTLS)
	}
	return nil, errors.Errorf("storage type %s is not supported", cfg.Type)
}

// s3 is a type for working with s3 storages
type s3 struct {
	client     *minio.Client // minio client for work with storage
	bucketName string        // S3 bucket name where binlogs will be stored
}

// newS3 return new Manager, useSSL using ssl for connection with storage
func newS3(endpoint, accessKeyID, secretAccessKey, bucketName, region string, verifyTLS bool) (*s3, error) {
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

	return &s3{
		client:     minioClient,
		bucketName: bucketName,
	}, nil
}

// getObject return content by given object name
func (s *s3) getObject(ctx context.Context, objectName string) (io.ReadCloser, error) {
	oldObj, err := s.client.GetObject(ctx, s.bucketName, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, errors.Wrapf(err, "get object %s", objectName)
	}

	return oldObj, nil
}

// listObjects retrieves a list of object keys from the S3 bucket with the specified prefix.
func (s *s3) listObjects(ctx context.Context, prefix string) ([]string, error) {
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

// azure is a type for working with azure Blob storages
type azure struct {
	client    *azblob.Client // azure client for work with storage
	container string
}

// newAzure initializes an Azure blob storage client with the given credentials and endpoint.
func newAzure(storageAccount, accessKey, endpoint, container string) (*azure, error) {
	credential, err := azblob.NewSharedKeyCredential(storageAccount, accessKey)
	if err != nil {
		return nil, errors.Wrap(err, "new credentials")
	}
	cli, err := azblob.NewClientWithSharedKeyCredential(endpoint, credential, nil)
	if err != nil {
		return nil, errors.Wrap(err, "new client")
	}

	return &azure{
		client:    cli,
		container: container,
	}, nil
}

// getObject fetches an object from Azure blob storage based on its name.
func (a *azure) getObject(ctx context.Context, name string) (io.ReadCloser, error) {
	resp, err := a.client.DownloadStream(ctx, a.container, url.QueryEscape(name), &azblob.DownloadStreamOptions{})
	if err != nil {
		return nil, errors.Wrapf(err, "download stream: %s", name)
	}
	return resp.Body, nil
}

// listObjects retrieves a list of object names from Azure blob storage with the specified prefix.
func (a *azure) listObjects(ctx context.Context, prefix string) ([]string, error) {
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
