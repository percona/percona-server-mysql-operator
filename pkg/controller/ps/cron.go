package ps

import (
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/robfig/cron/v3"
)

type CronRegistry struct {
	crons         *cron.Cron
	backupJobs    *sync.Map
	telemetryJobs *sync.Map
}

// NewCronRegistry creates a new cron registry.
func NewCronRegistry() CronRegistry {
	c := CronRegistry{
		crons:         cron.New(),
		backupJobs:    new(sync.Map),
		telemetryJobs: new(sync.Map),
	}

	c.crons.Start()

	return c
}

type scheduleJob struct {
	jobID cron.EntryID
}

// AddFuncWithSeconds does the same as cron.AddFunc but changes the schedule so that the function will run the exact second that this method is called.
func (r *CronRegistry) addFuncWithSeconds(spec string, cmd func()) (cron.EntryID, error) {
	schedule, err := cron.ParseStandard(spec)
	if err != nil {
		return 0, errors.Wrap(err, "failed to parse cron schedule")
	}
	schedule.(*cron.SpecSchedule).Second = uint64(1 << time.Now().Second())
	id := r.crons.Schedule(schedule, cron.FuncJob(cmd))
	return id, nil
}
