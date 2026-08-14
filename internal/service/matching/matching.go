// Package matching decides what an observed on-chain transfer means for an
// invoice, and writes the consequences.
//
// A service rather than a use case because both watchers call it, and a use case
// whose callers are other use cases is a service by another name. goauth has the
// same shape in internal/service.
//
// The engine never touches the network, knows nothing about polling intervals or
// cursors, and does not decide whether a transfer is final — the watcher settles
// that, because the two chains answer it in completely different ways. What is
// left is the part that must not go wrong: crediting the right invoice exactly
// once, and recording anything it cannot credit instead of dropping it.
package matching

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/invoicecfg"
	"github.com/gos0001/gomod-cryptopay/pkg/webhook"
)

// Event names written to the outbox.
const (
	EventDetected  = "invoice.detected"
	EventConfirmed = "invoice.confirmed"
	EventReverted  = "invoice.reverted"
)

// Observed is one transfer a watcher has seen, in the form both chains reduce to.
type Observed struct {
	Network  domain.Network
	TxHash   string
	LogIndex int32
	Contract string
	From     string
	To       string
	Value    *big.Int

	// BlockNumber is zero on TRON, whose feed does not supply one.
	BlockNumber int64
	BlockTime   time.Time

	// Final is the watcher's verdict on irreversibility. TRON compares the
	// transfer's timestamp against the solidified head; BSC compares its block
	// against the finalised one. The engine only needs the answer.
	Final bool
}

// Outcome says what the engine did, for logging and tests.
type Outcome string

const (
	// OutcomeCredited: the transfer settled or advanced an invoice.
	OutcomeCredited Outcome = "credited"
	// OutcomeUnchanged: already known and the invoice was already in the right
	// state. The normal result of re-reading a transfer on a later poll.
	OutcomeUnchanged Outcome = "unchanged"
	// OutcomeOrphaned: money arrived that could not be attributed.
	OutcomeOrphaned Outcome = "orphaned"
)

type Result struct {
	Outcome Outcome
	// InvoiceID is set when the transfer was credited.
	InvoiceID *uuid.UUID
	Status    domain.InvoiceStatus
	// Reason is set when the transfer was orphaned.
	Reason domain.OrphanReason
}

// Postgres is the slice of the adapter this service needs.
type Postgres interface {
	GetAssetByContract(ctx context.Context, network domain.Network, contract string) (domain.Asset, error)
	GetAssetByID(ctx context.Context, id int64) (domain.Asset, error)
	FindInvoiceForAmount(ctx context.Context, assetID int64, value *big.Int) (domain.Invoice, error)
	GetInvoiceByID(ctx context.Context, id uuid.UUID) (domain.Invoice, error)
	RecordPayment(ctx context.Context, p domain.Payment) (domain.Payment, bool, error)
	GetPaymentByChainRef(ctx context.Context, network domain.Network, txHash string, logIndex int32) (domain.Payment, error)
	MarkPaymentRemoved(ctx context.Context, network domain.Network, txHash string, logIndex int32) (domain.Payment, error)
	TransitionInvoice(ctx context.Context, id uuid.UUID, from, to domain.InvoiceStatus,
		confirmations int32, paidAt *time.Time, holdUntil time.Time) (domain.Invoice, error)
	RecordOrphanTransfer(ctx context.Context, o domain.OrphanTransfer) error
	EnqueueWebhookEvent(ctx context.Context, e postgresadapter.WebhookEvent) error

	// WithTx hands back the same narrow interface rather than the concrete
	// adapter. The adapter's own WithTx passes *postgresadapter.Adapter, which
	// would make this service untestable without a live database — a fake cannot
	// manufacture one. txRunner below bridges the two.
	WithTx(ctx context.Context, fn func(tx Postgres) error) error
}

// txRunner adapts the concrete adapter to Postgres, translating the
// transactional callback's parameter on the way through. Embedding supplies
// every other method unchanged.
type txRunner struct {
	*postgresadapter.Adapter
}

func (r txRunner) WithTx(ctx context.Context, fn func(tx Postgres) error) error {
	return r.Adapter.WithTx(ctx, func(tx *postgresadapter.Adapter) error {
		return fn(txRunner{tx})
	})
}

// Notifier tells the engine whether events are worth queueing at all.
type Notifier interface {
	Enabled() bool
}

type Service struct {
	postgres Postgres
	cfg      invoicecfg.Config
	webhooks Notifier
	logger   *zap.SugaredLogger
	now      func() time.Time
	newID    func() uuid.UUID
}

// New takes the concrete adapter because wire resolves concrete types.
func New(
	pg *postgresadapter.Adapter,
	cfg invoicecfg.Config,
	sender *webhook.Sender,
	logger *zap.SugaredLogger,
) *Service {
	return &Service{
		postgres: txRunner{pg}, cfg: cfg, webhooks: sender,
		logger: logger, now: time.Now, newID: uuid.New,
	}
}

// Apply records a transfer and moves its invoice, or files the transfer as
// unattributable.
func (s *Service) Apply(ctx context.Context, in Observed) (Result, error) {
	if in.Value == nil || in.Value.Sign() <= 0 {
		return Result{}, fmt.Errorf("matching: transfer %s carries no positive value", in.TxHash)
	}

	// Already recorded? Then this is a re-read, not new money.
	//
	// This check has to come first, and the reason is not obvious: the BSC
	// watcher rewinds its cursor by the reorg depth on every start, so it
	// legitimately re-observes transfers it has already credited. Without this,
	// a transfer whose invoice has since reached a terminal state would fall
	// through to the terminal-invoice branch and be filed as an orphan — a
	// duplicate record of money already credited, appearing afresh on every
	// restart. Found on the Hardhat bench, not by reasoning.
	if existing, err := s.postgres.GetPaymentByChainRef(ctx, in.Network, in.TxHash, in.LogIndex); err == nil {
		if existing.Live() {
			return s.reapply(ctx, existing, in.Final)
		}
		// Withdrawn earlier and now seen again: the transfer is back on chain,
		// so treat it as new and let the normal path re-credit it.
	} else if !errors.Is(err, domain.ErrNotFound) {
		return Result{}, fmt.Errorf("matching: look up payment %s: %w", in.TxHash, err)
	}

	// Disabled assets are included deliberately: a token switched off after an
	// invoice was issued still has to be recognised, or a real payment for a
	// real invoice would be filed as an unknown token.
	asset, err := s.postgres.GetAssetByContract(ctx, in.Network, in.Contract)
	if err != nil {
		if errors.Is(err, domain.ErrAssetNotFound) {
			return s.orphan(ctx, in, 0, domain.OrphanUnknownAsset)
		}
		return Result{}, fmt.Errorf("matching: resolve asset %s: %w", in.Contract, err)
	}

	invoice, err := s.postgres.FindInvoiceForAmount(ctx, asset.ID, in.Value)
	if err != nil {
		if errors.Is(err, domain.ErrInvoiceNotFound) {
			return s.orphan(ctx, in, asset.ID, domain.OrphanNoInvoice)
		}
		return Result{}, fmt.Errorf("matching: find invoice for %s: %w", in.Value, err)
	}

	// A late transfer against an invoice that already ended. Recognised as
	// belonging to it — hence the reason — but not credited: the amount is held
	// precisely so this cannot pay whoever inherited it.
	if invoice.Status.IsTerminal() {
		return s.orphan(ctx, in, asset.ID, domain.OrphanInvoiceTerminal)
	}

	return s.credit(ctx, in, asset, invoice)
}

// reapply handles a transfer that is already on record.
//
// Nothing is written unless the transfer has become final since it was last
// seen, in which case its invoice is settled. Re-reading is the normal case on
// every tick, so this is the quiet path.
func (s *Service) reapply(ctx context.Context, existing domain.Payment, final bool) (Result, error) {
	if !final {
		return Result{Outcome: OutcomeUnchanged, InvoiceID: existing.InvoiceID}, nil
	}
	return s.Settle(ctx, existing)
}

// credit writes the payment, the status change and the notification together.
func (s *Service) credit(ctx context.Context, in Observed, asset domain.Asset, invoice domain.Invoice) (Result, error) {
	target := domain.InvoiceStatusDetected
	if in.Final {
		target = domain.InvoiceStatusConfirmed
	}

	result := Result{Outcome: OutcomeUnchanged, InvoiceID: &invoice.ID, Status: invoice.Status}

	err := s.postgres.WithTx(ctx, func(tx Postgres) error {
		// Idempotent: ON CONFLICT DO NOTHING, so re-reading a transfer does not
		// create a second payment.
		//
		// The `created` flag is deliberately NOT used to decide whether to move
		// the invoice. A transfer first seen before it was final comes back
		// final on a later poll with created=false, and branching on it would
		// leave that invoice in detected forever.
		_, _, err := tx.RecordPayment(ctx, domain.Payment{
			Network:     in.Network,
			TxHash:      in.TxHash,
			LogIndex:    in.LogIndex,
			AssetID:     asset.ID,
			FromAddress: in.From,
			ToAddress:   in.To,
			Amount:      in.Value,
			BlockNumber: in.BlockNumber,
			BlockTime:   in.BlockTime,
			InvoiceID:   &invoice.ID,
		})
		if err != nil {
			return fmt.Errorf("record payment %s: %w", in.TxHash, err)
		}

		if invoice.Status == target {
			return nil
		}
		if err := invoice.Transition(target); err != nil {
			// Not fatal to the tick: a concurrent watcher or the expirer may
			// have moved the invoice. The payment is recorded either way.
			s.logger.Warnw("refusing an invoice transition",
				"invoice", invoice.ID, "from", invoice.Status, "to", target, "error", err)
			return nil
		}

		var paidAt *time.Time
		if target == domain.InvoiceStatusConfirmed {
			at := s.now()
			paidAt = &at
		}

		// The hold is extended past the invoice's end so a duplicate or late
		// transfer cannot pay whichever invoice inherits the amount.
		holdUntil := s.now().Add(s.cfg.AmountHold.Std())

		updated, err := tx.TransitionInvoice(ctx, invoice.ID,
			invoice.Status, target, invoice.Confirmations, paidAt, holdUntil)
		if err != nil {
			if errors.Is(err, domain.ErrInvalidTransition) {
				// The compare-and-set matched nothing: someone got there first.
				// Losing that race is not an error, and the payment stands.
				s.logger.Infow("invoice moved concurrently",
					"invoice", invoice.ID, "expected", invoice.Status)
				return nil
			}
			return fmt.Errorf("transition invoice %s: %w", invoice.ID, err)
		}

		event := EventDetected
		if target == domain.InvoiceStatusConfirmed {
			event = EventConfirmed
		}
		if err := s.enqueue(ctx, tx, updated, asset, event); err != nil {
			return err
		}

		result.Outcome = OutcomeCredited
		result.Status = updated.Status
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	return result, nil
}

// Settle promotes an already-recorded payment whose transfer has crossed the
// finality line.
//
// Separate from Apply on purpose. Apply resolves the asset from a contract
// address and finds the invoice by amount, because that is all a freshly
// observed transfer carries. A stored payment already knows its asset and its
// invoice, so re-running that resolution would be both wasted work and wrong —
// the settle path has no contract address to resolve from.
func (s *Service) Settle(ctx context.Context, payment domain.Payment) (Result, error) {
	if payment.InvoiceID == nil {
		return Result{Outcome: OutcomeUnchanged}, nil
	}
	if !payment.Live() {
		// Withdrawn by a reorg between the listing and now.
		return Result{Outcome: OutcomeUnchanged}, nil
	}

	result := Result{Outcome: OutcomeUnchanged, InvoiceID: payment.InvoiceID}

	err := s.postgres.WithTx(ctx, func(tx Postgres) error {
		invoice, err := tx.GetInvoiceByID(ctx, *payment.InvoiceID)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", *payment.InvoiceID, err)
		}
		result.Status = invoice.Status

		if invoice.Status == domain.InvoiceStatusConfirmed {
			return nil
		}
		if err := invoice.Transition(domain.InvoiceStatusConfirmed); err != nil {
			s.logger.Warnw("refusing to settle an invoice",
				"invoice", invoice.ID, "from", invoice.Status, "error", err)
			return nil
		}

		at := s.now()
		updated, err := tx.TransitionInvoice(ctx, invoice.ID,
			invoice.Status, domain.InvoiceStatusConfirmed,
			invoice.Confirmations, &at, s.now().Add(s.cfg.AmountHold.Std()))
		if err != nil {
			if errors.Is(err, domain.ErrInvalidTransition) {
				return nil
			}
			return fmt.Errorf("settle invoice %s: %w", invoice.ID, err)
		}

		asset, err := tx.GetAssetByID(ctx, updated.AssetID)
		if err != nil {
			return fmt.Errorf("load asset %d: %w", updated.AssetID, err)
		}

		if err := s.enqueue(ctx, tx, updated, asset, EventConfirmed); err != nil {
			return err
		}

		result.Outcome = OutcomeCredited
		result.Status = updated.Status
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	return result, nil
}

// Revoke handles a transfer that a reorg un-mined.
//
// Only EVM chains report this, via a log's `removed` flag. TRON offers no such
// signal, which is why a TRON payment is only ever confirmed once it is past the
// solidified head — there is nothing above it that could need revoking.
func (s *Service) Revoke(ctx context.Context, network domain.Network, txHash string, logIndex int32) error {
	payment, err := s.postgres.GetPaymentByChainRef(ctx, network, txHash, logIndex)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// A removal for a transfer never credited. Nothing to undo.
			return nil
		}
		return fmt.Errorf("matching: look up payment %s: %w", txHash, err)
	}
	if !payment.Live() {
		return nil // already revoked on an earlier tick
	}

	return s.postgres.WithTx(ctx, func(tx Postgres) error {
		if _, err := tx.MarkPaymentRemoved(ctx, network, txHash, logIndex); err != nil {
			return fmt.Errorf("mark payment %s removed: %w", txHash, err)
		}

		if payment.InvoiceID == nil {
			return nil
		}

		invoice, err := tx.GetInvoiceByID(ctx, *payment.InvoiceID)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", *payment.InvoiceID, err)
		}

		// Only a detected invoice goes back to pending. A confirmed one is past
		// the finality line, so a removal at this depth would mean the chain
		// reorganised deeper than it claims is possible — worth shouting about
		// rather than quietly un-crediting settled money.
		if invoice.Status == domain.InvoiceStatusConfirmed {
			s.logger.Errorw("a confirmed payment was reported as removed; "+
				"the chain reorganised past its own finality line",
				"invoice", invoice.ID, "tx", txHash, "network", network)
			return nil
		}
		if invoice.Status != domain.InvoiceStatusDetected {
			return nil
		}

		updated, err := tx.TransitionInvoice(ctx, invoice.ID,
			domain.InvoiceStatusDetected, domain.InvoiceStatusPending,
			0, nil, invoice.AmountHoldUntil)
		if err != nil {
			if errors.Is(err, domain.ErrInvalidTransition) {
				return nil
			}
			return fmt.Errorf("return invoice %s to pending: %w", invoice.ID, err)
		}

		asset, err := tx.GetAssetByID(ctx, updated.AssetID)
		if err != nil {
			return fmt.Errorf("load asset %d: %w", updated.AssetID, err)
		}

		s.logger.Warnw("payment withdrawn by a reorg; the invoice is payable again",
			"invoice", invoice.ID, "tx", txHash, "network", network)

		return s.enqueue(ctx, tx, updated, asset, EventReverted)
	})
}

// orphan files a transfer that could not be credited.
//
// Recorded rather than ignored: with amount-based matching, a payer who rounds
// their input produces a transfer nothing will ever claim, and the operator
// needs to see it to refund or credit it by hand.
func (s *Service) orphan(ctx context.Context, in Observed, assetID int64, reason domain.OrphanReason) (Result, error) {
	err := s.postgres.RecordOrphanTransfer(ctx, domain.OrphanTransfer{
		Network:         in.Network,
		TxHash:          in.TxHash,
		LogIndex:        in.LogIndex,
		AssetID:         assetID,
		ContractAddress: in.Contract,
		FromAddress:     in.From,
		ToAddress:       in.To,
		Amount:          in.Value,
		BlockNumber:     in.BlockNumber,
		BlockTime:       in.BlockTime,
		Reason:          reason,
	})
	if err != nil {
		return Result{}, fmt.Errorf("matching: record orphan %s: %w", in.TxHash, err)
	}

	s.logger.Warnw("transfer could not be attributed",
		"network", in.Network, "tx", in.TxHash, "amount", in.Value, "reason", reason)

	return Result{Outcome: OutcomeOrphaned, Reason: reason}, nil
}

// enqueue writes an outbox event inside the caller's transaction, so the notice
// and the status change commit together.
//
// Delivery is somebody else's problem: webhook_dispatcher drains the table on its
// own schedule. That split is what makes a receiver being down delay events
// rather than lose them, or worse, roll back the status change that produced one.
func (s *Service) enqueue(ctx context.Context, tx Postgres,
	invoice domain.Invoice, asset domain.Asset, event string,
) error {
	if !s.webhooks.Enabled() {
		// Nothing to deliver to, so nothing is queued: an outbox that only ever
		// grows is worse than no outbox.
		return nil
	}

	payload, err := json.Marshal(map[string]any{
		"event":      event,
		"invoice_id": invoice.ID.String(),
		"status":     string(invoice.Status),
		"network":    string(invoice.Network),
		"symbol":     asset.Symbol,
		"pay_amount": invoice.PayAmount.String(),
		"decimals":   asset.Decimals,
	})
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", event, err)
	}

	return tx.EnqueueWebhookEvent(ctx, postgresadapter.WebhookEvent{
		EventID:   s.newID(),
		InvoiceID: &invoice.ID,
		Event:     event,
		Payload:   payload,
	})
}
