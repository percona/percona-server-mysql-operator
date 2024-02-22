package psrestore

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

type Restorer interface {
	Job() (*batchv1.Job, error)
	Validate(ctx context.Context) error
}

type s3 struct {
	*restorerOptions
}

func (s *s3) Validate(ctx context.Context) error {
	if storage := s.bcp.Status.Storage; storage != nil && storage.S3 != nil && storage.S3.CredentialsSecret == "" {
		// Skip validation if the credentials secret isn't set.
		// This allows authentication via IAM roles.
		// More info: https://github.com/percona/k8spxc-docs/blob/87f98e6ddae8114474836c0610155d05d3531e03/docs/backups-storage.md?plain=1#L116-L126
		return nil
	}

	return s.restorerOptions.Validate(ctx)
}

var _ Restorer = new(s3)

type gcs struct {
	*restorerOptions
}

var _ Restorer = new(gcs)

type azure struct {
	*restorerOptions
}

var _ Restorer = new(azure)

type restorerOptions struct {
	cluster          *apiv1alpha1.PerconaServerMySQL
	bcp              *apiv1alpha1.PerconaServerMySQLBackup
	cr               *apiv1alpha1.PerconaServerMySQLRestore
	scheme           *runtime.Scheme
	k8sClient        client.Client
	newStorageClient storage.NewClientFunc
	initImage        string
}

func (r *PerconaServerMySQLRestoreReconciler) getRestorer(
	ctx context.Context,
	cr *apiv1alpha1.PerconaServerMySQLRestore,
	cluster *apiv1alpha1.PerconaServerMySQL,
) (Restorer, error) {
	initImage, err := k8s.InitImage(ctx, r.Client, cluster, cluster.Spec.Backup)
	if err != nil {
		return nil, errors.Wrap(err, "get operator image")
	}
	bcp, err := getBackup(ctx, r.Client, cr, cluster)
	if err != nil {
		return nil, errors.Wrap(err, "get backup")
	}
	s := restorerOptions{
		cr:               cr,
		bcp:              bcp,
		cluster:          cluster,
		scheme:           r.Scheme,
		initImage:        initImage,
		k8sClient:        r.Client,
		newStorageClient: r.NewStorageClient,
	}
	switch bcp.Status.Storage.Type {
	case apiv1alpha1.BackupStorageGCS:
		sr := gcs{&s}
		return &sr, nil
	case apiv1alpha1.BackupStorageS3:
		sr := s3{&s}
		return &sr, nil
	case apiv1alpha1.BackupStorageAzure:
		sr := azure{&s}
		return &sr, nil
	}
	return nil, errors.Errorf("unknown backup storage type")
}

func (s *restorerOptions) Job() (*batchv1.Job, error) {
	pvcName := fmt.Sprintf("%s-%s-mysql-0", mysql.DataVolumeName, s.cluster.Name)
	storage := s.bcp.Status.Storage
	job := xtrabackup.RestoreJob(s.cluster, s.bcp.Status.Destination, s.cr, storage, s.initImage, pvcName)
	switch storage.Type {
	case apiv1alpha1.BackupStorageS3:
		if storage.S3 == nil {
			return nil, errors.New("s3 storage is not set")
		}
		if err := xtrabackup.SetStorageS3(job, storage.S3); err != nil {
			return nil, errors.Wrap(err, "set storage s3")
		}
	case apiv1alpha1.BackupStorageGCS:
		if storage.GCS == nil {
			return nil, errors.New("gcs storage is not set")
		}
		if err := xtrabackup.SetStorageGCS(job, storage.GCS); err != nil {
			return nil, errors.Wrap(err, "set storage GCS")
		}
	case apiv1alpha1.BackupStorageAzure:
		if storage.Azure == nil {
			return nil, errors.New("azure storage is not set")
		}
		if err := xtrabackup.SetStorageAzure(job, storage.Azure); err != nil {
			return nil, errors.Wrap(err, "set storage Azure")
		}
	default:
		return nil, errors.Errorf("storage type %s is not supported", storage.Type)
	}

	if err := controllerutil.SetControllerReference(s.cr, job, s.scheme); err != nil {
		return nil, errors.Wrapf(err, "set controller reference to Job %s/%s", job.Namespace, job.Name)
	}
	return job, nil
}

func (opts *restorerOptions) Validate(ctx context.Context) error {
	if opts.bcp.Status.Storage == nil {
		return errors.New("backup storage is not set")
	}
	job, err := opts.Job()
	if err != nil {
		return errors.Wrap(err, "get job")
	}
	if err := opts.validateJob(ctx, job); err != nil {
		return errors.Wrap(err, "failed to validate job")
	}

	storageOpts, err := storage.GetOptionsFromBackup(ctx, opts.k8sClient, opts.cluster, opts.bcp)
	if err != nil {
		return errors.Wrap(err, "failed to get storage options")
	}

	storageClient, err := opts.newStorageClient(ctx, storageOpts)
	if err != nil {
		return errors.Wrap(err, "failed to create s3 client")
	}
	backupName := opts.bcp.Status.Destination.BackupName() + "/"
	objs, err := storageClient.ListObjects(ctx, backupName)
	if err != nil {
		return errors.Wrap(err, "failed to list objects")
	}
	if len(objs) == 0 {
		return errors.New("backup not found")
	}

	return nil
}

func (opts *restorerOptions) validateJob(ctx context.Context, job *batchv1.Job) error {
	cl := opts.k8sClient

	secrets := []string{}
	for _, container := range job.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name != "" {
				secrets = append(secrets, env.ValueFrom.SecretKeyRef.Name)
			}
		}
	}

	notExistingSecrets := make(map[string]struct{})
	for _, secret := range secrets {
		err := cl.Get(ctx, types.NamespacedName{
			Name:      secret,
			Namespace: job.Namespace,
		}, new(corev1.Secret))
		if err != nil {
			if k8serrors.IsNotFound(err) {
				notExistingSecrets[secret] = struct{}{}
				continue
			}
			return err
		}
	}
	if len(notExistingSecrets) > 0 {
		secrets := make([]string, 0, len(notExistingSecrets))
		for k := range notExistingSecrets {
			secrets = append(secrets, k)
		}
		sort.StringSlice(secrets).Sort()
		return errors.Errorf("secrets %s not found", strings.Join(secrets, ", "))
	}

	return nil
}

func getBackup(ctx context.Context, cl client.Client, cr *apiv1alpha1.PerconaServerMySQLRestore, cluster *apiv1alpha1.PerconaServerMySQL) (*apiv1alpha1.PerconaServerMySQLBackup, error) {
	if cr.Spec.BackupSource != nil {
		status := cr.Spec.BackupSource.DeepCopy()
		status.State = apiv1alpha1.BackupSucceeded
		status.CompletedAt = nil
		return &apiv1alpha1.PerconaServerMySQLBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cr.Name,
				Namespace: cr.Namespace,
			},
			Spec: apiv1alpha1.PerconaServerMySQLBackupSpec{
				ClusterName: cr.Spec.ClusterName,
			},
			Status: *status,
		}, nil
	}
	if cr.Spec.BackupName == "" {
		return nil, errors.New("backupName and backupSource are empty")
	}

	backup := &apiv1alpha1.PerconaServerMySQLBackup{}
	nn := types.NamespacedName{Name: cr.Spec.BackupName, Namespace: cr.Namespace}
	if err := cl.Get(ctx, nn, backup); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, errors.Errorf("PerconaServerMySQLBackup %s in namespace %s is not found", nn.Name, nn.Namespace)
		}
		return nil, errors.Wrapf(err, "get backup %s", nn)
	}
	storageName := backup.Spec.StorageName
	storage, ok := cluster.Spec.Backup.Storages[storageName]
	if !ok {
		return nil, errors.Errorf("%s not found in spec.backup.storages in PerconaServerMySQL CustomResource", storageName)
	}
	backup.Status.Storage = storage
	return backup, nil
}
