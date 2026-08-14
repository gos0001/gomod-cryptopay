// The Start and Stop policies below are yours to tune. The marker comments are
// not: the CLI splices at them, and deleting one silently stops the next
// generator from registering anything here.
package cron

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/gos0001/gomod-cryptopay/internal/usecases/chain/bsc_watcher"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/chain/tron_watcher"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/invoices/invoice_expirer"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/notify/webhook_dispatcher"
	// gostack:imports
)

// Job is one piece of periodic background work. A use case implements this from
// its own cron.go — one handler file per caller, named after the caller.
//
// CronRun does the work exactly once. The loop belongs to the orchestrator, so
// a failed run cannot kill the schedule.
type Job interface {
	Name() string
	Interval() time.Duration
	CronRun(ctx context.Context) error
}

// Cron owns every background schedule. Sequencing only: the work lives in the
// use cases.
type Cron struct {
	logger *zap.SugaredLogger
	cfg    Config
	jobs   []Job

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(
	logger *zap.SugaredLogger,
	cfg Config,
	invoiceExpirer *invoice_expirer.Cron,
	tronWatcher *tron_watcher.Cron,
	bscWatcher *bsc_watcher.Cron,
	webhookDispatcher *webhook_dispatcher.Cron,
	// gostack:params
) *Cron {
	c := &Cron{logger: logger, cfg: cfg}
	c.jobs = append(c.jobs, invoiceExpirer)
	c.jobs = append(c.jobs, tronWatcher)
	c.jobs = append(c.jobs, bscWatcher)
	c.jobs = append(c.jobs, webhookDispatcher)
	// gostack:routes

	return c
}

// Start gives each job its own goroutine and ticker, and returns immediately.
// The first run is on the first tick, not now — a job that must also run at
// boot adds a bootstrap.go handler beside its cron.go and gets both.
func (c *Cron) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)

	for _, job := range c.jobs {
		// A zero interval is how a job is switched off, and it is also the
		// value time.NewTicker panics on. Both reasons to stop here.
		if job.Interval() <= 0 {
			c.logger.Infow("cron job disabled", "job", job.Name())
			continue
		}

		c.logger.Infow("cron job scheduled", "job", job.Name(), "interval", job.Interval())
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.run(ctx, job)
		}()
	}
}

// run ticks until the context is cancelled. One goroutine per job means a run
// that outlives its interval delays the next tick instead of overlapping with
// itself — no guard needed, it falls out of the shape.
func (c *Cron) run(ctx context.Context, job Job) {
	ticker := time.NewTicker(job.Interval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A failed run is logged and the schedule continues. A background
			// job must not take the HTTP server down with it.
			if err := job.CronRun(ctx); err != nil && ctx.Err() == nil {
				c.logger.Errorw("cron job failed", "job", job.Name(), "error", err)
			}
		}
	}
}

// Stop cancels every job and waits for the in-flight runs, bounded by the
// configured grace period. Call it after the HTTP server has stopped accepting
// requests but before the pools close — jobs hold connections from them.
func (c *Cron) Stop() {
	if c.cancel == nil {
		return
	}
	c.cancel()

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		c.logger.Info("cron stopped")
	case <-time.After(c.cfg.ShutdownTimeout.Std()):
		c.logger.Warn("cron jobs did not stop within the grace period")
	}
}
