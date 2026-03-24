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

package psrestore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sretry "k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/haproxy"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/pitr"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/router"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
)

// PerconaServerMySQLRestoreReconciler reconciles a PerconaServerMySQLRestore object
type PerconaServerMySQLRestoreReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	ServerVersion    *platform.ServerVersion
	ClientCmd        clientcmd.Client
	NewStorageClient storage.NewClientFunc

	sm sync.Map
}

var ErrWaitingTermination error = errors.New("waiting for MySQL pods to terminate")

//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlrestores;perconaservermysqlrestores/status;perconaservermysqlrestores/finalizers,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the PerconaServerMySQLRestore object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.9.2/pkg/reconcile
func (r *PerconaServerMySQLRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithName("PerconaServerMySQLRestore").WithValues("name", req.Name, "namespace", req.Namespace)

	cr := &apiv1.PerconaServerMySQLRestore{}
	err := r.Get(ctx, req.NamespacedName, cr)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "get CR %s", req.NamespacedName)
	}

	status := cr.Status
	if status.State == apiv1.RestoreError {
		status.State = apiv1.RestoreNew
		status.StateDesc = ""
	}

	defer func() {
		if status.State == cr.Status.State && status.StateDesc == cr.Status.StateDesc {
			return
		}

		retriable := func(err error) bool {
			return err != nil
		}
		err := k8sretry.OnError(k8sretry.DefaultRetry, retriable, func() error {
			cr := &apiv1.PerconaServerMySQLRestore{}
			if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
				return errors.Wrapf(err, "get %v", req.String())
			}

			cr.Status = status
			log.Info("Updating status", "state", cr.Status.State)
			if err := r.Status().Update(ctx, cr); err != nil {
				return errors.Wrap(err, "update status")
			}

			if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
				return errors.Wrapf(err, "get %v", req.String())
			}
			if cr.Status.State != status.State {
				return errors.Errorf("status %s was not updated to %s", cr.Status.State, status.State)
			}
			return nil
		})
		if err != nil {
			log.Error(err, "failed to update status")
			return
		}

		log.V(1).Info("status updated", "state", status.State)
	}()

	switch status.State {
	case apiv1.RestoreFailed, apiv1.RestoreSucceeded:
		return ctrl.Result{}, nil
	}

	cluster := &apiv1.PerconaServerMySQL{}
	nn := types.NamespacedName{Name: cr.Spec.ClusterName, Namespace: cr.Namespace}
	if err := r.Get(ctx, nn, cluster); err != nil {
		if k8serrors.IsNotFound(err) {
			status.State = apiv1.RestoreError
			status.StateDesc = fmt.Sprintf("PerconaServerMySQL %s in namespace %s is not found", cr.Spec.ClusterName, cr.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, errors.Wrapf(err, "get cluster %s", nn)
	}
	if err := cluster.CheckNSetDefaults(ctx, r.ServerVersion); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "check n set defaults")
	}

	restoreList := &apiv1.PerconaServerMySQLRestoreList{}
	if err := r.List(ctx, restoreList, &client.ListOptions{Namespace: cr.Namespace}); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "get restore jobs list")
	}
	for _, restore := range restoreList.Items {
		if restore.Spec.ClusterName != cr.Spec.ClusterName || restore.Name == cr.Name {
			continue
		}

		switch restore.Status.State {
		case apiv1.RestoreSucceeded, apiv1.RestoreFailed, apiv1.RestoreError, apiv1.RestoreNew:
		default:
			status.State = apiv1.RestoreNew
			status.StateDesc = fmt.Sprintf("PerconaServerMySQLRestore %s is already running", restore.Name)
			log.Info("PerconaServerMySQLRestore is already running", "restore", restore.Name)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	if err := r.validate(ctx, cr, cluster); err != nil {
		status.State = apiv1.RestoreError
		status.StateDesc = errors.Cause(err).Error()
		return ctrl.Result{}, nil
	}

	// The above code is to prevent multiple restores from running at the same time. It works only if restore job is created.
	// But if multiple restores are created at the same time, then the above code will not work, because there are no restore jobs yet.
	// Therefore, we need to use sync.Map to prevent multiple restores from creating restore jobs at the same time.
	if _, ok := r.sm.LoadOrStore(cr.Spec.ClusterName, 1); ok {
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}
	defer r.sm.Delete(cr.Spec.ClusterName)

	if cr.Spec.PITR != nil {
		status.State = apiv1.RestoreStarting
		if err := r.reconcilePITRConfig(ctx, cr, cluster); err != nil {
			status.StateDesc = errors.Wrap(err, "reconcile pitr config").Error()
			return ctrl.Result{}, errors.Wrap(err, "reconcile pitr config")
		}
		status.StateDesc = ""
	}

	log.Info("Pausing cluster", "cluster", cluster.Name)
	if err := r.pauseCluster(ctx, cluster); err != nil {
		if errors.Is(err, ErrWaitingTermination) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, errors.Wrap(err, "pause cluster")
	}
	log.Info("Cluster paused", "cluster", cluster.Name)

	if cluster.Spec.MySQL.IsGR() {
		if err := r.removeBootstrapCondition(ctx, cluster); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "remove bootstrap condition")
		}
	}

	job := &batchv1.Job{}
	nn = types.NamespacedName{Name: xtrabackup.RestoreJobName(cluster, cr), Namespace: req.Namespace}
	err = r.Get(ctx, nn, job)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, errors.Wrapf(err, "get job %s", nn)
	}

	if k8serrors.IsNotFound(err) {
		log.Info("Creating restore job", "jobName", nn.Name)

		restorer, err := r.getRestorer(ctx, cr, cluster)
		if err != nil {
			status.State = apiv1.RestoreError
			status.StateDesc = err.Error()
			return ctrl.Result{}, nil
		}
		job, err := restorer.Job()
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "get job")
		}

		if err := r.Create(ctx, job); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "create job %s/%s", job.Namespace, job.Name)
		}

		return ctrl.Result{}, nil
	}

	switch status.State {
	case apiv1.RestoreStarting, apiv1.RestoreRunning:
		if job.Status.Active > 0 {
			status.State = apiv1.RestoreRunning
			return ctrl.Result{}, nil
		}

		for _, cond := range job.Status.Conditions {
			if cond.Status != corev1.ConditionTrue {
				continue
			}

			switch cond.Type {
			case batchv1.JobFailed:
				status.State = apiv1.RestoreFailed
			case batchv1.JobComplete:
				if cr.Spec.PITR != nil {
					pitrState, err := r.reconcilePITRJob(ctx, cr, cluster)
					if err != nil {
						return ctrl.Result{}, errors.Wrap(err, "reconcile pitr job")
					}
					status.State = pitrState
				} else {
					status.State = apiv1.RestoreSucceeded
				}
			}
		}
	case apiv1.RestoreFailed, apiv1.RestoreSucceeded:
		return ctrl.Result{}, nil
	default:
		status.State = apiv1.RestoreStarting
	}

	if status.State == apiv1.RestoreSucceeded {
		if cluster.CompareVersion("1.1.0") >= 0 || cluster.Spec.MySQL.IsGR() {
			if err := r.deletePVCs(ctx, cluster); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "delete PVCs")
			}
		}
		if err := r.unpauseCluster(ctx, cluster); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "unpause cluster")
		}
		log.Info("PerconaServerMySQLRestore is finished", "restore", cr.Name, "cluster", cluster.Name)
	}

	return ctrl.Result{}, nil
}

func (r *PerconaServerMySQLRestoreReconciler) reconcilePITRConfig(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQLRestore,
	cluster *apiv1.PerconaServerMySQL,
) error {
	cm := pitr.BinlogsConfigMap(cluster, cr)
	if err := r.Get(ctx, client.ObjectKeyFromObject(cm), new(corev1.ConfigMap)); err == nil {
		return nil
	}

	binlogs, err := r.searchBinlogs(ctx, cr, cluster)
	if err != nil {
		return errors.Wrap(err, "search binlogs")
	}
	if len(binlogs) == 0 {
		return errors.New("no binlogs found for the given PITR target")
	}

	data, err := json.Marshal(binlogs)
	if err != nil {
		return errors.Wrap(err, "marshal binlog entries")
	}

	cm.Data = make(map[string]string)
	cm.Data[pitr.BinlogsConfigKey] = string(data)

	if err := controllerutil.SetControllerReference(cr, cm, r.Scheme); err != nil {
		return errors.Wrapf(err, "set controller reference to ConfigMap %s/%s", cm.Namespace, cm.Name)
	}
	if err := r.Create(ctx, cm); err != nil {
		return errors.Wrapf(err, "create binlogs configmap %s/%s", cm.Namespace, cm.Name)
	}

	return nil
}

func (r *PerconaServerMySQLRestoreReconciler) reconcilePITRJob(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQLRestore,
	cluster *apiv1.PerconaServerMySQL,
) (apiv1.RestoreState, error) {
	log := logf.FromContext(ctx)

	pitrJob := &batchv1.Job{}
	nn := types.NamespacedName{Name: pitr.JobName(cluster, cr), Namespace: cr.Namespace}
	err := r.Get(ctx, nn, pitrJob)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return "", errors.Wrapf(err, "get pitr job %s", nn)
		}

		log.Info("Creating PITR restore job", "jobName", nn.Name)

		bcp, err := getBackup(ctx, r.Client, cr, cluster)
		if err != nil {
			return "", errors.Wrap(err, "get backup")
		}

		initImage, err := k8s.InitImage(ctx, r.Client, cluster, cluster.Spec.Backup)
		if err != nil {
			return "", errors.Wrap(err, "get operator image")
		}

		job := pitr.RestoreJob(cluster, cr, bcp.Status.Storage, initImage)
		if err := controllerutil.SetControllerReference(cr, job, r.Scheme); err != nil {
			return "", errors.Wrapf(err, "set controller reference to Job %s/%s", job.Namespace, job.Name)
		}

		if err := r.Create(ctx, job); err != nil {
			return "", errors.Wrapf(err, "create pitr job %s/%s", job.Namespace, job.Name)
		}

		return apiv1.RestoreRunning, nil
	}

	if pitrJob.Status.Active > 0 {
		return apiv1.RestoreRunning, nil
	}

	for _, cond := range pitrJob.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}

		switch cond.Type {
		case batchv1.JobFailed:
			return apiv1.RestoreFailed, nil
		case batchv1.JobComplete:
			return apiv1.RestoreSucceeded, nil
		}
	}

	return apiv1.RestoreRunning, nil
}

func (r *PerconaServerMySQLRestoreReconciler) searchBinlogs(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQLRestore,
	cluster *apiv1.PerconaServerMySQL,
) ([]binlogserver.BinlogEntry, error) {
	if cr.Spec.PITR == nil {
		return nil, errors.New("pitr spec is not set")
	}

	var resp *binlogserver.SearchResponse
	var err error

	switch cr.Spec.PITR.Type {
	case apiv1.PITRDate:
		ts := strings.Replace(cr.Spec.PITR.Date, " ", "T", 1)
		resp, err = binlogserver.SearchByTimestamp(ctx, r.Client, r.ClientCmd, cluster, ts)
	case apiv1.PITRGtid:
		resp, err = binlogserver.SearchByGTID(ctx, r.Client, r.ClientCmd, cluster, cr.Spec.PITR.GTID)
	default:
		return nil, errors.Errorf("unknown PITR type: %s", cr.Spec.PITR.Type)
	}
	if err != nil {
		return nil, errors.Wrap(err, "search binlogs")
	}

	if resp.Status != "success" {
		return nil, errors.Errorf("binlog search failed with status: %s", resp.Status)
	}

	return resp.Result, nil
}

func (r *PerconaServerMySQLRestoreReconciler) deletePVCs(ctx context.Context, cluster *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	pvcs, err := k8s.PVCsByLabels(ctx, r.Client, mysql.MatchLabels(cluster), cluster.Namespace)
	if err != nil {
		return errors.Wrap(err, "get PVC list")
	}

	for _, pvc := range pvcs {
		if pvc.Name == fmt.Sprintf("%s-%s-mysql-0", mysql.DataVolumeName, cluster.Name) {
			continue
		}

		if err := r.Delete(ctx, &pvc); err != nil {
			if !k8serrors.IsNotFound(err) {
				log.Error(err, "failed to delete PVC")
			}
			continue
		}

		log.Info("Deleted PVC after restore", "pvc", pvc.Name)
	}

	return nil
}

func (r *PerconaServerMySQLRestoreReconciler) removeBootstrapCondition(ctx context.Context, cluster *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		c := &apiv1.PerconaServerMySQL{}
		nn := types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}
		if err := r.Get(ctx, nn, c); err != nil {
			return err
		}

		meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
			Type:    apiv1.ConditionInnoDBClusterBootstrapped,
			Status:  metav1.ConditionFalse,
			Reason:  apiv1.ConditionInnoDBClusterBootstrapped,
			Message: "InnoDB cluster is not bootstrapped after restore",
		})

		return r.Client.Status().Update(ctx, c)
	})

	log.Info("Set condition to false", "condition", apiv1.ConditionInnoDBClusterBootstrapped)

	return err
}

func (r *PerconaServerMySQLRestoreReconciler) pauseCluster(ctx context.Context, cluster *apiv1.PerconaServerMySQL) error {
	err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		c := &apiv1.PerconaServerMySQL{}
		nn := types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}
		if err := r.Get(ctx, nn, c); err != nil {
			return err
		}

		c.Spec.Pause = true

		if err := r.Patch(ctx, c, client.MergeFrom(cluster)); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return errors.Wrapf(err, "patch cluster %s/%s", cluster.Namespace, cluster.Name)
	}

	sts := &appsv1.StatefulSet{}
	nn := types.NamespacedName{Name: mysql.Name(cluster), Namespace: cluster.Namespace}
	if err := r.Get(ctx, nn, sts); err != nil {
		return errors.Wrapf(err, "get statefulset %s", nn)
	}

	if sts.Status.Replicas != 0 {
		return ErrWaitingTermination
	}

	switch cluster.Spec.MySQL.ClusterType {
	case apiv1.ClusterTypeAsync:
		nn = types.NamespacedName{Name: orchestrator.Name(cluster), Namespace: cluster.Namespace}
		err := r.Get(ctx, nn, sts)
		if client.IgnoreNotFound(err) != nil {
			return errors.Wrapf(err, "get statefulset %s", nn)
		}
		if !k8serrors.IsNotFound(err) && sts.Status.Replicas != 0 {
			return ErrWaitingTermination
		}
	case apiv1.ClusterTypeGR:
		if cluster.HAProxyEnabled() {
			sts := new(appsv1.StatefulSet)
			nn = types.NamespacedName{Name: haproxy.Name(cluster), Namespace: cluster.Namespace}
			if err := r.Get(ctx, nn, sts); err != nil {
				return errors.Wrapf(err, "get deployment %s", nn)
			}
			if sts.Status.Replicas != 0 {
				return ErrWaitingTermination
			}
		}

		if cluster.RouterEnabled() {
			deployment := new(appsv1.Deployment)
			nn = types.NamespacedName{Name: router.Name(cluster), Namespace: cluster.Namespace}
			if err := r.Get(ctx, nn, deployment); err != nil {
				return errors.Wrapf(err, "get deployment %s", nn)
			}
			if deployment.Status.Replicas != 0 {
				return ErrWaitingTermination
			}
		}
	}

	return nil
}

func (r *PerconaServerMySQLRestoreReconciler) unpauseCluster(ctx context.Context, cluster *apiv1.PerconaServerMySQL) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		c := &apiv1.PerconaServerMySQL{}
		nn := types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}
		if err := r.Get(ctx, nn, c); err != nil {
			return err
		}

		c.Spec.Pause = false

		if err := r.Patch(ctx, c, client.MergeFrom(cluster)); err != nil {
			return err
		}

		return nil
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *PerconaServerMySQLRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.PerconaServerMySQLRestore{}).
		Named("psrestore-controller").
		Owns(&batchv1.Job{}).
		Complete(r)
}

func (r *PerconaServerMySQLRestoreReconciler) validate(ctx context.Context, cr *apiv1.PerconaServerMySQLRestore, cluster *apiv1.PerconaServerMySQL) error {
	bcp, err := getBackup(ctx, r.Client, cr, cluster)
	if err != nil {
		return err
	}

	if bs := cr.Spec.BackupSource; bs != nil {
		storage := bs.Storage
		destination := bs.Destination
		if destination == "" {
			return errors.New("backupSource.destination is empty")
		}
		if storage == nil {
			return errors.New("backupSource.storage is empty")
		}
	} else {
		storageName := bcp.Spec.StorageName
		var ok bool
		_, ok = cluster.Spec.Backup.Storages[storageName]
		if !ok {
			return errors.Errorf("%s not found in spec.backup.storages in PerconaServerMySQL CustomResource", storageName)
		}
	}
	if bcp.Status.Storage == nil {
		return errors.New("backup's status.storage is empty")
	}
	restorer, err := r.getRestorer(ctx, cr, cluster)
	if err != nil {
		return errors.Wrap(err, "get restorer")
	}
	if err := restorer.Validate(ctx); err != nil {
		return err
	}

	return nil
}
