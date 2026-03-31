package psbackup

import (
	"context"

	"github.com/pkg/errors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	k8sretry "k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

func (r *PerconaServerMySQLBackupReconciler) reconcileBackupJob(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQLBackup,
	cluster *apiv1.PerconaServerMySQL,
	status *apiv1.PerconaServerMySQLBackupStatus,
) error {
	if err := r.suspendJobIfNeeded(ctx, cr, cluster, status); err != nil {
		return errors.Wrap(err, "suspend job if needed")
	}

	if err := r.resumeJobIfNeeded(ctx, cr, cluster, status); err != nil {
		return errors.Wrap(err, "resume job if needed")
	}

	return nil
}

func (r *PerconaServerMySQLBackupReconciler) suspendJobIfNeeded(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQLBackup,
	cluster *apiv1.PerconaServerMySQL,
	status *apiv1.PerconaServerMySQLBackupStatus,
) error {
	if cluster.CanBackup() == nil {
		return nil
	}

	job := new(batchv1.Job)
	nn := xtrabackup.JobNamespacedName(cr)
	if err := r.Get(ctx, nn, job); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrapf(err, "get job %v", nn.String())
	}
	for _, cond := range job.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}

		switch cond.Type {
		case batchv1.JobSuspended, batchv1.JobComplete, batchv1.JobFailed:
			return nil
		}
	}

	if suspendedSpec := job.Spec.Suspend; suspendedSpec != nil && *suspendedSpec {
		return nil // already suspended
	}

	log := logf.FromContext(ctx)
	log.Info("Suspending backup job",
		"job", job.Name,
		"cluster status", cluster.Status.State,
		"ready mysql", cluster.Status.MySQL.Ready)

	if err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		job := new(batchv1.Job)
		nn := xtrabackup.JobNamespacedName(cr)
		if err := r.Get(ctx, nn, job); err != nil {
			return errors.Wrapf(err, "get job %v", nn.String())
		}

		job.Spec.Suspend = ptr.To(true)
		return r.Client.Update(ctx, job)
	}); err != nil {
		return errors.Wrap(err, "update job")
	}
	status.State = apiv1.BackupSuspended
	status.StateDesc = "suspended due to cluster not being ready"

	return nil
}

func (r *PerconaServerMySQLBackupReconciler) resumeJobIfNeeded(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQLBackup,
	cluster *apiv1.PerconaServerMySQL,
	status *apiv1.PerconaServerMySQLBackupStatus,
) error {
	if cluster.CanBackup() != nil {
		return nil
	}

	log := logf.FromContext(ctx)

	job := new(batchv1.Job)
	nn := xtrabackup.JobNamespacedName(cr)
	if err := r.Get(ctx, nn, job); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrapf(err, "get job %v", nn.String())
	}

	if suspendedSpec := job.Spec.Suspend; suspendedSpec != nil && !*suspendedSpec {
		return nil // already resumed
	}

	suspended := false
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobSuspended && cond.Status == corev1.ConditionTrue {
			suspended = true
			break
		}
	}

	if !suspended {
		return nil
	}

	log.Info("Resuming backup job",
		"job", job.Name,
		"cluster status", cluster.Status.State,
		"ready mysql", cluster.Status.MySQL.Ready)

	if err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		job := new(batchv1.Job)
		nn := xtrabackup.JobNamespacedName(cr)
		if err := r.Get(ctx, nn, job); err != nil {
			if k8serrors.IsNotFound(err) {
				return nil
			}
			return errors.Wrapf(err, "get job %v", nn.String())
		}

		job.Spec.Suspend = ptr.To(false)
		return r.Client.Update(ctx, job)
	}); err != nil {
		return errors.Wrap(err, "update job")
	}
	status.State = apiv1.BackupStarting
	status.StateDesc = ""

	return nil
}
