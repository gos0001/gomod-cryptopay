-- name: ListHeldPayAmountsInRange :many
-- Every amount currently reserved in this asset's candidate window, so the
-- repository can pick the lowest free nonce.
--
-- The window is [base_amount, base_amount + nonce_max*step), computed by the
-- caller: sqlc cannot generate code for arithmetic between two named
-- parameters, and passing the bounds in already multiplied costs nothing.
--
-- Deliberately not a `generate_series` anti-join returning the free nonce
-- directly, which is the shape this obviously wants. sqlc cannot parse
-- generate_series as a table source at all, so the enumeration lives in Go —
-- where it is also far easier to test. At most nonce_max rows come back, and
-- nonce_max is in the hundreds.
--
-- This is not a lock and does not need to be. Two concurrent callers can settle
-- on the same nonce; one then loses on cp_invoices_asset_amount_held_key with a
-- 23505 and retries. The index, not this query, is what guarantees uniqueness.
SELECT pay_amount
FROM cp_invoices
WHERE asset_id = @asset_id
  AND amount_held
  AND pay_amount >= @amount_from
  AND pay_amount < @amount_to
ORDER BY pay_amount;

-- name: CreateInvoice :one
-- Fails with 23505 on cp_invoices_asset_amount_held_key when another caller
-- took this amount first, and on cp_invoices_external_id_key when the external
-- ID is already used. The repository tells them apart by constraint name.
INSERT INTO cp_invoices (id, external_id, asset_id, network, pay_address,
                         base_amount, pay_amount, nonce, status,
                         description, metadata,
                         expires_at, amount_hold_until)
VALUES (@id, sqlc.narg('external_id'), @asset_id, @network, @pay_address,
        @base_amount, @pay_amount, @nonce, 'pending',
        @description, sqlc.narg('metadata'), @expires_at, @amount_hold_until)
RETURNING *;

-- name: GetInvoiceByID :one
SELECT * FROM cp_invoices WHERE id = $1;

-- name: GetInvoiceByExternalID :one
SELECT * FROM cp_invoices WHERE external_id = $1;

-- name: ListInvoices :many
-- Keyset pagination on (created_at, id): a stable order under concurrent
-- inserts, which OFFSET is not. The id breaks ties between invoices created in
-- the same microsecond.
SELECT *
FROM cp_invoices
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('asset_id')::bigint IS NULL OR asset_id = sqlc.narg('asset_id')::bigint)
  AND (sqlc.narg('network')::text IS NULL OR network = sqlc.narg('network')::text)
  AND (sqlc.narg('external_id')::text IS NULL OR external_id = sqlc.narg('external_id')::text)
  AND (sqlc.narg('created_from')::timestamptz IS NULL OR created_at >= sqlc.narg('created_from')::timestamptz)
  AND (sqlc.narg('created_to')::timestamptz IS NULL OR created_at <= sqlc.narg('created_to')::timestamptz)
  AND (sqlc.narg('cursor_created_at')::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg('cursor_created_at')::timestamptz, sqlc.narg('cursor_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_size;

-- name: UpdateInvoiceStatus :one
-- The expected-status guard makes this a compare-and-set: a watcher and the
-- expirer can reach the same invoice at once, and the loser must not overwrite
-- the winner. Zero rows means someone else moved it first.
UPDATE cp_invoices
SET status            = @next_status,
    confirmations     = @confirmations,
    paid_at           = COALESCE(sqlc.narg('paid_at')::timestamptz, paid_at),
    amount_hold_until = GREATEST(amount_hold_until, @amount_hold_until::timestamptz),
    updated_at        = NOW()
WHERE id = @id
  AND status = @expected_status
RETURNING *;

-- name: UpdateInvoiceConfirmations :one
-- Progress within the detected state, which needs no status guard.
UPDATE cp_invoices
SET confirmations = @confirmations, updated_at = NOW()
WHERE id = @id AND status = 'detected'
RETURNING *;

-- name: CancelInvoice :one
-- Only a pending invoice may be withdrawn. Once a transfer has been seen the
-- money is real, and cancelling would strand it.
UPDATE cp_invoices
SET status            = 'cancelled',
    amount_hold_until = @amount_hold_until,
    updated_at        = NOW()
WHERE id = @id AND status = 'pending'
RETURNING *;

-- name: FindHeldInvoiceForAmount :one
-- The matching lookup: which invoice, if any, claims this transfer.
--
-- The window is half-open — [pay_amount, pay_amount + step) — because
-- pay_amount + step is by construction some other invoice's amount, and
-- crediting it here would take that invoice's money.
--
-- Terminal invoices are included on purpose. A late transfer against an expired
-- invoice must be recognised as belonging to it and filed as an orphan with the
-- right reason, rather than silently matching nothing.
SELECT i.*
FROM cp_invoices i
         JOIN cp_assets a ON a.id = i.asset_id
WHERE i.asset_id = @asset_id
  AND i.amount_held
  AND @amount::numeric >= i.pay_amount
  AND @amount::numeric < i.pay_amount + a.step
ORDER BY i.created_at DESC
LIMIT 1;

-- name: ExpirePendingInvoices :many
-- Only pending invoices expire. An invoice in detected has money on chain, and
-- the clock must not void it.
UPDATE cp_invoices
SET status            = 'expired',
    amount_hold_until = GREATEST(amount_hold_until, NOW() + @hold::interval),
    updated_at        = NOW()
WHERE id IN (SELECT id
             FROM cp_invoices
             WHERE status = 'pending' AND expires_at < NOW()
             ORDER BY expires_at
             LIMIT @batch_size)
RETURNING *;

-- name: ReleaseExpiredAmountHolds :execrows
-- Frees payment amounts for reuse once their hold has run out.
--
-- This exists because a partial index predicate must be IMMUTABLE and
-- `WHERE amount_hold_until > now()` is not, so the boolean has to be maintained
-- rather than derived. If this stops running, amounts simply stay reserved —
-- the safe direction to fail in.
UPDATE cp_invoices
SET amount_held = FALSE, updated_at = NOW()
WHERE amount_held
  AND amount_hold_until <= NOW()
  AND status IN ('confirmed', 'expired', 'cancelled');

-- name: CountInvoicesByStatus :many
SELECT status, COUNT(*) AS total FROM cp_invoices GROUP BY status;
