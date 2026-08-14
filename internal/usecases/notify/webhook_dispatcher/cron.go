package webhook_dispatcher

import (
	"context"
	"time"
)

// Cron adapts the dispatcher to the cron orchestrator's Job interface.
type Cron struct {
	uc  *Usecase
	cfg Config
}

func NewCron(uc *Usecase, cfg Config) *Cron { return &Cron{uc: uc, cfg: cfg} }

func (c *Cron) Name() string { return "webhook_dispatcher" }

// Interval returning zero leaves the job unscheduled. Note that this also stops
// pruning, so an operator switching it off keeps the outbox forever.
func (c *Cron) Interval() time.Duration { return c.cfg.Interval.Std() }

func (c *Cron) CronRun(ctx context.Context) error {
	_, err := c.uc.Execute(ctx, Input{})
	return err
}
