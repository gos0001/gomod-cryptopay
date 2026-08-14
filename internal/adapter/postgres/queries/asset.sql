-- name: UpsertAsset :one
-- Called once per configured asset at boot. The conflict target is the real
-- key, so editing a token's decimals or step in configuration updates the row
-- rather than failing or duplicating it.
INSERT INTO cp_assets (network, symbol, contract_address, decimals, step, nonce_max, enabled)
VALUES ($1, $2, $3, $4, $5, $6, TRUE)
ON CONFLICT (network, contract_address) DO UPDATE
SET symbol     = EXCLUDED.symbol,
    decimals   = EXCLUDED.decimals,
    step       = EXCLUDED.step,
    nonce_max  = EXCLUDED.nonce_max,
    enabled    = TRUE,
    updated_at = NOW()
RETURNING *;

-- name: DisableAssetsNotIn :execrows
-- Removing an asset from configuration disables it rather than deleting it:
-- invoices reference it, and their history must stay readable.
UPDATE cp_assets
SET enabled = FALSE, updated_at = NOW()
WHERE enabled AND NOT (id = ANY(@keep_ids::bigint[]));

-- name: GetAssetByID :one
SELECT * FROM cp_assets WHERE id = $1;

-- name: ListAssetsByNetworkAndSymbol :many
-- Deliberately :many, not :one with a LIMIT.
--
-- Symbol is a label, not a key: an operator can legitimately configure two
-- contracts under one symbol on one chain. Silently taking the lowest id would
-- issue an invoice denominated in a token the caller did not ask for, and
-- nothing downstream could tell. The caller resolves the ambiguity instead, by
-- naming a contract address.
SELECT * FROM cp_assets
WHERE network = $1 AND symbol = $2 AND enabled
ORDER BY id;

-- name: GetEnabledAssetByContract :one
-- The unambiguous path. Disabled assets are excluded so that removing a token
-- from the configuration stops new invoices for it by both routes, not just by
-- symbol.
SELECT * FROM cp_assets
WHERE network = $1 AND contract_address = $2 AND enabled;

-- name: GetAssetByContract :one
-- Includes disabled assets: the watcher has to recognise a transfer of a token
-- that was switched off after the invoice was issued.
SELECT * FROM cp_assets WHERE network = $1 AND contract_address = $2;

-- name: ListAssets :many
SELECT * FROM cp_assets
WHERE enabled OR NOT @enabled_only::boolean
ORDER BY network, symbol, id;

-- name: ListEnabledAssetsByNetwork :many
-- What a watcher asks for on every poll: the contracts it must filter on.
SELECT * FROM cp_assets
WHERE network = $1 AND enabled
ORDER BY id;
