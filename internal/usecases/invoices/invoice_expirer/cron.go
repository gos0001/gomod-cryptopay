package invoice_expirer

import (
	"context"
	"time"

	"github.com/gos0001/gomod-cryptopay/internal/invoicecfg"
)

// Cron adapts the use case to the cron orchestrator's Job interface.
type Cron struct {
	uc  *Usecase
	cfg invoicecfg.Config
}

func NewCron(uc *Usecase, cfg invoicecfg.Config) *Cron {
	return &Cron{uc: uc, cfg: cfg}
}

func (c *Cron) Name() string { return "invoice_expirer" }

// Interval returns zero to stay unscheduled, which is the orchestrator's off
// switch. Switching this off also stops payment amounts from ever being
// released, so it is a heavier decision than it looks.
func (c *Cron) Interval() time.Duration { return c.cfg.ExpireInterval.Std() }

func (c *Cron) CronRun(ctx context.Context) error {
	_, err := c.uc.Execute(ctx, Input{})
	return err
}
