/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package psbackup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/pkg/errors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	k8sretry "k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql/topology"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

// PerconaServerMySQLBackupReconciler reconciles a PerconaServerMySQLBackup object
type PerconaServerMySQLBackupReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ServerVersion *platform.ServerVersion
}

//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlbackups;perconaservermysqlbackups/status;perconaservermysqlbackups/finalizers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete

// SetupWithManager sets up the controller with the Manager.
func (r *PerconaServerMySQLBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.PerconaServerMySQLBackup{}).
		Named("psbackup-controller").
		Complete(r)
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the PerconaServerMySQLBackup object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.9.2/pkg/reconcile
func (r *PerconaServerMySQLBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithName("PerconaServerMySQLBackup")

	rr := ctrl.Result{RequeueAfter: 5 * time.Second}

	cr := &apiv1alpha1.PerconaServerMySQLBackup{}
	if err := r.Client.Get(ctx, req.NamespacedName, cr); err != nil {
		if k8serrors.IsNotFound(err) {
			return rr, nil
		}

		return rr, errors.Wrapf(err, "get %v", req.NamespacedName.String())
	}

	status := cr.Status

	defer func() {
		if status.State == cr.Status.State && status.Destination == cr.Status.Destination {
			return
		}

		err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
			cr := &apiv1alpha1.PerconaServerMySQLBackup{}
			if err := r.Client.Get(ctx, req.NamespacedName, cr); err != nil {
				return errors.Wrapf(err, "get %v", req.NamespacedName.String())
			}

			cr.Status = status
			log.Info("Updating status", "state", cr.Status.State)
			if err := r.Client.Status().Update(ctx, cr); err != nil {
				return errors.Wrapf(err, "update %v", req.NamespacedName.String())
			}

			return nil
		})
		if err != nil {
			log.Error(err, "failed to update status")
		}
	}()

	r.checkFinalizers(ctx, cr)

	switch cr.Status.State {
	case apiv1alpha1.BackupFailed, apiv1alpha1.BackupSucceeded:
		return rr, nil
	}

	cluster := &apiv1alpha1.PerconaServerMySQL{}
	nn := types.NamespacedName{Name: cr.Spec.ClusterName, Namespace: cr.Namespace}
	if err := r.Client.Get(ctx, nn, cluster); err != nil {
		if k8serrors.IsNotFound(err) {
			status.State = apiv1alpha1.BackupError
			status.StateDesc = fmt.Sprintf("PerconaServerMySQL %s in namespace %s is not found", cr.Spec.ClusterName, cr.Namespace)
			return rr, nil
		}
		return rr, errors.Wrapf(err, "get %v", nn.String())
	}

	if err := cluster.CheckNSetDefaults(ctx, r.ServerVersion); err != nil {
		return rr, errors.Wrapf(err, "check and set defaults for %v", nn.String())
	}

	if cluster.Spec.Backup == nil || !cluster.Spec.Backup.Enabled {
		status.State = apiv1alpha1.BackupError
		status.StateDesc = "spec.backup stanza not found in PerconaServerMySQL CustomResource or backup is disabled"
		return rr, nil
	}

	storage, ok := cluster.Spec.Backup.Storages[cr.Spec.StorageName]
	if !ok {
		status.State = apiv1alpha1.BackupError
		status.StateDesc = fmt.Sprintf("%s not found in spec.backup.storages in PerconaServerMySQL CustomResource", cr.Spec.StorageName)
		return rr, nil
	}

	if cluster.Status.MySQL.State != apiv1alpha1.StateReady {
		log.Info("Cluster is not ready", "cluster", cr.Name)
		status.State = apiv1alpha1.BackupNew
		status.StateDesc = "cluster is not ready"
		return rr, nil
	}

	job := &batchv1.Job{}
	nn = xtrabackup.NamespacedName(cr)
	err := r.Client.Get(ctx, nn, job)
	if err != nil && !k8serrors.IsNotFound(err) {
		return rr, errors.Wrapf(err, "get job %v", nn.String())
	}

	if k8serrors.IsNotFound(err) {
		log.Info("Creating backup job", "jobName", nn.Name)

		initImage, err := k8s.InitImage(ctx, r.Client, cluster, cluster.Spec.Backup)
		if err != nil {
			return rr, errors.Wrap(err, "get operator image")
		}

		destination := getDestination(storage, cr.Spec.ClusterName, cr.CreationTimestamp.Format("2006-01-02-15:04:05"))
		job := xtrabackup.Job(cluster, cr, destination, initImage, storage)

		switch storage.Type {
		case apiv1alpha1.BackupStorageS3:
			if storage.S3 == nil {
				return rr, errors.New("s3 stanza is required in storage")
			}

			nn := types.NamespacedName{Name: storage.S3.CredentialsSecret, Namespace: cr.Namespace}
			exists, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{})
			if err != nil {
				return rr, errors.Wrapf(err, "check %s exists", nn)
			}

			if !exists {
				return rr, errors.Errorf("secret %s not found", nn)
			}

			if err := xtrabackup.SetStorageS3(job, storage.S3); err != nil {
				return rr, errors.Wrap(err, "set storage S3")
			}

			status.Destination = fmt.Sprintf("s3://%s/%s", storage.S3.Bucket, destination)
		case apiv1alpha1.BackupStorageGCS:
			if storage.GCS == nil {
				return rr, errors.New("gcs stanza is required in storage")
			}

			nn := types.NamespacedName{Name: storage.GCS.CredentialsSecret, Namespace: cr.Namespace}
			exists, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{})
			if err != nil {
				return rr, errors.Wrapf(err, "check %s exists", nn)
			}

			if !exists {
				return rr, errors.Errorf("secret %s not found", nn)
			}

			if err := xtrabackup.SetStorageGCS(job, storage.GCS); err != nil {
				return rr, errors.Wrap(err, "set storage GCS")
			}

			status.Destination = fmt.Sprintf("gs://%s/%s", storage.GCS.Bucket, destination)
		case apiv1alpha1.BackupStorageAzure:
			if storage.Azure == nil {
				return rr, errors.New("azure stanza is required in storage")
			}

			nn := types.NamespacedName{Name: storage.Azure.CredentialsSecret, Namespace: cr.Namespace}
			exists, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{})
			if err != nil {
				return rr, errors.Wrapf(err, "check %s exists", nn)
			}

			if !exists {
				return rr, errors.Errorf("secret %s not found", nn)
			}

			if err := xtrabackup.SetStorageAzure(job, storage.Azure); err != nil {
				return rr, errors.Wrap(err, "set storage Azure")
			}

			status.Destination = fmt.Sprintf("%s/%s", storage.Azure.ContainerName, destination)
		default:
			return rr, errors.Errorf("storage type %s is not supported", storage.Type)
		}

		status.Image = cluster.Spec.Backup.Image
		status.Storage = storage

		src, err := r.getBackupSource(ctx, cluster)
		if err != nil {
			return rr, errors.Wrap(err, "get backup source node")
		}

		if err := xtrabackup.SetSourceNode(job, src); err != nil {
			return rr, errors.Wrap(err, "set backup source node")
		}

		if err := controllerutil.SetControllerReference(cr, job, r.Scheme); err != nil {
			return rr, errors.Wrapf(err, "set controller reference to Job %s/%s", job.Namespace, job.Name)
		}

		if err := r.Client.Create(ctx, job); err != nil {
			return rr, errors.Wrapf(err, "create job %s/%s", job.Namespace, job.Name)
		}

		return rr, nil
	}

	switch status.State {
	case apiv1alpha1.BackupStarting:
		if job.Status.Active > 0 {
			status.State = apiv1alpha1.BackupRunning
		}
	case apiv1alpha1.BackupRunning:
		if job.Status.Active > 0 {
			return rr, nil
		}

		for _, cond := range job.Status.Conditions {
			if cond.Status != corev1.ConditionTrue {
				continue
			}

			switch cond.Type {
			case batchv1.JobFailed:
				status.State = apiv1alpha1.BackupFailed
			case batchv1.JobComplete:
				status.State = apiv1alpha1.BackupSucceeded
			}

			status.CompletedAt = job.Status.CompletionTime
		}
	case apiv1alpha1.BackupFailed, apiv1alpha1.BackupSucceeded:
		return rr, nil
	default:
		status.State = apiv1alpha1.BackupStarting
		status.StateDesc = ""
	}

	return rr, nil
}

func getDestination(storage *apiv1alpha1.BackupStorageSpec, clusterName, creationTimeStamp string) string {
	dest := fmt.Sprintf("%s-%s-full", clusterName, creationTimeStamp)

	switch storage.Type {
	case apiv1alpha1.BackupStorageS3:
		dest = path.Join(storage.S3.Bucket.Prefix(), storage.S3.Prefix, dest)
	case apiv1alpha1.BackupStorageGCS:
		dest = path.Join(storage.GCS.Bucket.Prefix(), storage.GCS.Prefix, dest)
	case apiv1alpha1.BackupStorageAzure:
		dest = path.Join(storage.Azure.ContainerName.Prefix(), storage.Azure.Prefix, dest)
	}

	return dest
}

func (r *PerconaServerMySQLBackupReconciler) getBackupSource(ctx context.Context, cluster *apiv1alpha1.PerconaServerMySQL) (string, error) {
	log := logf.FromContext(ctx).WithName("getBackupSource")

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cluster, apiv1alpha1.UserOperator)
	if err != nil {
		return "", errors.Wrap(err, "get operator password")
	}

	top, err := topology.Get(ctx, cluster, operatorPass)
	if err != nil {
		return "", errors.Wrap(err, "get topology")
	}

	var source string
	if len(top.Replicas) < 1 {
		source = top.Primary
		log.Info("no replicas found, using primary as the backup source", "primary", top.Primary)
	} else {
		source = top.Replicas[0]
	}

	return source, nil
}

func (r *PerconaServerMySQLBackupReconciler) checkFinalizers(ctx context.Context,
	cr *apiv1alpha1.PerconaServerMySQLBackup) {
	if cr.DeletionTimestamp == nil || cr.Status.State == apiv1alpha1.BackupStarting || cr.Status.State == apiv1alpha1.BackupRunning {
		return
	}
	log := logf.FromContext(ctx).WithName("checkFinalizers")

	defer func() {
		if err := r.Update(ctx, cr); err != nil {
			log.Error(err, "failed to update finalizers for backup", "backup", cr.Name)
		}
	}()

	if cr.Status.State == apiv1alpha1.BackupNew {
		cr.Finalizers = nil
		return
	}

	finalizers := sets.NewString()
	for _, finalizer := range cr.GetFinalizers() {
		var err error
		switch finalizer {
		case "delete-backup":
			var ok bool
			ok, err = r.deleteBackup(ctx, cr)
			if !ok {
				finalizers.Insert(finalizer)
			}
		default:
			finalizers.Insert(finalizer)
		}
		if err != nil {
			log.Error(err, "failed to run finalizer "+finalizer)
			finalizers.Insert(finalizer)
		}
	}
	cr.Finalizers = finalizers.List()
}

func (r *PerconaServerMySQLBackupReconciler) backupConfig(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQLBackup) (*xtrabackup.BackupConfig, error) {
	storage := cr.Status.Storage
	if storage == nil {
		return nil, errors.New("storage is not set")
	}
	verifyTLS := true
	if storage.VerifyTLS != nil {
		verifyTLS = *storage.VerifyTLS
	}
	destination := getDestination(storage, cr.Spec.ClusterName, cr.CreationTimestamp.Format("2006-01-02-15:04:05"))
	conf := &xtrabackup.BackupConfig{
		Destination: destination,
		VerifyTLS:   verifyTLS,
	}
	s := new(corev1.Secret)
	nn := types.NamespacedName{
		Namespace: cr.Namespace,
	}
	switch storage.Type {
	case apiv1alpha1.BackupStorageS3:
		s3 := storage.S3
		nn.Name = s3.CredentialsSecret
		if err := r.Get(ctx, nn, s); err != nil {
			return nil, errors.Wrapf(err, "get secret/%s", nn.Name)
		}
		accessKey, ok := s.Data[secret.CredentialsAWSAccessKey]
		if !ok {
			return nil, errors.Errorf("no credentials for Azure in secret %s", nn.Name)
		}
		secretKey, ok := s.Data[secret.CredentialsAWSSecretKey]
		if !ok {
			return nil, errors.Errorf("no credentials for Azure in secret %s", nn.Name)
		}
		conf.S3.Bucket = s3.Bucket.Bucket()
		conf.S3.Region = s3.Region
		conf.S3.EndpointURL = s3.EndpointURL
		conf.S3.StorageClass = s3.StorageClass
		conf.S3.AccessKey = string(accessKey)
		conf.S3.SecretKey = string(secretKey)
		conf.Type = apiv1alpha1.BackupStorageS3
	case apiv1alpha1.BackupStorageGCS:
		gcs := storage.GCS
		nn.Name = gcs.CredentialsSecret
		if err := r.Get(ctx, nn, s); err != nil {
			return nil, errors.Wrapf(err, "get secret/%s", nn.Name)
		}
		accessKey, ok := s.Data[secret.CredentialsGCSAccessKey]
		if !ok {
			return nil, errors.Errorf("no credentials for Azure in secret %s", nn.Name)
		}
		secretKey, ok := s.Data[secret.CredentialsGCSSecretKey]
		if !ok {
			return nil, errors.Errorf("no credentials for Azure in secret %s", nn.Name)
		}
		conf.GCS.Bucket = gcs.Bucket.Bucket()
		conf.GCS.EndpointURL = gcs.EndpointURL
		conf.GCS.StorageClass = gcs.StorageClass
		conf.GCS.AccessKey = string(accessKey)
		conf.GCS.SecretKey = string(secretKey)
		conf.Type = apiv1alpha1.BackupStorageGCS
	case apiv1alpha1.BackupStorageAzure:
		azure := storage.Azure
		nn.Name = azure.CredentialsSecret
		if err := r.Get(ctx, nn, s); err != nil {
			return nil, errors.Wrapf(err, "get secret/%s", nn.Name)
		}
		storageAccount, ok := s.Data[secret.CredentialsAzureStorageAccount]
		if !ok {
			return nil, errors.Errorf("no credentials for Azure in secret %s", nn.Name)
		}
		accessKey, ok := s.Data[secret.CredentialsAzureAccessKey]
		if !ok {
			return nil, errors.Errorf("no credentials for Azure in secret %s", nn.Name)
		}
		conf.Azure.ContainerName = azure.ContainerName.Bucket()
		conf.Azure.EndpointURL = azure.EndpointURL
		conf.Azure.StorageClass = azure.StorageClass
		conf.Azure.StorageAccount = string(storageAccount)
		conf.Azure.AccessKey = string(accessKey)
		conf.Type = apiv1alpha1.BackupStorageAzure
	default:
		return nil, errors.New("unknown backup storage type")
	}
	return conf, nil
}

func (r *PerconaServerMySQLBackupReconciler) deleteBackup(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQLBackup) (bool, error) {
	backupConf, err := r.backupConfig(ctx, cr)
	if err != nil {
		return false, errors.Wrap(err, "failed to create sidecar backup config")
	}

	cluster := new(apiv1alpha1.PerconaServerMySQL)
	nn := types.NamespacedName{Name: cr.Spec.ClusterName, Namespace: cr.Namespace}
	err = r.Client.Get(ctx, nn, cluster)
	if client.IgnoreNotFound(err) != nil {
		return false, errors.Wrapf(err, "get cluster %s", nn)
	}
	if k8serrors.IsNotFound(err) {
		job := &batchv1.Job{}
		nn = types.NamespacedName{Name: xtrabackup.DeleteJobName(cr), Namespace: cr.Namespace}
		err = r.Client.Get(ctx, nn, job)
		if client.IgnoreNotFound(err) != nil {
			return false, errors.Wrapf(err, "get job %s", nn)
		}
		if k8serrors.IsNotFound(err) {
			job = xtrabackup.GetDeleteJob(cr, backupConf)
			if err := controllerutil.SetControllerReference(cr, job, r.Scheme); err != nil {
				return false, errors.Wrapf(err, "set controller reference to Job %s/%s", job.Namespace, job.Name)
			}

			if err := r.Client.Create(ctx, job); err != nil {
				return false, errors.Wrapf(err, "create job %s/%s", job.Namespace, job.Name)
			}
			return false, nil
		}

		complete := false
		for _, cond := range job.Status.Conditions {
			if cond.Status != corev1.ConditionTrue {
				continue
			}

			switch cond.Type {
			case batchv1.JobFailed:
				return false, errors.New("job failed")
			case batchv1.JobComplete:
				complete = true
			}
		}
		return complete, nil
	}
	data, err := json.Marshal(backupConf)
	if err != nil {
		return false, errors.Wrap(err, "marshal sidecar backup config")
	}
	src, err := r.getBackupSource(ctx, cluster)
	if err != nil {
		return false, errors.Wrap(err, "get backup source node")
	}
	sidecarURL := url.URL{
		Host:   src + ":6033",
		Scheme: "http",
		Path:   "/backup/" + cr.Name,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, sidecarURL.String(), bytes.NewReader(data))
	if err != nil {
		return false, errors.Wrap(err, "create http request")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, errors.Wrap(err, "delete backup")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false, errors.Wrap(err, "read response body")
		}
		return false, errors.Errorf("delete backup failed: %s", string(body))
	}
	return true, nil
}
