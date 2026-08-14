package tron_watcher

import (
	"context"
	"time"
)

// Cron adapts the watcher to the cron orchestrator's Job interface.
type Cron struct {
	uc  *Usecase
	cfg Config
}

func NewCron(uc *Usecase, cfg Config) *Cron { return &Cron{uc: uc, cfg: cfg} }

func (c *Cron) Name() string { return "tron_watcher" }

// Interval returning zero leaves the job unscheduled, which is how the whole
// TRON network is switched off — there is no separate enable flag.
func (c *Cron) Interval() time.Duration { return c.cfg.WatchInterval.Std() }

func (c *Cron) CronRun(ctx context.Context) error {
	_, err := c.uc.Execute(ctx, Input{})
	return err
}
