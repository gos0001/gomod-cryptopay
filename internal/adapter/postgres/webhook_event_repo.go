package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/gos0001/gomod-cryptopay/internal/adapter/postgres/generated"
)

// maxLastErrorLen truncates what a failing endpoint told us before it is
// stored. The body of a remote error is untrusted input and can be arbitrarily
// long; enough of it to diagnose the problem is all that is useful.
const maxLastErrorLen = 512

// WebhookEvent is one queued notification. It lives here rather than in domain
// because it is delivery bookkeeping, not a payment concept.
type WebhookEvent struct {
	ID            int64
	EventID       uuid.UUID
	InvoiceID     *uuid.UUID
	Event         string
	Payload       []byte
	Attempts      int32
	NextAttemptAt time.Time
	DeliveredAt   *time.Time
	LastError     string
	CreatedAt     time.Time
}

func toWebhookEvent(row generated.CpWebhookEvent) WebhookEvent {
	return WebhookEvent{
		ID:            row.ID,
		EventID:       fromUID(row.EventID),
		InvoiceID:     fromUIDPtr(row.InvoiceID),
		Event:         row.Event,
		Payload:       row.Payload,
		Attempts:      row.Attempts,
		NextAttemptAt: fromTS(row.NextAttemptAt),
		DeliveredAt:   fromTSPtr(row.DeliveredAt),
		LastError:     row.LastError,
		CreatedAt:     fromTS(row.CreatedAt),
	}
}

// EnqueueWebhookEvent writes one outbox row.
//
// Callers must run this inside the same transaction as the status change it
// describes, so there is no state in which the invoice moved but the notice was
// lost. Use WithTx for that.
func (a *Adapter) EnqueueWebhookEvent(ctx context.Context, e WebhookEvent) error {
	return MapError(a.q.EnqueueWebhookEvent(ctx, generated.EnqueueWebhookEventParams{
		EventID:   uid(e.EventID),
		InvoiceID: uidPtr(e.InvoiceID),
		Event:     e.Event,
		Payload:   e.Payload,
	}), nil)
}

// ClaimDueWebhookEvents takes ownership of a batch of pending events.
//
// The claim pushes next_attempt_at forward by lease, so a dispatcher that is
// merely slow does not have its work claimed underneath it by the next tick.
// Combined with FOR UPDATE SKIP LOCKED in the query, that is what lets more
// than one dispatcher run without either double-sending.
func (a *Adapter) ClaimDueWebhookEvents(ctx context.Context, lease time.Duration, maxAttempts, batchSize int32) ([]WebhookEvent, error) {
	rows, err := a.q.ClaimDueWebhookEvents(ctx, generated.ClaimDueWebhookEventsParams{
		Lease:       interval(lease),
		MaxAttempts: maxAttempts,
		BatchSize:   batchSize,
	})
	if err != nil {
		return nil, MapError(err, nil)
	}

	out := make([]WebhookEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, toWebhookEvent(r))
	}
	return out, nil
}

func (a *Adapter) MarkWebhookDelivered(ctx context.Context, id int64) error {
	return MapError(a.q.MarkWebhookDelivered(ctx, id), nil)
}

func (a *Adapter) MarkWebhookFailed(ctx context.Context, id int64, backoff time.Duration, lastError string) error {
	if len(lastError) > maxLastErrorLen {
		lastError = lastError[:maxLastErrorLen]
	}
	return MapError(a.q.MarkWebhookFailed(ctx, generated.MarkWebhookFailedParams{
		ID:        id,
		Backoff:   interval(backoff),
		LastError: lastError,
	}), nil)
}

// PruneWebhookEvents deletes delivered events past the retention window, and
// exhausted ones after the same period — a failed integration stays visible
// long enough to be noticed.
func (a *Adapter) PruneWebhookEvents(ctx context.Context, retention time.Duration, maxAttempts int32) (int64, error) {
	n, err := a.q.PruneWebhookEvents(ctx, generated.PruneWebhookEventsParams{
		Retention:   interval(retention),
		MaxAttempts: maxAttempts,
	})
	if err != nil {
		return 0, MapError(err, nil)
	}
	return n, nil
}

func (a *Adapter) CountPendingWebhookEvents(ctx context.Context, maxAttempts int32) (int64, error) {
	n, err := a.q.CountPendingWebhookEvents(ctx, maxAttempts)
	if err != nil {
		return 0, MapError(err, nil)
	}
	return n, nil
}
