package postgres

import (
	"context"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gos0001/gomod-cryptopay/internal/adapter/postgres/generated"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
)

// allocationAttempts bounds the retry loop around amount allocation.
//
// Each attempt costs one read and one failed insert, and only a genuine
// collision with a concurrent creation causes another pass. Three is generous:
// losing three races in a row means either extreme contention on one base
// amount, or a nonce space so nearly full that failing is the honest answer.
const allocationAttempts = 3

func toDomainInvoice(row generated.CpInvoice) (domain.Invoice, error) {
	base, err := fromAmount(row.BaseAmount)
	if err != nil {
		return domain.Invoice{}, err
	}
	pay, err := fromAmount(row.PayAmount)
	if err != nil {
		return domain.Invoice{}, err
	}

	return domain.Invoice{
		ID:              fromUID(row.ID),
		ExternalID:      fromText(row.ExternalID),
		AssetID:         row.AssetID,
		Network:         domain.Network(row.Network),
		PayAddress:      row.PayAddress,
		BaseAmount:      base,
		PayAmount:       pay,
		Nonce:           row.Nonce,
		Status:          domain.InvoiceStatus(row.Status),
		Confirmations:   row.Confirmations,
		Description:     row.Description,
		Metadata:        row.Metadata,
		CreatedAt:       fromTS(row.CreatedAt),
		UpdatedAt:       fromTS(row.UpdatedAt),
		ExpiresAt:       fromTS(row.ExpiresAt),
		AmountHoldUntil: fromTS(row.AmountHoldUntil),
		PaidAt:          fromTSPtr(row.PaidAt),
	}, nil
}

func toDomainInvoices(rows []generated.CpInvoice) ([]domain.Invoice, error) {
	out := make([]domain.Invoice, 0, len(rows))
	for _, row := range rows {
		inv, err := toDomainInvoice(row)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, nil
}

// lowestFreeNonce returns the smallest nonce in [0, nonceMax) whose payment
// amount is not among held, along with that amount.
//
// held must be ascending, which is what the query guarantees. The walk relies
// on that: it advances a candidate through the sorted held nonces and stops at
// the first gap, so it is one pass with no set allocation.
//
// Amounts that are not an exact multiple of step above base are skipped rather
// than rejected. They exist whenever an operator has changed an asset's step
// while invoices were outstanding, and the old ones are still genuinely held —
// but they occupy no nonce in the new grid, so they cannot block one.
func lowestFreeNonce(base, step *big.Int, nonceMax int32, held []string) (int32, *big.Int, bool) {
	if base == nil || step == nil || step.Sign() <= 0 || nonceMax <= 0 {
		return 0, nil, false
	}

	var candidate int32
	diff := new(big.Int)
	rem := new(big.Int)

	for _, h := range held {
		amt, ok := new(big.Int).SetString(h, 10)
		if !ok {
			continue
		}

		diff.Sub(amt, base)
		if diff.Sign() < 0 {
			continue
		}
		diff.QuoRem(diff, step, rem)
		if rem.Sign() != 0 || !diff.IsInt64() {
			continue // off-grid: a leftover from a previous step setting
		}

		n := diff.Int64()
		if n < int64(candidate) {
			continue // duplicate or out of order; the walk has passed it
		}
		if n > int64(candidate) {
			break // the gap at candidate is the answer
		}
		candidate++
		if candidate >= nonceMax {
			return 0, nil, false
		}
	}

	if candidate >= nonceMax {
		return 0, nil, false
	}

	offset := new(big.Int).Mul(step, big.NewInt(int64(candidate)))
	return candidate, offset.Add(offset, base), true
}

// CreateInvoiceInput is what the use case hands the repository. It carries no
// payment amount: choosing one is this layer's job.
type CreateInvoiceInput struct {
	ID              uuid.UUID
	ExternalID      string
	Asset           domain.Asset
	PayAddress      string
	BaseAmount      *big.Int
	Description     string
	Metadata        []byte
	ExpiresAt       time.Time
	AmountHoldUntil time.Time
}

// CreateInvoice allocates a unique payment amount and stores the invoice.
//
// The read that finds a free amount and the insert that claims it are not
// atomic, and deliberately so: serialising every creation to close a window
// that the unique index already covers would cost more than it saves. A
// concurrent creation that takes the amount first surfaces as a 23505 on
// cp_invoices_asset_amount_held_key, and the loop simply picks the next gap.
func (a *Adapter) CreateInvoice(ctx context.Context, in CreateInvoiceInput) (domain.Invoice, error) {
	span := new(big.Int).Mul(in.Asset.Step, big.NewInt(int64(in.Asset.NonceMax)))
	upper := new(big.Int).Add(in.BaseAmount, span)

	var lastErr error
	for attempt := 0; attempt < allocationAttempts; attempt++ {
		held, err := a.q.ListHeldPayAmountsInRange(ctx, generated.ListHeldPayAmountsInRangeParams{
			AssetID:    in.Asset.ID,
			AmountFrom: amount(in.BaseAmount),
			AmountTo:   amount(upper),
		})
		if err != nil {
			return domain.Invoice{}, MapError(err, nil)
		}

		nonce, payAmount, ok := lowestFreeNonce(in.BaseAmount, in.Asset.Step, in.Asset.NonceMax, held)
		if !ok {
			return domain.Invoice{}, domain.ErrAmountSpaceExhausted
		}

		row, err := a.q.CreateInvoice(ctx, generated.CreateInvoiceParams{
			ID:              uid(in.ID),
			ExternalID:      textOrNull(in.ExternalID),
			AssetID:         in.Asset.ID,
			Network:         string(in.Asset.Network),
			PayAddress:      in.PayAddress,
			BaseAmount:      amount(in.BaseAmount),
			PayAmount:       amount(payAmount),
			Nonce:           nonce,
			Description:     in.Description,
			Metadata:        in.Metadata,
			ExpiresAt:       ts(in.ExpiresAt),
			AmountHoldUntil: ts(in.AmountHoldUntil),
		})
		if err == nil {
			return toDomainInvoice(row)
		}

		// A reused external ID is the caller's problem and retrying cannot fix
		// it; a contested amount is nobody's problem and retrying is the fix.
		if uniqueViolationOn(err, idxInvoiceExternalID) {
			return domain.Invoice{}, domain.ErrExternalIDTaken
		}
		if !uniqueViolationOn(err, idxInvoiceAmountHeld) {
			return domain.Invoice{}, MapError(err, nil)
		}
		lastErr = err
	}

	// Every attempt lost a race. Retryable from the caller's side, so it must
	// not be reported as an internal failure.
	_ = lastErr
	return domain.Invoice{}, domain.ErrAmountSpaceExhausted
}

func (a *Adapter) GetInvoiceByID(ctx context.Context, id uuid.UUID) (domain.Invoice, error) {
	row, err := a.q.GetInvoiceByID(ctx, uid(id))
	if err != nil {
		return domain.Invoice{}, MapError(err, domain.ErrInvoiceNotFound)
	}
	return toDomainInvoice(row)
}

func (a *Adapter) GetInvoiceByExternalID(ctx context.Context, externalID string) (domain.Invoice, error) {
	row, err := a.q.GetInvoiceByExternalID(ctx, textOrNull(externalID))
	if err != nil {
		return domain.Invoice{}, MapError(err, domain.ErrInvoiceNotFound)
	}
	return toDomainInvoice(row)
}

// ListInvoicesFilter is the read side of the list endpoint. A zero field means
// "no filter" throughout.
type ListInvoicesFilter struct {
	Status      domain.InvoiceStatus
	AssetID     int64
	Network     domain.Network
	ExternalID  string
	CreatedFrom time.Time
	CreatedTo   time.Time

	// CursorCreatedAt and CursorID are the last row of the previous page.
	// Keyset rather than offset: a stable window under concurrent inserts.
	CursorCreatedAt time.Time
	CursorID        uuid.UUID

	PageSize int32
}

func (a *Adapter) ListInvoices(ctx context.Context, f ListInvoicesFilter) ([]domain.Invoice, error) {
	params := generated.ListInvoicesParams{PageSize: f.PageSize}

	if f.Status != "" {
		params.Status = text(string(f.Status))
	}
	if f.AssetID != 0 {
		params.AssetID = pgtype.Int8{Int64: f.AssetID, Valid: true}
	}
	if f.Network != "" {
		params.Network = text(string(f.Network))
	}
	if f.ExternalID != "" {
		params.ExternalID = text(f.ExternalID)
	}
	if !f.CreatedFrom.IsZero() {
		params.CreatedFrom = ts(f.CreatedFrom)
	}
	if !f.CreatedTo.IsZero() {
		params.CreatedTo = ts(f.CreatedTo)
	}
	// Both halves of the keyset or neither: a cursor timestamp with no id would
	// compare against a NULL uuid and silently return nothing.
	if !f.CursorCreatedAt.IsZero() && f.CursorID != uuid.Nil {
		params.CursorCreatedAt = ts(f.CursorCreatedAt)
		params.CursorID = uid(f.CursorID)
	}

	rows, err := a.q.ListInvoices(ctx, params)
	if err != nil {
		return nil, MapError(err, nil)
	}
	return toDomainInvoices(rows)
}

// TransitionInvoice is a compare-and-set on status.
//
// domain.ErrInvalidTransition on zero rows: a watcher and the expirer can reach
// the same invoice at once, and the loser must be told it lost rather than
// silently succeed.
func (a *Adapter) TransitionInvoice(
	ctx context.Context,
	id uuid.UUID,
	from, to domain.InvoiceStatus,
	confirmations int32,
	paidAt *time.Time,
	holdUntil time.Time,
) (domain.Invoice, error) {
	row, err := a.q.UpdateInvoiceStatus(ctx, generated.UpdateInvoiceStatusParams{
		ID:              uid(id),
		ExpectedStatus:  string(from),
		NextStatus:      string(to),
		Confirmations:   confirmations,
		PaidAt:          tsPtr(paidAt),
		AmountHoldUntil: ts(holdUntil),
	})
	if err != nil {
		return domain.Invoice{}, MapError(err, domain.ErrInvalidTransition)
	}
	return toDomainInvoice(row)
}

// UpdateInvoiceConfirmations records progress within the detected state.
func (a *Adapter) UpdateInvoiceConfirmations(ctx context.Context, id uuid.UUID, confirmations int32) (domain.Invoice, error) {
	row, err := a.q.UpdateInvoiceConfirmations(ctx, generated.UpdateInvoiceConfirmationsParams{
		ID:            uid(id),
		Confirmations: confirmations,
	})
	if err != nil {
		return domain.Invoice{}, MapError(err, domain.ErrInvoiceNotFound)
	}
	return toDomainInvoice(row)
}

// CancelInvoice withdraws a pending invoice.
//
// domain.ErrInvalidTransition on zero rows covers both "already cancelled" and
// "a transfer arrived first"; the use case re-reads to tell the caller which.
func (a *Adapter) CancelInvoice(ctx context.Context, id uuid.UUID, holdUntil time.Time) (domain.Invoice, error) {
	row, err := a.q.CancelInvoice(ctx, generated.CancelInvoiceParams{
		ID:              uid(id),
		AmountHoldUntil: ts(holdUntil),
	})
	if err != nil {
		return domain.Invoice{}, MapError(err, domain.ErrInvalidTransition)
	}
	return toDomainInvoice(row)
}

// FindInvoiceForAmount returns the invoice whose credit window contains value,
// including terminal ones — a late transfer must be recognised as belonging to
// the invoice it was meant for, so it can be filed with the right reason.
func (a *Adapter) FindInvoiceForAmount(ctx context.Context, assetID int64, value *big.Int) (domain.Invoice, error) {
	row, err := a.q.FindHeldInvoiceForAmount(ctx, generated.FindHeldInvoiceForAmountParams{
		AssetID: assetID,
		Amount:  amount(value),
	})
	if err != nil {
		return domain.Invoice{}, MapError(err, domain.ErrInvoiceNotFound)
	}
	return toDomainInvoice(row)
}

// ExpirePendingInvoices moves overdue pending invoices to expired.
func (a *Adapter) ExpirePendingInvoices(ctx context.Context, hold time.Duration, batchSize int32) ([]domain.Invoice, error) {
	rows, err := a.q.ExpirePendingInvoices(ctx, generated.ExpirePendingInvoicesParams{
		Hold:      interval(hold),
		BatchSize: batchSize,
	})
	if err != nil {
		return nil, MapError(err, nil)
	}
	return toDomainInvoices(rows)
}

// ReleaseExpiredAmountHolds frees payment amounts whose hold has run out, and
// returns how many were freed.
func (a *Adapter) ReleaseExpiredAmountHolds(ctx context.Context) (int64, error) {
	n, err := a.q.ReleaseExpiredAmountHolds(ctx)
	if err != nil {
		return 0, MapError(err, nil)
	}
	return n, nil
}

// CountInvoicesByStatus backs the health and metrics surface.
func (a *Adapter) CountInvoicesByStatus(ctx context.Context) (map[domain.InvoiceStatus]int64, error) {
	rows, err := a.q.CountInvoicesByStatus(ctx)
	if err != nil {
		return nil, MapError(err, nil)
	}
	out := make(map[domain.InvoiceStatus]int64, len(rows))
	for _, r := range rows {
		out[domain.InvoiceStatus(r.Status)] = r.Total
	}
	return out, nil
}
