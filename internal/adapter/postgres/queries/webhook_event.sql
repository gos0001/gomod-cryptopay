-- name: EnqueueWebhookEvent :exec
-- Written inside the same transaction as the status change it describes, so
-- there is no window in which the invoice moved but the notice was lost.
INSERT INTO cp_webhook_events (event_id, invoice_id, event, payload)
VALUES ($1, sqlc.narg('invoice_id'), $2, $3)
ON CONFLICT (event_id) DO NOTHING;

-- name: ClaimDueWebhookEvents :many
-- FOR UPDATE SKIP LOCKED is what lets more than one dispatcher run without
-- either of them sending the same event twice, and without the second one
-- blocking behind the first.
--
-- next_attempt_at is pushed forward as part of the claim, so a dispatcher that
-- dies mid-delivery releases the row on rollback but a dispatcher that merely
-- takes a long time does not have the same event claimed underneath it by the
-- next tick.
UPDATE cp_webhook_events
SET attempts        = attempts + 1,
    next_attempt_at = NOW() + @lease::interval
WHERE id IN (SELECT id
             FROM cp_webhook_events
             WHERE delivered_at IS NULL
               AND next_attempt_at <= NOW()
               AND attempts < @max_attempts::int
             ORDER BY next_attempt_at
             LIMIT @batch_size FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: MarkWebhookDelivered :exec
UPDATE cp_webhook_events
SET delivered_at = NOW(), last_error = ''
WHERE id = $1;

-- name: MarkWebhookFailed :exec
-- last_error is truncated by the caller before it gets here: a remote
-- endpoint's error body is untrusted input and can be arbitrarily long.
UPDATE cp_webhook_events
SET next_attempt_at = NOW() + @backoff::interval,
    last_error      = @last_error
WHERE id = @id;

-- name: PruneWebhookEvents :execrows
-- Delivered events past the retention window, and exhausted ones kept a while
-- longer so a failed integration is still visible after the fact.
DELETE FROM cp_webhook_events
WHERE (delivered_at IS NOT NULL AND delivered_at < NOW() - @retention::interval)
   OR (delivered_at IS NULL AND attempts >= @max_attempts::int
       AND created_at < NOW() - @retention::interval);

-- name: CountPendingWebhookEvents :one
SELECT COUNT(*) FROM cp_webhook_events
WHERE delivered_at IS NULL AND attempts < @max_attempts::int;
