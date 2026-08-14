// Package webhook_dispatcher drains the outbox to the configured receiver.
//
// Events are written in the same transaction as the change they describe, so
// this package's only job is getting them out — and getting them out eventually
// rather than immediately. A receiver that is down delays events; it does not
// lose them.
package webhook_dispatcher

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"go.uber.org/zap"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/pkg/webhook"
)

type Postgres interface {
	ClaimDueWebhookEvents(ctx context.Context, lease time.Duration, maxAttempts, batchSize int32) ([]postgresadapter.WebhookEvent, error)
	MarkWebhookDelivered(ctx context.Context, id int64) error
	MarkWebhookFailed(ctx context.Context, id int64, backoff time.Duration, lastError string) error
	PruneWebhookEvents(ctx context.Context, retention time.Duration, maxAttempts int32) (int64, error)
}

type Sender interface {
	Enabled() bool
	Send(ctx context.Context, eventID, event string, attempt int, payload []byte) error
}

type Usecase struct {
	postgres Postgres
	sender   Sender
	cfg      Config
	logger   *zap.SugaredLogger
}

func New(pg *postgresadapter.Adapter, sender *webhook.Sender, cfg Config, logger *zap.SugaredLogger) *Usecase {
	return &Usecase{postgres: pg, sender: sender, cfg: cfg, logger: logger}
}

type Input struct{}

type Output struct {
	Delivered int
	Failed    int
	Exhausted int
	Pruned    int64
}

// Execute drains one batch and prunes what is no longer needed.
//
// A receiver that rejected an event is never an error here: that is recorded
// against the row and retried later. An error means the outbox itself could not
// be read or written.
func (uc *Usecase) Execute(ctx context.Context, _ Input) (Output, error) {
	var out Output

	// Pruning runs whether or not a receiver is configured. Tying it to the
	// sender would leave the table with nothing able to clean it the moment
	// webhooks were switched off.
	pruned, err := uc.postgres.PruneWebhookEvents(ctx, uc.cfg.Retention.Std(), uc.cfg.MaxAttempts)
	if err != nil {
		return out, fmt.Errorf("webhook_dispatcher: prune: %w", err)
	}
	out.Pruned = pruned

	if !uc.sender.Enabled() {
		return out, nil
	}

	events, err := uc.postgres.ClaimDueWebhookEvents(ctx,
		uc.cfg.Lease(), uc.cfg.MaxAttempts, uc.cfg.BatchSize)
	if err != nil {
		return out, fmt.Errorf("webhook_dispatcher: claim: %w", err)
	}
	if len(events) == 0 {
		return out, nil
	}

	results := uc.deliver(ctx, events)

	for _, r := range results {
		if r.storeErr != nil {
			return out, r.storeErr
		}
		switch {
		case r.delivered:
			out.Delivered++
		case r.exhausted:
			out.Exhausted++
		default:
			out.Failed++
		}
	}

	if out.Delivered > 0 || out.Failed > 0 || out.Exhausted > 0 {
		uc.logger.Infow("webhook batch", "delivered", out.Delivered,
			"failed", out.Failed, "exhausted", out.Exhausted, "pruned", out.Pruned)
	}
	return out, nil
}

type result struct {
	delivered bool
	exhausted bool
	// storeErr is a failure to record the outcome, which is fatal to the tick —
	// unlike a failure to deliver, which is expected and recorded.
	storeErr error
}

// deliver sends the batch with bounded concurrency.
//
// Bounded rather than sequential so one hung receiver does not hold up the rest
// of the batch, and bounded rather than unlimited so a large backlog does not
// open fifty connections at once.
func (uc *Usecase) deliver(ctx context.Context, events []postgresadapter.WebhookEvent) []result {
	results := make([]result, len(events))
	slots := make(chan struct{}, uc.cfg.Concurrency)

	var wg sync.WaitGroup
	for i, ev := range events {
		wg.Add(1)
		go func(i int, ev postgresadapter.WebhookEvent) {
			defer wg.Done()

			slots <- struct{}{}
			defer func() { <-slots }()

			results[i] = uc.deliverOne(ctx, ev)
		}(i, ev)
	}
	wg.Wait()

	return results
}

func (uc *Usecase) deliverOne(ctx context.Context, ev postgresadapter.WebhookEvent) result {
	// The claim already incremented attempts, so the stored value is this
	// attempt's number, 1-based.
	attempt := int(ev.Attempts)

	sendErr := uc.sender.Send(ctx, ev.EventID.String(), ev.Event, attempt, ev.Payload)
	if sendErr == nil {
		if err := uc.postgres.MarkWebhookDelivered(ctx, ev.ID); err != nil {
			return result{storeErr: fmt.Errorf("webhook_dispatcher: mark delivered: %w", err)}
		}
		return result{delivered: true}
	}

	// No separate "gave up" flag: the claim query filters on attempts <
	// max_attempts, so an exhausted event simply stops being claimed and the
	// pruner removes it after the retention window. It is logged here because
	// otherwise nobody would ever learn that it happened.
	exhausted := int32(attempt) >= uc.cfg.MaxAttempts
	if exhausted {
		uc.logger.Errorw("giving up on a webhook event after the last attempt",
			"event", ev.Event, "event_id", ev.EventID, "attempts", attempt, "error", sendErr)
	} else {
		uc.logger.Warnw("webhook delivery failed, will retry",
			"event", ev.Event, "event_id", ev.EventID, "attempt", attempt, "error", sendErr)
	}

	if err := uc.postgres.MarkWebhookFailed(ctx, ev.ID, uc.backoff(attempt), sendErr.Error()); err != nil {
		return result{storeErr: fmt.Errorf("webhook_dispatcher: mark failed: %w", err)}
	}
	return result{exhausted: exhausted}
}

// backoff grows exponentially from BackoffBase and stops at BackoffMax, so a
// receiver that has been down for an hour is retried every BackoffMax rather
// than at an interval that keeps doubling past any useful value.
func (uc *Usecase) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	// Guarded against overflow: 2^attempt past about 63 wraps, and a negative
	// duration would schedule the retry in the past.
	if attempt > 40 {
		return uc.cfg.BackoffMax.Std()
	}

	d := time.Duration(math.Pow(2, float64(attempt-1))) * uc.cfg.BackoffBase.Std()
	if d <= 0 || d > uc.cfg.BackoffMax.Std() {
		return uc.cfg.BackoffMax.Std()
	}
	return d
}
