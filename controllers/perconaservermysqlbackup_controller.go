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

	"github.com/pkg/errors"
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

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
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
	l := log.FromContext(ctx).WithName("PerconaServerMySQLBackup")

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
		if status.State == cr.Status.State {
			return
		}

		err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
			cr := &apiv1alpha1.PerconaServerMySQLBackup{}
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
		return rr, errors.Wrapf(err, "get %v", nn.String())
	}

	if err := cluster.CheckNSetDefaults(r.ServerVersion); err != nil {
		return rr, errors.Wrapf(err, "check and set defaults for %v", nn.String())
	}

	switch cr.Status.State {
	case apiv1alpha1.BackupFailed, apiv1alpha1.BackupSucceeded:
		// run finalizers
		return rr, nil
	}

	if cluster.Spec.Backup == nil {
		l.Info("spec.backup stanza not found in PerconaServerMySQL CustomResource")
		return rr, nil
	}

	storage, ok := cluster.Spec.Backup.Storages[cr.Spec.StorageName]
	if !ok {
		return rr, errors.Errorf("%s not found in spec.backup.storages in PerconaServerMySQL CustomResource", cr.Spec.StorageName)
	}

	if cluster.Status.MySQL.State != apiv1alpha1.StateReady {
		l.Info("Cluster is not ready", "cluster", cr.Name)
		return rr, nil
	}

	j := &batchv1.Job{}
	nn = types.NamespacedName{Name: xtrabackup.JobName(cluster, cr), Namespace: req.Namespace}
	err := r.Client.Get(ctx, nn, j)
	if err != nil && !k8serrors.IsNotFound(err) {
		return rr, errors.Wrapf(err, "get job %v", nn.String())
	}

	destination := fmt.Sprintf("%s-%s-full", cr.ClusterName, cr.CreationTimestamp.Format("2006-01-02-15:04:05"))
	if k8serrors.IsNotFound(err) {
		l.Info("Creating backup job", "jobName", nn.Name)

		initImage, err := k8s.InitImage(ctx, r.Client)
		if err != nil {
			return rr, errors.Wrap(err, "get operator image")
		}

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
		default:
			return rr, errors.Errorf("storage type %s is not supported", storage.Type)
		}

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
		if j.Status.Active > 0 {
			status.State = apiv1alpha1.BackupRunning
		}
	case apiv1alpha1.BackupRunning:
		if j.Status.Active > 0 {
			return rr, nil
		}

		for _, cond := range j.Status.Conditions {
			if cond.Status != corev1.ConditionTrue {
				continue
			}

			switch cond.Type {
			case batchv1.JobFailed:
				status.State = apiv1alpha1.BackupFailed
			case batchv1.JobComplete:
				status.State = apiv1alpha1.BackupSucceeded
			}
		}
	case apiv1alpha1.BackupFailed, apiv1alpha1.BackupSucceeded:
		return rr, nil
	default:
		status.State = apiv1alpha1.BackupStarting
	}

	return rr, nil
}

func (r *PerconaServerMySQLBackupReconciler) getBackupSource(ctx context.Context, cluster *apiv1alpha1.PerconaServerMySQL) (string, error) {
	l := log.FromContext(ctx).WithName("getBackupSource")

	orcHost := orchestrator.APIHost(cluster)
	primary, err := orchestrator.ClusterPrimary(ctx, orcHost, cluster.ClusterHint())
	if err != nil {
		return "", errors.Wrap(err, "get primary")
	}

	var src string
	if len(primary.Replicas) < 1 {
		src = primary.Key.Hostname
		l.Info("no replicas found, using primary as the backup source", "source", src)
	} else {
		src = primary.Replicas[0].Hostname
		l.Info("using replica as the backup source", "source", src)
	}

	return src, nil
}
