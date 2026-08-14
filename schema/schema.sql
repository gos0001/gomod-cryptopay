-- gomod-cryptopay schema.
--
-- Every statement here must be idempotent: this file is executed in full on
-- every start, against databases that may already hold data.
--
-- That means `CREATE TABLE IF NOT EXISTS` for a new table, and — critically —
-- `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` for a new column on an existing
-- one. Editing the CREATE TABLE body instead is silently a no-op: the guard
-- skips the whole statement once the table exists, so the column never appears
-- on any deployment that is not brand new. The same applies to the CHECK
-- constraints below — widening one later needs an explicit DO block, not an
-- edit to the CREATE TABLE.
--
-- Tables are prefixed `cp_` so they stay identifiable if this service ever
-- shares a database with something else.
--
-- sqlc validates the queries in internal/adapter/postgres/queries against this
-- same file, so the schema the service creates and the schema the generated
-- code assumes cannot drift apart.
--
-- Money is NUMERIC(78,0), always in the token's smallest units. 78 digits is
-- above uint256, so no on-chain value can overflow it, and a scale of 0 means
-- there is no decimal point in the database at all — rendering one is the API
-- layer's job, from the asset's own `decimals`.
--
-- Addresses are stored exactly as the application normalises them: lowercase
-- hex for EVM chains, base58 verbatim for TRON. Normalisation is deliberately
-- not done in SQL, because `lower()` is correct for one of those and destroys
-- the other.


-- ---------------------------------------------------------------------------
-- Assets: which token on which chain. Configuration, upserted at boot.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cp_assets (
    id               BIGSERIAL PRIMARY KEY,
    network          TEXT          NOT NULL CHECK (network IN ('tron', 'bsc')),
    symbol           TEXT          NOT NULL,
    contract_address TEXT          NOT NULL,
    decimals         SMALLINT      NOT NULL CHECK (decimals >= 0 AND decimals <= 36),

    -- The gap between two invoices asked for the same amount, in smallest
    -- units. Also the width of the credit window: a transfer settles an invoice
    -- when it lands in [pay_amount, pay_amount + step).
    step             NUMERIC(78,0) NOT NULL CHECK (step > 0),
    -- How many invoices may be outstanding for one base amount.
    nonce_max        INTEGER       NOT NULL CHECK (nonce_max > 0),

    enabled          BOOLEAN       NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

-- The real key. Symbol is a label — two chains can both carry a "USDT", and an
-- operator may legitimately configure two contracts under one symbol.
CREATE UNIQUE INDEX IF NOT EXISTS cp_assets_network_contract_key
    ON cp_assets (network, contract_address);


-- ---------------------------------------------------------------------------
-- Invoices.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cp_invoices (
    id                UUID          PRIMARY KEY,
    -- The merchant's own key, and the idempotency key for creation. Nullable:
    -- most callers do not need one.
    external_id       TEXT,

    asset_id          BIGINT        NOT NULL REFERENCES cp_assets (id),
    network           TEXT          NOT NULL CHECK (network IN ('tron', 'bsc')),
    -- Copied from configuration rather than joined at read time, so rotating
    -- the receiving address does not rewrite the history of where callers were
    -- told to pay.
    pay_address       TEXT          NOT NULL,

    -- What the merchant asked for, and what the payer must actually send.
    base_amount       NUMERIC(78,0) NOT NULL CHECK (base_amount > 0),
    pay_amount        NUMERIC(78,0) NOT NULL CHECK (pay_amount > 0),
    nonce             INTEGER       NOT NULL CHECK (nonce >= 0),

    status            TEXT          NOT NULL CHECK (
                          status IN ('pending', 'detected', 'confirmed', 'expired', 'cancelled')),
    confirmations     INTEGER       NOT NULL DEFAULT 0,

    description       TEXT          NOT NULL DEFAULT '',
    -- Opaque merchant JSON: stored, echoed back, never interpreted here.
    metadata          JSONB,

    created_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    expires_at        TIMESTAMPTZ   NOT NULL,

    -- amount_held is what the uniqueness index below is built on, and
    -- amount_hold_until is when a sweep may clear it.
    --
    -- Two columns rather than one because a partial index predicate must be
    -- IMMUTABLE, and `WHERE amount_hold_until > now()` is not — now() is merely
    -- STABLE, so Postgres refuses to build that index at all. The boolean is
    -- the immutable stand-in; the invoice_expirer job clears it once the
    -- timestamp has passed.
    --
    -- The hold deliberately outlives the invoice's terminal status. A transfer
    -- sent just before expiry can arrive well after it, and if the amount had
    -- already been handed to a new invoice, that late transfer would pay the
    -- wrong one. If the expirer stops running the amounts simply stay reserved,
    -- which is the safe direction to fail in.
    amount_held       BOOLEAN       NOT NULL DEFAULT TRUE,
    amount_hold_until TIMESTAMPTZ   NOT NULL,

    paid_at           TIMESTAMPTZ
);

-- The core of the matching scheme: at most one live invoice may claim a given
-- amount of a given asset. Allocation races resolve here, as a 23505 the caller
-- retries with the next nonce.
CREATE UNIQUE INDEX IF NOT EXISTS cp_invoices_asset_amount_held_key
    ON cp_invoices (asset_id, pay_amount) WHERE amount_held;

CREATE UNIQUE INDEX IF NOT EXISTS cp_invoices_external_id_key
    ON cp_invoices (external_id) WHERE external_id IS NOT NULL;

-- Drives the expirer: find pending invoices past their deadline.
CREATE INDEX IF NOT EXISTS cp_invoices_pending_expiry_idx
    ON cp_invoices (expires_at) WHERE status = 'pending';

-- Drives the hold sweep.
CREATE INDEX IF NOT EXISTS cp_invoices_hold_release_idx
    ON cp_invoices (amount_hold_until) WHERE amount_held;

-- Drives the list endpoint's default ordering, and its status filter.
CREATE INDEX IF NOT EXISTS cp_invoices_created_at_idx
    ON cp_invoices (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS cp_invoices_status_created_at_idx
    ON cp_invoices (status, created_at DESC, id DESC);


-- ---------------------------------------------------------------------------
-- Payments: transfers that were credited to an invoice.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cp_payments (
    id            BIGSERIAL     PRIMARY KEY,

    network       TEXT          NOT NULL CHECK (network IN ('tron', 'bsc')),
    tx_hash       TEXT          NOT NULL,
    -- Which transfer within the transaction. EVM logs carry a real index; the
    -- TRON API exposes one transfer per record, so it is 0 there.
    log_index     INTEGER       NOT NULL DEFAULT 0,

    asset_id      BIGINT        NOT NULL REFERENCES cp_assets (id),
    from_address  TEXT          NOT NULL,
    to_address    TEXT          NOT NULL,
    amount        NUMERIC(78,0) NOT NULL CHECK (amount > 0),

    -- Nullable because TRON does not supply one: its transfer feed carries only
    -- a block timestamp. Zero would have been a lie that looks like a valid
    -- block, so absence is recorded as absence. For TRON, block_time is the
    -- authoritative position; for EVM chains, block_number is.
    block_number  BIGINT,
    block_time    TIMESTAMPTZ   NOT NULL,

    invoice_id    UUID          REFERENCES cp_invoices (id),
    confirmations INTEGER       NOT NULL DEFAULT 0,

    -- Set when a reorg un-mines the transfer. The row is kept rather than
    -- deleted: during reconciliation it is the only evidence that a payment was
    -- seen and then withdrawn, which is otherwise indistinguishable from an
    -- invoice that was never paid.
    removed_at    TIMESTAMPTZ,

    created_at    TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

-- Idempotent follow-ups for databases created before these columns existed.
-- Editing the CREATE TABLE body above is not enough — the IF NOT EXISTS guard
-- skips the whole statement once the table is there.
ALTER TABLE cp_payments ADD COLUMN IF NOT EXISTS removed_at TIMESTAMPTZ;
ALTER TABLE cp_payments ALTER COLUMN block_number DROP NOT NULL;

-- A watcher re-reads the same transfer on every poll. This index is what makes
-- that harmless instead of a second credit.
CREATE UNIQUE INDEX IF NOT EXISTS cp_payments_chain_key
    ON cp_payments (network, tx_hash, log_index);

-- And this one is what stops two different transfers settling one invoice.
--
-- Removed payments are excluded from it. Without that exclusion a transfer
-- un-mined by a reorg would hold its invoice forever and the invoice could never
-- be paid again — the exact situation the reorg path exists to recover from.
--
-- The predicate changed after the first release, and a partial index cannot be
-- redefined in place: CREATE UNIQUE INDEX IF NOT EXISTS would find the old name
-- and skip, leaving the old predicate. Hence the drop of the previous name and a
-- new one alongside it.
DROP INDEX IF EXISTS cp_payments_invoice_key;
CREATE UNIQUE INDEX IF NOT EXISTS cp_payments_live_invoice_key
    ON cp_payments (invoice_id) WHERE invoice_id IS NOT NULL AND removed_at IS NULL;

-- Drives the confirmation sweep: credited payments that are not settled yet.
CREATE INDEX IF NOT EXISTS cp_payments_unsettled_idx
    ON cp_payments (network, block_time) WHERE invoice_id IS NOT NULL AND removed_at IS NULL;


-- ---------------------------------------------------------------------------
-- Orphan transfers: money that arrived and could not be attributed.
--
-- Recorded rather than dropped. With amount-based matching a payer who rounds
-- their input produces a transfer nothing will ever claim, and the operator
-- needs to see it to refund or credit it by hand.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cp_orphan_transfers (
    id               BIGSERIAL     PRIMARY KEY,

    network          TEXT          NOT NULL CHECK (network IN ('tron', 'bsc')),
    tx_hash          TEXT          NOT NULL,
    log_index        INTEGER       NOT NULL DEFAULT 0,

    -- NULL when the token itself is unrecognised. contract_address is always
    -- populated, so an unknown token is still identifiable.
    asset_id         BIGINT        REFERENCES cp_assets (id),
    contract_address TEXT          NOT NULL,
    from_address     TEXT          NOT NULL,
    to_address       TEXT          NOT NULL,
    amount           NUMERIC(78,0) NOT NULL,

    block_number     BIGINT        NOT NULL,
    block_time       TIMESTAMPTZ   NOT NULL,

    reason           TEXT          NOT NULL CHECK (
                         reason IN ('no_matching_invoice', 'invoice_terminal', 'unknown_asset')),
    created_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS cp_orphan_transfers_chain_key
    ON cp_orphan_transfers (network, tx_hash, log_index);

CREATE INDEX IF NOT EXISTS cp_orphan_transfers_created_at_idx
    ON cp_orphan_transfers (created_at DESC);


-- ---------------------------------------------------------------------------
-- Webhook outbox.
--
-- Events are written in the same transaction as the status change they
-- describe, so there is no state in which the invoice moved but the notice was
-- lost. A separate job drains them.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cp_webhook_events (
    id              BIGSERIAL   PRIMARY KEY,
    -- Sent as X-Cryptopay-Event-Id and stable across retries, so a receiver can
    -- deduplicate. Delivery is at-least-once; this is what makes that workable.
    event_id        UUID        NOT NULL,

    invoice_id      UUID        REFERENCES cp_invoices (id),
    event           TEXT        NOT NULL,
    payload         JSONB       NOT NULL,

    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at    TIMESTAMPTZ,
    -- Truncated by the dispatcher before it is stored: a remote endpoint's
    -- error body is untrusted input and can be arbitrarily long.
    last_error      TEXT        NOT NULL DEFAULT '',

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS cp_webhook_events_event_id_key
    ON cp_webhook_events (event_id);

-- Idempotent follow-ups for databases created before the destination moved into
-- configuration.
--
-- The URL is no longer accepted per invoice: this is a self-hosted module with
-- one operator and one receiver, so taking a destination from an API caller
-- would have the service posting wherever a caller pointed it and would expose
-- the receiver's domain to whoever holds a key.
--
-- It is not stored per event either. Keeping it would bake a typo in the
-- configuration into every queued row, so retries would keep hitting the wrong
-- address after the operator fixed it. Reading the current setting at delivery
-- time is what makes the fix take effect.
ALTER TABLE cp_invoices       DROP COLUMN IF EXISTS webhook_url;
ALTER TABLE cp_webhook_events DROP COLUMN IF EXISTS url;

-- The dispatcher's claim query: undelivered and due, oldest first.
CREATE INDEX IF NOT EXISTS cp_webhook_events_due_idx
    ON cp_webhook_events (next_attempt_at) WHERE delivered_at IS NULL;

-- Drives retention pruning of delivered rows.
CREATE INDEX IF NOT EXISTS cp_webhook_events_delivered_idx
    ON cp_webhook_events (delivered_at) WHERE delivered_at IS NOT NULL;


-- ---------------------------------------------------------------------------
-- Chain cursors: how far each watcher has scanned.
--
-- Two positions because the chains are enumerated differently — BSC by block
-- range, TRON by transfer timestamp. Each watcher uses its own column.
--
-- Persisting this is what makes downtime survivable: on restart the watcher
-- resumes where it stopped rather than at the current head, so transfers that
-- arrived while the service was down are still picked up.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cp_chain_cursors (
    network        TEXT        PRIMARY KEY CHECK (network IN ('tron', 'bsc')),
    last_block     BIGINT      NOT NULL DEFAULT 0,
    last_timestamp TIMESTAMPTZ NOT NULL DEFAULT to_timestamp(0),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
