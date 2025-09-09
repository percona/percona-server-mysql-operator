package ps

import (
	"context"
	"fmt"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/telemetry"
	"github.com/pkg/errors"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *PerconaServerMySQLReconciler) reconcileScheduledTelemetrySending(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	logger := logf.FromContext(ctx)

	if cr.Status.State != apiv1alpha1.StateReady {
		return nil
	}

	jobName := telemetryJobName(cr)
	existingJob, existingJobFound := r.Crons.telemetryJobs.Load(jobName)
	if !telemetryEnabled() {
		if existingJobFound {
			r.Crons.deleteTelemetryJob(jobName)
		}
		return nil
	}
	job := telemetryJob{}
	if existingJobFound {
		job = existingJob.(telemetryJob)
	}

	configuredSchedule, scheduleFound, err := telemetry.Schedule()
	if err != nil {
		logger.Error(err, "failed to get the schedule for the telemetry job")
		return nil
	}

	// If an existing job is found but no schedule is set, assume the job is already fully configured.
	if existingJobFound && !scheduleFound {
		return nil
	}

	// If the configured schedule is identical to the existing job's schedule, no action is needed.
	if job.cronSchedule == configuredSchedule {
		return nil
	}

	logger.Info("removing existing telemetry job because the configured schedule changed", "old", job.cronSchedule, "new", configuredSchedule)
	r.Crons.deleteTelemetryJob(jobName)

	id, err := r.Crons.addFuncWithSeconds(configuredSchedule, r.telemetrySendingHandlerFunc(ctx, cr, jobName))
	if err != nil {
		return err
	}

	logger.Info("adding new job", "name", jobName, "schedule", configuredSchedule)

	r.Crons.telemetryJobs.Store(jobName, telemetryJob{
		scheduleJob:  scheduleJob{jobID: id},
		cronSchedule: configuredSchedule,
	})

	logger.Info("sending telemetry on cluster start")

	err = r.sendTelemetry(ctx, cr)
	if err != nil {
		logger.Error(err, "failed to send telemetry report")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) telemetrySendingHandlerFunc(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, jobName string) func() {
	return func() {
		logger := logf.FromContext(ctx)

		localCr := &apiv1alpha1.PerconaServerMySQL{}
		err := r.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, localCr)
		if k8serrors.IsNotFound(err) {
			logger.Info("cluster is not found, deleting the job",
				"name", jobName, "cluster", cr.Name, "namespace", cr.Namespace)
			r.Crons.deleteTelemetryJob(jobName)
			return
		}
		if err != nil {
			logger.Error(err, "failed to get cr")
			return
		}

		if localCr.Status.State != apiv1alpha1.StateReady {
			logger.Info("cluster is not ready yet")
			return
		}

		logger.Info("sending telemetry on schedule...", "job name", jobName)

		err = r.sendTelemetry(ctx, localCr)
		if err != nil {
			logger.Error(err, "failed to send telemetry report")
		}
	}
}

func (r *PerconaServerMySQLReconciler) sendTelemetry(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	telemetryService, err := telemetry.NewTelemetryService()
	if err != nil {
		return errors.Wrap(err, "telemetry service")
	}
	err = telemetryService.SendReport(ctx, cr, r.ServerVersion)
	if err != nil {
		return errors.Wrap(err, "failed to send telemetry report")
	}
	return nil
}

type telemetryJob struct {
	scheduleJob
	cronSchedule string
}

func telemetryJobName(cr *apiv1alpha1.PerconaServerMySQL) string {
	jobName := "telemetry"
	nn := types.NamespacedName{
		Name:      cr.Name,
		Namespace: cr.Namespace,
	}
	return fmt.Sprintf("%s/%s", jobName, nn.String())
}

func (r *CronRegistry) deleteTelemetryJob(jobName string) {
	job, ok := r.telemetryJobs.LoadAndDelete(jobName)
	if !ok {
		return
	}
	r.crons.Remove(job.(telemetryJob).jobID)
}
