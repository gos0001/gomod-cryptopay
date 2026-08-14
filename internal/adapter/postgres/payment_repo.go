package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gos0001/gomod-cryptopay/internal/adapter/postgres/generated"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
)

func toDomainPayment(row generated.CpPayment) (domain.Payment, error) {
	amt, err := fromAmount(row.Amount)
	if err != nil {
		return domain.Payment{}, err
	}
	return domain.Payment{
		ID:            row.ID,
		Network:       domain.Network(row.Network),
		TxHash:        row.TxHash,
		LogIndex:      row.LogIndex,
		AssetID:       row.AssetID,
		FromAddress:   row.FromAddress,
		ToAddress:     row.ToAddress,
		Amount:        amt,
		BlockNumber:   fromInt8(row.BlockNumber),
		BlockTime:     fromTS(row.BlockTime),
		InvoiceID:     fromUIDPtr(row.InvoiceID),
		Confirmations: row.Confirmations,
		RemovedAt:     fromTSPtr(row.RemovedAt),
		CreatedAt:     fromTS(row.CreatedAt),
	}, nil
}

// RecordPayment stores a credited transfer.
//
// The second return value reports whether this call is what created the row.
// False means the watcher had already seen this transfer, which is the normal
// case on every poll after the first — not an error, and specifically not a
// second credit.
func (a *Adapter) RecordPayment(ctx context.Context, p domain.Payment) (domain.Payment, bool, error) {
	row, err := a.q.InsertPayment(ctx, generated.InsertPaymentParams{
		Network:     string(p.Network),
		TxHash:      p.TxHash,
		LogIndex:    p.LogIndex,
		AssetID:     p.AssetID,
		FromAddress: p.FromAddress,
		ToAddress:   p.ToAddress,
		Amount:      amount(p.Amount),
		// Nil for TRON: the transfer feed supplies no block number, and zero
		// would read as a valid block.
		BlockNumber:   int8OrNull(p.BlockNumber),
		BlockTime:     ts(p.BlockTime),
		InvoiceID:     uidPtr(p.InvoiceID),
		Confirmations: p.Confirmations,
	})
	if err != nil {
		// ON CONFLICT DO NOTHING returns no row rather than an error, so
		// ErrNoRows here means "already known", not "missing".
		if mapped := MapError(err, domain.ErrPaymentAlreadyRecorded); mapped == domain.ErrPaymentAlreadyRecorded {
			existing, getErr := a.GetPaymentByChainRef(ctx, p.Network, p.TxHash, p.LogIndex)
			if getErr != nil {
				return domain.Payment{}, false, getErr
			}
			return existing, false, nil
		}
		return domain.Payment{}, false, MapError(err, nil)
	}

	out, err := toDomainPayment(row)
	return out, true, err
}

func (a *Adapter) GetPaymentByChainRef(ctx context.Context, network domain.Network, txHash string, logIndex int32) (domain.Payment, error) {
	row, err := a.q.GetPaymentByChainRef(ctx, generated.GetPaymentByChainRefParams{
		Network:  string(network),
		TxHash:   txHash,
		LogIndex: logIndex,
	})
	if err != nil {
		return domain.Payment{}, MapError(err, domain.ErrNotFound)
	}
	return toDomainPayment(row)
}

func (a *Adapter) GetPaymentByInvoice(ctx context.Context, invoiceID uuid.UUID) (domain.Payment, error) {
	row, err := a.q.GetPaymentByInvoice(ctx, uid(invoiceID))
	if err != nil {
		return domain.Payment{}, MapError(err, domain.ErrNotFound)
	}
	return toDomainPayment(row)
}

// MarkPaymentRemoved records that a reorg withdrew the transfer.
//
// Marking rather than deleting keeps the evidence, and it releases the invoice:
// cp_payments_live_invoice_key excludes removed rows, so the invoice can be paid
// again. Idempotent — a watcher can see the same removal on several ticks.
func (a *Adapter) MarkPaymentRemoved(ctx context.Context, network domain.Network, txHash string, logIndex int32) (domain.Payment, error) {
	row, err := a.q.MarkPaymentRemoved(ctx, generated.MarkPaymentRemovedParams{
		Network:  string(network),
		TxHash:   txHash,
		LogIndex: logIndex,
	})
	if err != nil {
		return domain.Payment{}, MapError(err, domain.ErrNotFound)
	}
	return toDomainPayment(row)
}

func (a *Adapter) UpdatePaymentConfirmations(ctx context.Context, id int64, confirmations int32) error {
	return MapError(a.q.UpdatePaymentConfirmations(ctx, generated.UpdatePaymentConfirmationsParams{
		ID:            id,
		Confirmations: confirmations,
	}), nil)
}

// ListPaymentsAwaitingConfirmation returns payments whose invoice is still in
// detected, so the caller can recompute confirmations against the chain head.
func (a *Adapter) ListPaymentsAwaitingConfirmation(ctx context.Context, network domain.Network, batchSize int32) ([]domain.Payment, error) {
	rows, err := a.q.ListPaymentsAwaitingConfirmation(ctx, generated.ListPaymentsAwaitingConfirmationParams{
		Network:   string(network),
		BatchSize: batchSize,
	})
	if err != nil {
		return nil, MapError(err, nil)
	}

	out := make([]domain.Payment, 0, len(rows))
	for _, r := range rows {
		p, err := toDomainPayment(generated.CpPayment{
			ID:            r.ID,
			Network:       r.Network,
			TxHash:        r.TxHash,
			LogIndex:      r.LogIndex,
			AssetID:       r.AssetID,
			FromAddress:   r.FromAddress,
			ToAddress:     r.ToAddress,
			Amount:        r.Amount,
			BlockNumber:   r.BlockNumber,
			BlockTime:     r.BlockTime,
			InvoiceID:     r.InvoiceID,
			Confirmations: r.Confirmations,
			RemovedAt:     r.RemovedAt,
			CreatedAt:     r.CreatedAt,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ListLivePaymentsInBlockRange returns credited, not-yet-settled payments whose
// transfer sits in a block range.
//
// Backs reorg detection by absence: a poller never receives a log marked
// `removed`, so the watcher re-reads a window of blocks and treats anything
// listed here but missing from those logs as reorganised out.
func (a *Adapter) ListLivePaymentsInBlockRange(ctx context.Context, network domain.Network, fromBlock, toBlock int64) ([]domain.Payment, error) {
	rows, err := a.q.ListLivePaymentsInBlockRange(ctx, generated.ListLivePaymentsInBlockRangeParams{
		Network:   string(network),
		FromBlock: pgtype.Int8{Int64: fromBlock, Valid: true},
		ToBlock:   pgtype.Int8{Int64: toBlock, Valid: true},
	})
	if err != nil {
		return nil, MapError(err, nil)
	}

	out := make([]domain.Payment, 0, len(rows))
	for _, r := range rows {
		p, err := toDomainPayment(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// RecordOrphanTransfer files money that arrived and could not be attributed.
func (a *Adapter) RecordOrphanTransfer(ctx context.Context, o domain.OrphanTransfer) error {
	return MapError(a.q.InsertOrphanTransfer(ctx, generated.InsertOrphanTransferParams{
		Network:         string(o.Network),
		TxHash:          o.TxHash,
		LogIndex:        o.LogIndex,
		AssetID:         int8OrNull(o.AssetID),
		ContractAddress: o.ContractAddress,
		FromAddress:     o.FromAddress,
		ToAddress:       o.ToAddress,
		Amount:          amount(o.Amount),
		BlockNumber:     o.BlockNumber,
		BlockTime:       ts(o.BlockTime),
		Reason:          string(o.Reason),
	}), nil)
}

func (a *Adapter) ListOrphanTransfers(ctx context.Context, pageSize int32) ([]domain.OrphanTransfer, error) {
	rows, err := a.q.ListOrphanTransfers(ctx, pageSize)
	if err != nil {
		return nil, MapError(err, nil)
	}

	out := make([]domain.OrphanTransfer, 0, len(rows))
	for _, r := range rows {
		amt, err := fromAmount(r.Amount)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.OrphanTransfer{
			ID:              r.ID,
			Network:         domain.Network(r.Network),
			TxHash:          r.TxHash,
			LogIndex:        r.LogIndex,
			AssetID:         fromInt8(r.AssetID),
			ContractAddress: r.ContractAddress,
			FromAddress:     r.FromAddress,
			ToAddress:       r.ToAddress,
			Amount:          amt,
			BlockNumber:     r.BlockNumber,
			BlockTime:       fromTS(r.BlockTime),
			Reason:          domain.OrphanReason(r.Reason),
			CreatedAt:       fromTS(r.CreatedAt),
		})
	}
	return out, nil
}

func toDomainCursor(row generated.CpChainCursor) domain.ChainCursor {
	return domain.ChainCursor{
		Network:       domain.Network(row.Network),
		LastBlock:     row.LastBlock,
		LastTimestamp: fromTS(row.LastTimestamp),
		UpdatedAt:     fromTS(row.UpdatedAt),
	}
}

// GetChainCursor returns a zero-valued cursor rather than an error when the
// watcher has never run: "start from the beginning" is a legitimate position,
// not a missing row the caller should have to special-case.
func (a *Adapter) GetChainCursor(ctx context.Context, network domain.Network) (domain.ChainCursor, error) {
	row, err := a.q.GetChainCursor(ctx, string(network))
	if err != nil {
		if mapped := MapError(err, domain.ErrNotFound); mapped == domain.ErrNotFound {
			return domain.ChainCursor{Network: network}, nil
		}
		return domain.ChainCursor{}, MapError(err, nil)
	}
	return toDomainCursor(row), nil
}

// SaveChainCursor advances the scan position. The query clamps it forward only,
// so a stale write cannot make the watcher re-scan ground it has covered.
func (a *Adapter) SaveChainCursor(ctx context.Context, network domain.Network, lastBlock int64, lastTimestamp time.Time) (domain.ChainCursor, error) {
	row, err := a.q.UpsertChainCursor(ctx, generated.UpsertChainCursorParams{
		Network:       string(network),
		LastBlock:     lastBlock,
		LastTimestamp: ts(lastTimestamp),
	})
	if err != nil {
		return domain.ChainCursor{}, MapError(err, nil)
	}
	return toDomainCursor(row), nil
}

// RewindChainCursor backs the position up by depth blocks at startup, so blocks
// that were only shallowly confirmed when the service stopped are re-examined.
func (a *Adapter) RewindChainCursor(ctx context.Context, network domain.Network, depth int64) (domain.ChainCursor, error) {
	row, err := a.q.RewindChainCursor(ctx, generated.RewindChainCursorParams{
		Network: string(network),
		Depth:   depth,
	})
	if err != nil {
		if mapped := MapError(err, domain.ErrNotFound); mapped == domain.ErrNotFound {
			// Nothing to rewind; the watcher has never run on this network.
			return domain.ChainCursor{Network: network}, nil
		}
		return domain.ChainCursor{}, MapError(err, nil)
	}
	return toDomainCursor(row), nil
}
