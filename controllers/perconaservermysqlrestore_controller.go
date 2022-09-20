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

package controllers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sretry "k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
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
	l := log.FromContext(ctx).WithName("PerconaServerMySQLRestore").WithValues("name", req.Name, "namespace", req.Namespace)

	cr := &apiv1alpha1.PerconaServerMySQLRestore{}
	err := r.Client.Get(ctx, req.NamespacedName, cr)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "get CR %s", req.NamespacedName)
	}

	status := cr.Status

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
			l.Info("Updating status", "state", cr.Status.State)
			if err := r.Client.Status().Update(ctx, cr); err != nil {
				return errors.Wrapf(err, "update %v", req.NamespacedName.String())
			}

			return nil
		})
		if err != nil {
			l.Error(err, "failed to update status")
		}
	}()

	cluster := &apiv1alpha1.PerconaServerMySQL{}
	nn := types.NamespacedName{Name: cr.Spec.ClusterName, Namespace: cr.Namespace}
	if err := r.Client.Get(ctx, nn, cluster); err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "get cluster %s", nn)
	}

	var storageName string
	var destination string
	if cr.Spec.BackupName != "" {
		backup := &apiv1alpha1.PerconaServerMySQLBackup{}
		nn := types.NamespacedName{Name: cr.Spec.BackupName, Namespace: cr.Namespace}
		if err := r.Client.Get(ctx, nn, backup); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "get backup %s", nn)
		}
		storageName = backup.Spec.StorageName
		destination = backup.Status.Destination
	} else if cr.Spec.BackupSource != nil && cr.Spec.BackupSource.StorageName != "" {
		storageName = cr.Spec.BackupSource.StorageName
		destination = cr.Spec.BackupSource.Destination
	} else {
		return ctrl.Result{}, errors.New("spec.storageName and backupSource.storageName are empty")
	}

	storage, ok := cluster.Spec.Backup.Storages[storageName]
	if !ok {
		return ctrl.Result{}, errors.Errorf("%s not found in spec.backup.storages in PerconaServerMySQL CustomResource", storageName)
	}

	switch status.State {
	case apiv1alpha1.RestoreFailed, apiv1alpha1.RestoreSucceeded:
		return ctrl.Result{}, nil
	}

	l.Info("Pausing cluster", "cluster", cluster.Name)
	if err := r.pauseCluster(ctx, cluster); err != nil {
		if errors.Is(err, ErrWaitingTermination) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, errors.Wrap(err, "pause cluster")
	}
	l.Info("Cluster paused", "cluster", cluster.Name)

	job := &batchv1.Job{}
	nn = types.NamespacedName{Name: xtrabackup.RestoreJobName(cluster, cr), Namespace: req.Namespace}
	err = r.Client.Get(ctx, nn, job)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, errors.Wrapf(err, "get job %s", nn)
	}

	if k8serrors.IsNotFound(err) {
		l.Info("Creating restore job", "jobName", nn.Name)

		initImage, err := k8s.InitImage(ctx, r.Client)
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
		if err := r.unpauseCluster(ctx, cluster); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "unpause cluster")
		}
	}

	return ctrl.Result{}, nil
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

	sts = &appsv1.StatefulSet{}
	nn = types.NamespacedName{Name: orchestrator.Name(cluster), Namespace: cluster.Namespace}
	if err := r.Client.Get(ctx, nn, sts); err != nil {
		return errors.Wrapf(err, "get statefulset %s", nn)
	}

	if sts.Status.Replicas != 0 {
		return ErrWaitingTermination
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
		Owns(&batchv1.Job{}).
		Complete(r)
}
