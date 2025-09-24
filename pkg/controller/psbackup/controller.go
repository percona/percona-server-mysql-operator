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
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	k8sretry "k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

// PerconaServerMySQLBackupReconciler reconciles a PerconaServerMySQLBackup object
type PerconaServerMySQLBackupReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ServerVersion *platform.ServerVersion
	ClientCmd     clientcmd.Client

	NewSidecarClient xtrabackup.NewSidecarClientFunc
}

//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlbackups;perconaservermysqlbackups/status;perconaservermysqlbackups/finalizers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete

const controllerName = "psbackup-controller"

// SetupWithManager sets up the controller with the Manager.
func (r *PerconaServerMySQLBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.PerconaServerMySQLBackup{}).
		Named(controllerName).
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

	cr := &apiv1.PerconaServerMySQLBackup{}
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
		if status.State != cr.Status.State && status.StateDesc == cr.Status.StateDesc {
			status.StateDesc = ""
		}

		err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
			cr := &apiv1.PerconaServerMySQLBackup{}
			if err := r.Client.Get(ctx, req.NamespacedName, cr); err != nil {
				return errors.Wrapf(err, "get %v", req.NamespacedName.String())
			}

			cr.Status = status
			log.Info("Updating status", "state", cr.Status.State)
			return r.Client.Status().Update(ctx, cr)
		})
		if err != nil {
			log.Error(err, "Failed to update backup status")
		}
	}()

	r.checkFinalizers(ctx, cr)

	switch cr.Status.State {
	case apiv1.BackupFailed, apiv1.BackupSucceeded, apiv1.BackupError:
		return rr, nil
	}

	cluster := &apiv1.PerconaServerMySQL{}
	nn := types.NamespacedName{Name: cr.Spec.ClusterName, Namespace: cr.Namespace}
	if err := r.Client.Get(ctx, nn, cluster); err != nil {
		if k8serrors.IsNotFound(err) {
			status.State = apiv1.BackupError
			status.StateDesc = fmt.Sprintf("PerconaServerMySQL %s in namespace %s is not found", cr.Spec.ClusterName, cr.Namespace)
			return rr, nil
		}
		return rr, errors.Wrapf(err, "get %v", nn.String())
	}

	if err := cluster.CheckNSetDefaults(ctx, r.ServerVersion); err != nil {
		return rr, errors.Wrapf(err, "check and set defaults for %v", nn.String())
	}

	if cluster.Spec.Backup == nil || !cluster.Spec.Backup.Enabled {
		status.State = apiv1.BackupError
		status.StateDesc = "spec.backup not found in PerconaServerMySQL CustomResource or backups are disabled"
		return rr, nil
	}

	storage, ok := cluster.Spec.Backup.Storages[cr.Spec.StorageName]
	if !ok {
		status.State = apiv1.BackupError
		status.StateDesc = fmt.Sprintf("%s not found in spec.backup.storages in PerconaServerMySQL CustomResource", cr.Spec.StorageName)
		return rr, nil
	}

	backupSource, err := r.getBackupSource(ctx, cr, cluster)
	if err != nil {
		status.State = apiv1.BackupError
		status.StateDesc = fmt.Sprintf("failed to get the source host for backup: %v", err)
		return rr, nil
	}

	if cluster.Status.MySQL.State != apiv1.StateReady {
		log.Info("Cluster is not ready", "cluster", cr.Name)
		status.State = apiv1.BackupNew
		status.StateDesc = "cluster is not ready"
		return rr, nil
	}

	job := &batchv1.Job{}
	nn = xtrabackup.JobNamespacedName(cr)
	err = r.Get(ctx, nn, job)
	if err != nil && !k8serrors.IsNotFound(err) {
		return rr, errors.Wrapf(err, "get job %v", nn.String())
	}

	if k8serrors.IsNotFound(err) {
		log.Info("Preparing backup source", "source", backupSource)
		if err := r.prepareBackupSource(ctx, cr, cluster, backupSource); err != nil {
			return rr, errors.Wrap(err, "prepare backup source")
		}

		log.Info("Creating backup job", "jobName", nn.Name)
		if err := r.createBackupJob(ctx, cr, cluster, backupSource, storage, &status); err != nil {
			return rr, errors.Wrap(err, "failed to create backup job")
		}

		return rr, nil
	}

	for _, cond := range job.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}

		switch cond.Type {
		case batchv1.JobFailed:
			status.State = apiv1.BackupFailed
		case batchv1.JobComplete:
			status.State = apiv1.BackupSucceeded
		}

		status.CompletedAt = job.Status.CompletionTime
	}

	switch status.State {
	case apiv1.BackupStarting:
		if job.Status.Active == 0 {
			return rr, nil
		}

		running, err := r.isBackupJobRunning(ctx, job)
		if err != nil {
			return rr, errors.Wrap(err, "check if backup job is running")
		}

		if running {
			status.State = apiv1.BackupRunning
		}
	case apiv1.BackupRunning:
		if job.Status.Active > 0 {
			return rr, nil
		}
	case apiv1.BackupFailed, apiv1.BackupSucceeded:
		log.Info("Running post finish tasks")
		if err := r.runPostFinishTasks(ctx, cr, cluster); err != nil {
			return rr, errors.Wrap(err, "run post finish tasks")
		}
		return rr, nil
	default:
		status.State = apiv1.BackupStarting
		status.StateDesc = ""
	}

	return rr, nil
}

func (r *PerconaServerMySQLBackupReconciler) isBackupJobRunning(ctx context.Context, job *batchv1.Job) (bool, error) {
	if len(job.Spec.Template.Spec.Containers) == 0 {
		return false, nil
	}

	srcNode := ""
	destination := ""
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		switch env.Name {
		case "SRC_NODE":
			srcNode = env.Value
		case "BACKUP_DEST":
			destination = env.Value
		}
	}

	sc := r.NewSidecarClient(srcNode)
	cfg, err := sc.GetRunningBackupConfig(ctx)
	if err != nil {
		return false, errors.Wrap(err, "get running backup config")
	}

	if cfg == nil || cfg.Destination != destination {
		return false, nil
	}

	return true, nil
}

func (r *PerconaServerMySQLBackupReconciler) createBackupJob(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQLBackup,
	cluster *apiv1.PerconaServerMySQL,
	backupSource string,
	storage *apiv1.BackupStorageSpec,
	status *apiv1.PerconaServerMySQLBackupStatus,
) error {
	initImage, err := k8s.InitImage(ctx, r.Client, cluster, cluster.Spec.Backup)
	if err != nil {
		return errors.Wrap(err, "get operator image")
	}

	destination, err := xtrabackup.GetDestination(storage, cr.Spec.ClusterName, cr.CreationTimestamp.Format("2006-01-02-15:04:05"))
	if err != nil {
		return errors.Wrap(err, "get backup destination")
	}
	job, err := xtrabackup.Job(cluster, cr, destination, initImage, storage)
	if err != nil {
		return errors.Wrap(err, "create backup job")
	}

	switch storage.Type {
	case apiv1.BackupStorageS3:
		if storage.S3 == nil {
			return errors.New("s3 is required in storage")
		}

		nn := types.NamespacedName{Name: storage.S3.CredentialsSecret, Namespace: cr.Namespace}
		exists, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{})
		if err != nil {
			return errors.Wrapf(err, "check %s exists", nn)
		}

		if !exists {
			return errors.Errorf("secret %s not found", nn)
		}

		if err := xtrabackup.SetStorageS3(job, storage.S3); err != nil {
			return errors.Wrap(err, "set storage S3")
		}

		status.Destination = destination
	case apiv1.BackupStorageGCS:
		if storage.GCS == nil {
			return errors.New("gcs is required in storage")
		}

		nn := types.NamespacedName{Name: storage.GCS.CredentialsSecret, Namespace: cr.Namespace}
		exists, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{})
		if err != nil {
			return errors.Wrapf(err, "check %s exists", nn)
		}

		if !exists {
			return errors.Errorf("secret %s not found", nn)
		}

		if err := xtrabackup.SetStorageGCS(job, storage.GCS); err != nil {
			return errors.Wrap(err, "set storage GCS")
		}

		status.Destination = destination
	case apiv1.BackupStorageAzure:
		if storage.Azure == nil {
			return errors.New("azure is required in storage")
		}

		nn := types.NamespacedName{Name: storage.Azure.CredentialsSecret, Namespace: cr.Namespace}
		exists, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{})
		if err != nil {
			return errors.Wrapf(err, "check %s exists", nn)
		}

		if !exists {
			return errors.Errorf("secret %s not found", nn)
		}

		if err := xtrabackup.SetStorageAzure(job, storage.Azure); err != nil {
			return errors.Wrap(err, "set storage Azure")
		}

		status.Destination = destination
	default:
		return errors.Errorf("storage type %s is not supported", storage.Type)
	}

	status.Image = cluster.Spec.Backup.Image
	status.Storage = storage

	if err := xtrabackup.SetSourceNode(job, backupSource); err != nil {
		return errors.Wrap(err, "set backup source node")
	}

	status.BackupSource = backupSource

	if err := controllerutil.SetControllerReference(cr, job, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to Job %s/%s", job.Namespace, job.Name)
	}

	if err := r.Client.Create(ctx, job); err != nil {
		return errors.Wrapf(err, "create job %s/%s", job.Namespace, job.Name)
	}

	return nil
}

func (r *PerconaServerMySQLBackupReconciler) getBackupSource(ctx context.Context, cr *apiv1.PerconaServerMySQLBackup, cluster *apiv1.PerconaServerMySQL) (string, error) {
	log := logf.FromContext(ctx)

	var sourcePod string
	if cr.Spec.SourcePod != "" {
		sourcePod = cr.Spec.SourcePod
	} else if cluster.Spec.Backup.SourcePod != "" {
		sourcePod = cluster.Spec.Backup.SourcePod
	} else if cluster.Spec.MySQL.Size == 1 {
		sourcePod = mysql.PodName(cluster, 0)
	}

	if sourcePod != "" {
		pod := &corev1.Pod{}
		err := r.Get(ctx, types.NamespacedName{Name: sourcePod, Namespace: cr.Namespace}, pod)
		if err != nil {
			return sourcePod, errors.Wrap(err, "get pod")
		}
		return fmt.Sprintf("%s.%s.%s", sourcePod, mysql.ServiceName(cluster), cluster.Namespace), nil
	}

	if cluster.Spec.MySQL.ClusterType == apiv1.ClusterTypeAsync && !cluster.Spec.Orchestrator.Enabled {
		return "", errors.New("Orchestrator is disabled. Please specify the backup source explicitly using either spec.backup.sourcePod in the cluster CR or spec.sourcePod in the PerconaServerMySQLBackup resource.")
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cluster, apiv1.UserOperator)
	if err != nil {
		return "", errors.Wrap(err, "get operator password")
	}

	top, err := getDBTopology(ctx, r.Client, r.ClientCmd, cluster, operatorPass)
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

func (r *PerconaServerMySQLBackupReconciler) prepareBackupSource(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQLBackup,
	cluster *apiv1.PerconaServerMySQL,
	backupSource string,
) error {
	log := logf.FromContext(ctx)

	if cluster.Spec.MySQL.IsAsync() && cluster.Spec.Orchestrator.Enabled {
		orcPod, err := orchestrator.GetReadyPod(ctx, r.Client, cluster)
		if err != nil {
			return errors.Wrap(err, "get ready orchestrator pod")
		}

		owner := controllerName
		reason := fmt.Sprintf("ps-backup-%s", cr.Name)
		duration := 1200 // TODO: this should be configurable
		log.Info("Starting downtime for backup source", "source", backupSource, "owner", owner, "reason", reason, "durationSeconds", duration)

		err = orchestrator.BeginDowntime(
			ctx, r.ClientCmd, orcPod,
			backupSource, mysql.DefaultPort,
			owner, reason, duration)
		if err != nil {
			return errors.Wrapf(err, "begin downtime for %s", backupSource)
		}
	}

	return nil
}

func (r *PerconaServerMySQLBackupReconciler) runPostFinishTasks(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQLBackup,
	cluster *apiv1.PerconaServerMySQL,
) error {
	if !cluster.Spec.MySQL.IsAsync() || !cluster.Spec.Orchestrator.Enabled {
		return nil
	}

	log := logf.FromContext(ctx)

	pod, err := orchestrator.GetReadyPod(ctx, r.Client, cluster)
	if err != nil {
		return errors.Wrap(err, "get ready orchestrator pod")
	}

	log.Info("Ending downtime for backup source", "source", cr.Status.BackupSource)

	err = orchestrator.EndDowntime(ctx, r.ClientCmd, pod, cr.Status.BackupSource, mysql.DefaultPort)
	if err != nil {
		return errors.Wrapf(err, "end downtime for %s", cr.Status.BackupSource)
	}

	return nil
}

func (r *PerconaServerMySQLBackupReconciler) checkFinalizers(ctx context.Context, cr *apiv1.PerconaServerMySQLBackup) {
	if cr.DeletionTimestamp == nil || cr.Status.State == apiv1.BackupStarting || cr.Status.State == apiv1.BackupRunning {
		return
	}
	log := logf.FromContext(ctx).WithName("checkFinalizers")

	defer func() {
		if err := r.Update(ctx, cr); err != nil {
			log.Error(err, "failed to update finalizers for backup", "backup", cr.Name)
		}
	}()

	switch cr.Status.State {
	case apiv1.BackupError, apiv1.BackupNew:
		cr.Finalizers = nil
		return
	}

	finalizers := sets.NewString()
	for _, finalizer := range cr.GetFinalizers() {
		var err error
		switch finalizer {
		case naming.FinalizerDeleteBackup:
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

func (r *PerconaServerMySQLBackupReconciler) deleteBackup(ctx context.Context, cr *apiv1.PerconaServerMySQLBackup) (bool, error) {
	if cr.Status.State != apiv1.BackupSucceeded {
		return true, nil
	}

	log := logf.FromContext(ctx)
	log.Info("Deleting backup")

	backupConf, err := xtrabackup.GetBackupConfig(ctx, r.Client, cr)
	if err != nil {
		return false, errors.Wrap(err, "failed to create sidecar backup config")
	}

	cluster := new(apiv1.PerconaServerMySQL)
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
			job = xtrabackup.GetDeleteJob(cluster, cr, backupConf)
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

	pod, err := mysql.GetReadyPod(ctx, r.Client, cluster)
	if err != nil {
		return false, errors.Wrap(err, "get ready mysql pod")
	}
	src := mysql.PodFQDN(cluster, pod)
	sc := r.NewSidecarClient(src)

	if err := sc.DeleteBackup(ctx, cr.Name, *backupConf); err != nil {
		return false, errors.Wrap(err, "delete backup")
	}
	return true, nil
}

func getBackupSourcePod(ctx context.Context, cl client.Client, namespace, src string) (*corev1.Pod, error) {
	s := strings.Split(src, ".")
	if len(s) < 1 {
		return nil, errors.Errorf("unexpected backup source '%s'", src)
	}
	podName := s[0]

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
	}
	err := cl.Get(ctx, client.ObjectKeyFromObject(pod), pod)
	if err != nil {
		return pod, errors.Wrap(err, "get pod")
	}

	return pod, nil
}
