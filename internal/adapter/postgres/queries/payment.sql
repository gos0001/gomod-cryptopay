-- name: InsertPayment :one
-- ON CONFLICT DO NOTHING rather than an upsert: re-seeing a transfer is the
-- normal case on every poll, and the first record of it is the true one.
-- Zero rows returned means "already known", which the caller treats as success.
INSERT INTO cp_payments (network, tx_hash, log_index, asset_id, from_address, to_address,
                         amount, block_number, block_time, invoice_id, confirmations)
VALUES ($1, $2, $3, $4, $5, $6, $7, sqlc.narg('block_number'), $8, sqlc.narg('invoice_id'), $9)
ON CONFLICT (network, tx_hash, log_index) DO NOTHING
RETURNING *;

-- name: GetPaymentByChainRef :one
SELECT * FROM cp_payments WHERE network = $1 AND tx_hash = $2 AND log_index = $3;

-- name: GetPaymentByInvoice :one
-- Live payments only: a payment withdrawn by a reorg is history, not the
-- invoice's current state.
SELECT * FROM cp_payments WHERE invoice_id = $1 AND removed_at IS NULL;

-- name: UpdatePaymentConfirmations :exec
UPDATE cp_payments SET confirmations = @confirmations WHERE id = @id;

-- name: ListPaymentsAwaitingConfirmation :many
-- Payments credited to an invoice that is not settled yet.
--
-- This is the settle pass, and it is the reason a payment seen before it was
-- final still reaches `confirmed`: the discovery feed will not return it again
-- once the cursor has moved past it, so nothing else would ever revisit it.
--
-- Needs no network calls of its own — the finality line the caller already
-- fetched for discovery covers every row here.
--
-- Ordered by block_time rather than block_number because TRON supplies no block
-- number; time is the one position both chains have.
SELECT p.*, i.status AS invoice_status
FROM cp_payments p
         JOIN cp_invoices i ON i.id = p.invoice_id
WHERE p.network = @network
  AND i.status = 'detected'
  AND p.removed_at IS NULL
ORDER BY p.block_time
LIMIT @batch_size;

-- name: MarkPaymentRemoved :one
-- Records that a reorg withdrew the transfer.
--
-- The row stays: during reconciliation it is the only evidence that a payment
-- was seen and then withdrawn, which is otherwise indistinguishable from an
-- invoice that was never paid. Marking it also releases the invoice, because
-- cp_payments_live_invoice_key excludes removed rows.
--
-- Idempotent: re-marking an already-removed payment keeps the first timestamp,
-- since a watcher can see the same removal on more than one tick.
UPDATE cp_payments
SET removed_at = COALESCE(removed_at, NOW())
WHERE network = @network AND tx_hash = @tx_hash AND log_index = @log_index
RETURNING *;

-- name: InsertOrphanTransfer :exec
-- Same conflict rule as payments: the watcher re-reads it every poll.
INSERT INTO cp_orphan_transfers (network, tx_hash, log_index, asset_id, contract_address,
                                 from_address, to_address, amount, block_number, block_time, reason)
VALUES ($1, $2, $3, sqlc.narg('asset_id'), $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (network, tx_hash, log_index) DO NOTHING;

-- name: ListOrphanTransfers :many
SELECT * FROM cp_orphan_transfers
ORDER BY created_at DESC, id DESC
LIMIT @page_size;

-- name: GetChainCursor :one
SELECT * FROM cp_chain_cursors WHERE network = $1;

-- name: UpsertChainCursor :one
-- GREATEST guards against a cursor going backwards, which would make the
-- watcher re-scan ground it has already covered. A deliberate rewind — the
-- reorg depth applied at boot — is done by writing the target value directly
-- with RewindChainCursor below.
INSERT INTO cp_chain_cursors (network, last_block, last_timestamp, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (network) DO UPDATE
SET last_block     = GREATEST(cp_chain_cursors.last_block, EXCLUDED.last_block),
    last_timestamp = GREATEST(cp_chain_cursors.last_timestamp, EXCLUDED.last_timestamp),
    updated_at     = NOW()
RETURNING *;

-- name: RewindChainCursor :one
-- Backs the cursor up by the configured reorg depth at startup, so blocks that
-- were only shallowly confirmed when the service stopped are re-examined.
-- Clamped at zero: rewinding past the genesis block is not a thing.
UPDATE cp_chain_cursors
SET last_block = GREATEST(0, last_block - @depth::bigint),
    updated_at = NOW()
WHERE network = @network
RETURNING *;

-- name: ListLivePaymentsInBlockRange :many
-- Credited, not-yet-settled payments whose transfer sits in a block range.
--
-- This backs reorg detection by absence. A poller never receives a log marked
-- `removed` — eth_getLogs answers with the canonical chain only, so a withdrawn
-- transfer is simply missing from the answer. The watcher therefore re-reads a
-- window of blocks, and anything listed here that does not appear in those logs
-- has been reorganised out.
--
-- Restricted to invoices still in `detected`: a confirmed payment is past the
-- chain's own finality line, and un-crediting settled money on the strength of a
-- missing log would be worse than leaving it alone.
--
-- block_number IS NOT NULL excludes TRON, which supplies none and has no
-- equivalent mechanism.
SELECT p.*
FROM cp_payments p
         JOIN cp_invoices i ON i.id = p.invoice_id
WHERE p.network = @network
  AND p.removed_at IS NULL
  AND p.block_number IS NOT NULL
  AND p.block_number >= @from_block
  AND p.block_number <= @to_block
  AND i.status = 'detected'
ORDER BY p.block_number;
