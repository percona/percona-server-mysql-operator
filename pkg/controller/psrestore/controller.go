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
	"fmt"
	"time"

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

	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/router"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

// PerconaServerMySQLRestoreReconciler reconciles a PerconaServerMySQLRestore object
type PerconaServerMySQLRestoreReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ServerVersion *platform.ServerVersion
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

	cr := &apiv1alpha1.PerconaServerMySQLRestore{}
	err := r.Client.Get(ctx, req.NamespacedName, cr)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "get CR %s", req.NamespacedName)
	}

	status := cr.Status
	if status.State == apiv1alpha1.RestoreError {
		status.State = apiv1alpha1.RestoreNew
		status.StateDesc = ""
	}

	defer func() {
		if status.State == cr.Status.State {
			return
		}

		err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
			cr := &apiv1alpha1.PerconaServerMySQLRestore{}
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

	cluster := &apiv1alpha1.PerconaServerMySQL{}
	nn := types.NamespacedName{Name: cr.Spec.ClusterName, Namespace: cr.Namespace}
	if err := r.Client.Get(ctx, nn, cluster); err != nil {
		if k8serrors.IsNotFound(err) {
			status.State = apiv1alpha1.RestoreError
			status.StateDesc = fmt.Sprintf("PerconaServerMySQL %s in namespace %s is not found", cr.Spec.ClusterName, cr.Namespace)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, errors.Wrapf(err, "get cluster %s", nn)
	}

	var destination string
	var storage *apiv1alpha1.BackupStorageSpec
	if cr.Spec.BackupName != "" {
		backup := &apiv1alpha1.PerconaServerMySQLBackup{}
		nn := types.NamespacedName{Name: cr.Spec.BackupName, Namespace: cr.Namespace}
		if err := r.Client.Get(ctx, nn, backup); err != nil {
			if k8serrors.IsNotFound(err) {
				status.State = apiv1alpha1.RestoreError
				status.StateDesc = fmt.Sprintf("PerconaServerMySQLBackup %s in namespace %s is not found", cr.Spec.BackupName, cr.Namespace)
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, errors.Wrapf(err, "get backup %s", nn)
		}
		destination = backup.Status.Destination
		storageName := backup.Spec.StorageName
		var ok bool
		storage, ok = cluster.Spec.Backup.Storages[storageName]
		if !ok {
			status.State = apiv1alpha1.RestoreError
			status.StateDesc = fmt.Sprintf("%s not found in spec.backup.storages in PerconaServerMySQL CustomResource", storageName)
			return ctrl.Result{}, nil
		}
	} else if cr.Spec.BackupSource != nil {
		storage = cr.Spec.BackupSource.Storage
		destination = cr.Spec.BackupSource.Destination
		if destination == "" {
			status.State = apiv1alpha1.RestoreError
			status.StateDesc = "backupSource.destination is empty"
			return ctrl.Result{}, nil
		}
		if storage == nil {
			status.State = apiv1alpha1.RestoreError
			status.StateDesc = "backupSource.storage is empty"
			return ctrl.Result{}, nil
		}
	} else {
		status.State = apiv1alpha1.RestoreError
		status.StateDesc = "backupName and backupSource are empty"
		return ctrl.Result{}, nil
	}

	switch status.State {
	case apiv1alpha1.RestoreFailed, apiv1alpha1.RestoreSucceeded:
		return ctrl.Result{}, nil
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
	err = r.Client.Get(ctx, nn, job)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, errors.Wrapf(err, "get job %s", nn)
	}

	if k8serrors.IsNotFound(err) {
		log.Info("Creating restore job", "jobName", nn.Name)

		initImage, err := k8s.InitImage(ctx, r.Client, cluster, cluster.Spec.Backup)
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "get operator image")
		}

		pvcName := fmt.Sprintf("%s-%s-mysql-0", mysql.DataVolumeName, cluster.Name)
		job := xtrabackup.RestoreJob(cluster, destination, cr, storage, initImage, pvcName)

		switch storage.Type {
		case apiv1alpha1.BackupStorageS3:
			if storage.S3 == nil {
				return ctrl.Result{}, errors.New("s3 stanza is required in storage")
			}

			nn := types.NamespacedName{Name: storage.S3.CredentialsSecret, Namespace: cr.Namespace}
			exists, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{})
			if err != nil {
				return ctrl.Result{}, errors.Wrapf(err, "check %s exists", nn)
			}

			if !exists {
				status.State = apiv1alpha1.RestoreError
				status.StateDesc = fmt.Sprintf("secret %s is not found", storage.S3.CredentialsSecret)
				return ctrl.Result{}, errors.Wrapf(err, "secret %s not found", nn)
			}

			if err := xtrabackup.SetStorageS3(job, storage.S3); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "set storage s3")
			}
		case apiv1alpha1.BackupStorageGCS:
			if storage.GCS == nil {
				return ctrl.Result{}, errors.New("gcs stanza is required in storage")
			}

			nn := types.NamespacedName{Name: storage.GCS.CredentialsSecret, Namespace: cr.Namespace}
			exists, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{})
			if err != nil {
				return ctrl.Result{}, errors.Wrapf(err, "check %s exists", nn)
			}

			if !exists {
				status.State = apiv1alpha1.RestoreError
				status.StateDesc = fmt.Sprintf("secret %s is not found", storage.GCS.CredentialsSecret)
				return ctrl.Result{}, errors.Wrapf(err, "secret %s not found", nn)
			}

			if err := xtrabackup.SetStorageGCS(job, storage.GCS); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "set storage GCS")
			}
		case apiv1alpha1.BackupStorageAzure:
			if storage.Azure == nil {
				return ctrl.Result{}, errors.New("azure stanza is required in storage")
			}

			nn := types.NamespacedName{Name: storage.Azure.CredentialsSecret, Namespace: cr.Namespace}
			exists, err := k8s.ObjectExists(ctx, r.Client, nn, &corev1.Secret{})
			if err != nil {
				return ctrl.Result{}, errors.Wrapf(err, "check %s exists", nn)
			}

			if !exists {
				status.State = apiv1alpha1.RestoreError
				status.StateDesc = fmt.Sprintf("secret %s is not found", storage.Azure.CredentialsSecret)
				return ctrl.Result{}, errors.Wrapf(err, "secret %s not found", nn)
			}

			if err := xtrabackup.SetStorageAzure(job, storage.Azure); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "set storage Azure")
			}
		default:
			return ctrl.Result{}, errors.Errorf("Storage type %s is not supported", storage.Type)
		}

		if err := controllerutil.SetControllerReference(cr, job, r.Scheme); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "set controller reference to Job %s/%s", job.Namespace, job.Name)
		}

		if err := r.Client.Create(ctx, job); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "create job %s/%s", job.Namespace, job.Name)
		}

		return ctrl.Result{}, nil
	}

	switch status.State {
	case apiv1alpha1.RestoreStarting:
		if job.Status.Active > 0 {
			status.State = apiv1alpha1.RestoreRunning
		}
	case apiv1alpha1.RestoreRunning:
		if job.Status.Active > 0 {
			return ctrl.Result{}, nil
		}

		for _, cond := range job.Status.Conditions {
			if cond.Status != corev1.ConditionTrue {
				continue
			}

			switch cond.Type {
			case batchv1.JobFailed:
				status.State = apiv1alpha1.RestoreFailed
			case batchv1.JobComplete:
				status.State = apiv1alpha1.RestoreSucceeded
			}
		}
	case apiv1alpha1.RestoreFailed, apiv1alpha1.RestoreSucceeded:
		return ctrl.Result{}, nil
	default:
		status.State = apiv1alpha1.RestoreStarting
	}

	if status.State == apiv1alpha1.RestoreSucceeded {
		if cluster.Spec.MySQL.IsGR() {
			if err := r.deletePVCs(ctx, cluster); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "delete PVCs")
			}
		}
		if err := r.unpauseCluster(ctx, cluster); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "unpause cluster")
		}
	}

	return ctrl.Result{}, nil
}

func (r *PerconaServerMySQLRestoreReconciler) deletePVCs(ctx context.Context, cluster *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	pvcs, err := k8s.PVCsByLabels(ctx, r.Client, mysql.MatchLabels(cluster))
	if err != nil {
		return errors.Wrap(err, "get PVC list")
	}

	for _, pvc := range pvcs {
		if pvc.Name == fmt.Sprintf("%s-%s-mysql-0", mysql.DataVolumeName, cluster.Name) {
			continue
		}

		if err := r.Client.Delete(ctx, &pvc); err != nil {
			log.Error(err, "failed to delete PVC")
			continue
		}

		log.Info("Deleted PVC after restore", "pvc", pvc.Name)
	}

	return nil
}

func (r *PerconaServerMySQLRestoreReconciler) removeBootstrapCondition(ctx context.Context, cluster *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		c := &apiv1alpha1.PerconaServerMySQL{}
		nn := types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}
		if err := r.Client.Get(ctx, nn, c); err != nil {
			return err
		}

		meta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{
			Type:               apiv1alpha1.ConditionInnoDBClusterBootstrapped,
			Status:             metav1.ConditionFalse,
			Reason:             apiv1alpha1.ConditionInnoDBClusterBootstrapped,
			Message:            "InnoDB cluster is not bootstrapped after restore",
			LastTransitionTime: metav1.Now(),
		})

		return r.Client.Status().Update(ctx, c)
	})

	log.Info("Set condition to false", "condition", apiv1alpha1.ConditionInnoDBClusterBootstrapped)

	return err
}

func (r *PerconaServerMySQLRestoreReconciler) pauseCluster(ctx context.Context, cluster *apiv1alpha1.PerconaServerMySQL) error {
	err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		c := &apiv1alpha1.PerconaServerMySQL{}
		nn := types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}
		if err := r.Client.Get(ctx, nn, c); err != nil {
			return err
		}

		c.Spec.Pause = true

		if err := r.Client.Patch(ctx, c, client.MergeFrom(cluster)); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return errors.Wrapf(err, "patch cluster %s/%s", cluster.Namespace, cluster.Name)
	}

	sts := &appsv1.StatefulSet{}
	nn := types.NamespacedName{Name: mysql.Name(cluster), Namespace: cluster.Namespace}
	if err := r.Client.Get(ctx, nn, sts); err != nil {
		return errors.Wrapf(err, "get statefulset %s", nn)
	}

	if sts.Status.Replicas != 0 {
		return ErrWaitingTermination
	}

	switch cluster.Spec.MySQL.ClusterType {
	case apiv1alpha1.ClusterTypeAsync:
		nn = types.NamespacedName{Name: orchestrator.Name(cluster), Namespace: cluster.Namespace}
		err := r.Client.Get(ctx, nn, sts)
		if client.IgnoreNotFound(err) != nil {
			return errors.Wrapf(err, "get statefulset %s", nn)
		}
		if !k8serrors.IsNotFound(err) && sts.Status.Replicas != 0 {
			return ErrWaitingTermination
		}
	case apiv1alpha1.ClusterTypeGR:
		deployment := new(appsv1.Deployment)
		nn = types.NamespacedName{Name: router.Name(cluster), Namespace: cluster.Namespace}
		if err := r.Client.Get(ctx, nn, deployment); err != nil {
			return errors.Wrapf(err, "get deployment %s", nn)
		}
		if deployment.Status.Replicas != 0 {
			return ErrWaitingTermination
		}
	}

	return nil
}

func (r *PerconaServerMySQLRestoreReconciler) unpauseCluster(ctx context.Context, cluster *apiv1alpha1.PerconaServerMySQL) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		c := &apiv1alpha1.PerconaServerMySQL{}
		nn := types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}
		if err := r.Client.Get(ctx, nn, c); err != nil {
			return err
		}

		c.Spec.Pause = false

		if err := r.Client.Patch(ctx, c, client.MergeFrom(cluster)); err != nil {
			return err
		}

		return nil
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *PerconaServerMySQLRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.PerconaServerMySQLRestore{}).
		Named("psrestore-controller").
		Owns(&batchv1.Job{}).
		Complete(r)
}
