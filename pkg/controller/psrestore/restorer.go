package psrestore

import (
	"context"
	"fmt"
	"slices"
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

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

type Restorer interface {
	Job(ctx context.Context) (*batchv1.Job, error)
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

	job, err := s.Job(ctx)
	if err != nil {
		return errors.Wrap(err, "get job")
	}
	if err := s.validateJob(ctx, job); err != nil {
		return errors.Wrap(err, "failed to validate job")
	}
	if err := s.validateStorage(ctx); err != nil {
		return errors.Wrap(err, "failed to validate storage")
	}

	return nil
}

func (s *s3) Job(ctx context.Context) (*batchv1.Job, error) {
	job, err := s.job(ctx)
	if err != nil {
		return nil, err
	}

	storage := s.bcp.Status.Storage
	if storage == nil || storage.S3 == nil {
		return nil, errors.New("s3 storage is not set")
	}
	if err := xtrabackup.SetStorageS3(job, storage.S3); err != nil {
		return nil, errors.Wrap(err, "set storage s3")
	}

	return job, nil
}

var _ Restorer = new(s3)

type gcs struct {
	*restorerOptions
}

func (g *gcs) Validate(ctx context.Context) error {
	job, err := g.Job(ctx)
	if err != nil {
		return errors.Wrap(err, "get job")
	}
	if err := g.validateJob(ctx, job); err != nil {
		return errors.Wrap(err, "failed to validate job")
	}
	if err := g.validateStorage(ctx); err != nil {
		return errors.Wrap(err, "failed to validate storage")
	}
	return nil
}

func (g *gcs) Job(ctx context.Context) (*batchv1.Job, error) {
	job, err := g.job(ctx)
	if err != nil {
		return nil, err
	}

	storage := g.bcp.Status.Storage
	if storage == nil || storage.GCS == nil {
		return nil, errors.New("gcs storage is not set")
	}
	if err := xtrabackup.SetStorageGCS(job, storage.GCS); err != nil {
		return nil, errors.Wrap(err, "set storage GCS")
	}

	return job, nil
}

var _ Restorer = new(gcs)

type azure struct {
	*restorerOptions
}

func (a *azure) Validate(ctx context.Context) error {
	job, err := a.Job(ctx)
	if err != nil {
		return errors.Wrap(err, "get job")
	}
	if err := a.validateJob(ctx, job); err != nil {
		return errors.Wrap(err, "failed to validate job")
	}
	if err := a.validateStorage(ctx); err != nil {
		return errors.Wrap(err, "failed to validate storage")
	}
	return nil
}

func (a *azure) Job(ctx context.Context) (*batchv1.Job, error) {
	job, err := a.job(ctx)
	if err != nil {
		return nil, err
	}

	storage := a.bcp.Status.Storage
	if storage == nil || storage.Azure == nil {
		return nil, errors.New("azure storage is not set")
	}
	if err := xtrabackup.SetStorageAzure(job, storage.Azure); err != nil {
		return nil, errors.Wrap(err, "set storage Azure")
	}

	return job, nil
}

var _ Restorer = new(azure)

type restorerOptions struct {
	cluster          *apiv1.PerconaServerMySQL
	bcp              *apiv1.PerconaServerMySQLBackup
	cr               *apiv1.PerconaServerMySQLRestore
	scheme           *runtime.Scheme
	k8sClient        client.Client
	newStorageClient storage.NewClientFunc
	initImage        string
}

func (r *PerconaServerMySQLRestoreReconciler) getRestorer(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQLRestore,
	cluster *apiv1.PerconaServerMySQL,
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
	case apiv1.BackupStorageGCS:
		sr := gcs{&s}
		return &sr, nil
	case apiv1.BackupStorageS3:
		sr := s3{&s}
		return &sr, nil
	case apiv1.BackupStorageAzure:
		sr := azure{&s}
		return &sr, nil
	}
	return nil, errors.Errorf("unknown backup storage type")
}

func (s *restorerOptions) job(ctx context.Context) (*batchv1.Job, error) {
	pvcName := fmt.Sprintf("%s-%s-mysql-0", mysql.DataVolumeName, s.cluster.Name)
	bcpStorage := s.bcp.Status.Storage

	destination := xtrabackup.DestinationInfo{
		Base: s.bcp.Status.Destination.PathWithoutBucket(),
	}
	if s.bcp.Status.Type == apiv1.BackupTypeIncremental {
		var err error
		destination, err = s.resolveIncrementalChain(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "resolve incremental chain")
		}
	}

	job := xtrabackup.RestoreJob(s.cluster, destination, s.cr, bcpStorage, s.initImage, pvcName)
	if err := controllerutil.SetControllerReference(s.cr, job, s.scheme); err != nil {
		return nil, errors.Wrapf(err, "set controller reference to Job %s/%s", job.Namespace, job.Name)
	}
	return job, nil
}

// resolveIncrementalChain discovers all incremental backups in the chain up to (and including)
// the target backup's destination, and returns the base + ordered incremental paths.
func (s *restorerOptions) resolveIncrementalChain(ctx context.Context) (xtrabackup.DestinationInfo, error) {
	dest := s.bcp.Status.Destination
	baseDest := dest.IncrementalBaseDestination()

	incrementalsDirFull := dest.IncrementalsDir()
	if incrementalsDirFull == "" {
		return xtrabackup.DestinationInfo{}, errors.New("could not parse incremental destination path")
	}

	storageOpts, err := storage.GetOptionsFromBackupStatus(ctx, s.k8sClient, s.cluster, s.bcp.Spec.StorageName, s.bcp.Status)
	if err != nil {
		return xtrabackup.DestinationInfo{}, errors.Wrap(err, "get storage options")
	}

	storageClient, err := s.newStorageClient(ctx, storageOpts)
	if err != nil {
		return xtrabackup.DestinationInfo{}, errors.Wrap(err, "create storage client")
	}

	// The storage client was initialized with a prefix derived from the incremental
	// destination (e.g. "pfx/base-backup.incr/"). Reset it to the base backup's
	// prefix so that listing paths align with the backup name directly.
	_, basePrefix := baseDest.BucketAndPrefix()
	storageClient.SetPrefix(basePrefix)

	incrListPrefix := baseDest.BackupName() + ".incr/"
	objects, err := storageClient.ListObjects(ctx, incrListPrefix)
	if err != nil {
		return xtrabackup.DestinationInfo{}, errors.Wrap(err, "list incremental backups")
	}

	var allIncrementals []string
	for _, obj := range objects {
		rel := strings.TrimPrefix(obj, incrListPrefix)
		ts, _, _ := strings.Cut(rel, "/")
		if ts != "" && strings.HasSuffix(ts, "-incr") {
			allIncrementals = append(allIncrementals, ts)
		}
	}
	slices.Sort(allIncrementals)
	allIncrementals = slices.Compact(allIncrementals)

	var incrementalDests []string
	// Timestamps are in RFC3339-like format, so lexicographic order == chronological order.
	for _, ts := range allIncrementals {
		if ts > dest.BackupName() {
			break
		}
		incrDest := apiv1.BackupDestination(incrementalsDirFull + ts)
		incrementalDests = append(incrementalDests, incrDest.PathWithoutBucket())
	}

	if len(incrementalDests) == 0 {
		return xtrabackup.DestinationInfo{}, errors.New("no incremental backups found in chain")
	}

	return xtrabackup.DestinationInfo{
		Base:         baseDest.PathWithoutBucket(),
		Incrementals: incrementalDests,
	}, nil
}

func (opts *restorerOptions) validateStorage(ctx context.Context) error {
	storageOpts, err := storage.GetOptionsFromBackupStatus(ctx, opts.k8sClient, opts.cluster, opts.bcp.Spec.StorageName, opts.bcp.Status)
	if err != nil {
		return errors.Wrap(err, "failed to get storage options")
	}

	storageClient, err := opts.newStorageClient(ctx, storageOpts)
	if err != nil {
		return errors.Wrap(err, "failed to create s3 client")
	}

	if opts.bcp.Status.Type == apiv1.BackupTypeIncremental {
		_, prefix := opts.bcp.Status.Destination.BucketAndPrefix()
		storageClient.SetPrefix(prefix)
	}

	backupName := opts.bcp.Status.Destination.BackupName() + "/"
	objs, err := storageClient.ListObjects(ctx, backupName)
	if err != nil {
		return errors.Wrap(err, "failed to list objects")
	}
	if len(objs) == 0 {
		return errors.New("backup not found in storage")
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

func getBackup(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQLRestore, cluster *apiv1.PerconaServerMySQL) (*apiv1.PerconaServerMySQLBackup, error) {
	if cr.Spec.BackupSource != nil {
		status := cr.Spec.BackupSource.DeepCopy()
		status.State = apiv1.BackupSucceeded
		status.CompletedAt = nil
		status.Type = apiv1.BackupTypeFull
		if status.Destination.IsIncremental() {
			status.Type = apiv1.BackupTypeIncremental
		}

		return &apiv1.PerconaServerMySQLBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cr.Name,
				Namespace: cr.Namespace,
			},
			Spec: apiv1.PerconaServerMySQLBackupSpec{
				ClusterName: cr.Spec.ClusterName,
				Type:        status.Type,
			},
			Status: *status,
		}, nil
	}
	if cr.Spec.BackupName == "" {
		return nil, errors.New("backupName and backupSource are empty")
	}

	backup := &apiv1.PerconaServerMySQLBackup{}
	nn := types.NamespacedName{Name: cr.Spec.BackupName, Namespace: cr.Namespace}
	if err := cl.Get(ctx, nn, backup); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, errors.Errorf("PerconaServerMySQLBackup %s in namespace %s is not found", nn.Name, nn.Namespace)
		}
		return nil, errors.Wrapf(err, "get backup %s", nn)
	}
	storage, ok := cluster.Spec.Backup.Storages[backup.Spec.StorageName]
	if ok {
		backup.Status.Storage = storage
	}
	return backup, nil
}
