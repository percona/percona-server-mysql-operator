package schedule

import (
	"sync"

	"github.com/robfig/cron/v3"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

type BackupJob struct {
	apiv1alpha1.BackupScheduleSpec

	JobID cron.EntryID
}

type CronRegistry struct {
	Crons      *cron.Cron
	BackupJobs *sync.Map
}

func NewCronRegistry() CronRegistry {
	r := CronRegistry{
		Crons:      cron.New(),
		BackupJobs: new(sync.Map),
	}

	r.Crons.Start()

	return r
}
