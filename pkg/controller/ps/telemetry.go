package ps

import (
	"context"
	"fmt"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/telemetry"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *PerconaServerMySQLReconciler) reconcileScheduledTelemetrySending(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcileScheduledTelemetrySending")

	jn := telemetryJobName(cr)
	existingJob, ok := r.Crons.telemetryJobs.Load(jn)
	if !telemetryEnabled() {
		if ok {
			r.Crons.telemetryJobs.Delete(jn)
		}
		return nil
	}
	job := telemetryJob{}
	if ok {
		job = existingJob.(telemetryJob)
	}

	configuredSchedule := telemetry.Schedule()

	if job.cronSchedule == configuredSchedule {
		return nil
	}

	log.Info("removing existing telemetry job because the configured schedule changed", "old", job.cronSchedule, "new", configuredSchedule)
	r.Crons.telemetryJobs.Delete(jn)

	telemetryService, err := telemetry.NewTelemetryService()

	id, err := r.Crons.addFuncWithSeconds(configuredSchedule, func() {
		localCr := &apiv1alpha1.PerconaServerMySQL{}
		err := r.Client.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, localCr)
		if k8serrors.IsNotFound(err) {
			log.Info("cluster is not found, deleting the job",
				"name", jn, "cluster", cr.Name, "namespace", cr.Namespace)
			r.Crons.telemetryJobs.Delete(jn)
			return
		}
		if err != nil {
			log.Error(err, "failed to get cr")
			return
		}

		if cr.Status.State != apiv1alpha1.StateReady {
			log.Info("cluster is not ready yet")
			return
		}

		err = telemetryService.SendReport(ctx, cr, r.ServerVersion)
		if err != nil {
			log.Error(err, "failed to send telemetry report")
		}
	})
	if err != nil {
		return err
	}

	log.Info("adding new job", "name", jn, "schedule", configuredSchedule)

	r.Crons.telemetryJobs.Store(jn, telemetryJob{
		scheduleJob:  scheduleJob{jobID: id},
		cronSchedule: configuredSchedule,
	})

	// send telemetry on cluster creation
	err = telemetryService.SendReport(ctx, cr, r.ServerVersion)
	if err != nil {
		log.Error(err, "failed to send telemetry report")
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
