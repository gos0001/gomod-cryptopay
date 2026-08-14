package domain

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvoiceNotFound = errors.New("invoice not found")
	// ErrExternalIDTaken means the caller reused an external ID for a different
	// invoice. Reusing it for an identical one is not an error — that is the
	// idempotency the field exists for.
	ErrExternalIDTaken = errors.New("external id already used for a different invoice")
	// ErrAmountSpaceExhausted means every nonce for this asset and base amount
	// is currently held. Retryable, not permanent: holds expire.
	ErrAmountSpaceExhausted = errors.New("no free payment amount for this asset")
	// ErrInvalidTransition guards the status machine below.
	ErrInvalidTransition = errors.New("invalid invoice status transition")
)

type InvoiceStatus string

const (
	// InvoiceStatusPending is an invoice awaiting a transfer.
	InvoiceStatusPending InvoiceStatus = "pending"
	// InvoiceStatusDetected means a matching transfer is on chain but has not
	// reached the confirmation threshold.
	InvoiceStatusDetected InvoiceStatus = "detected"
	// InvoiceStatusConfirmed is paid and settled.
	InvoiceStatusConfirmed InvoiceStatus = "confirmed"
	// InvoiceStatusExpired means the window closed with no transfer.
	InvoiceStatusExpired InvoiceStatus = "expired"
	// InvoiceStatusCancelled means the merchant withdrew the invoice.
	InvoiceStatusCancelled InvoiceStatus = "cancelled"
)

func (s InvoiceStatus) Valid() bool {
	switch s {
	case InvoiceStatusPending, InvoiceStatusDetected,
		InvoiceStatusConfirmed, InvoiceStatusExpired, InvoiceStatusCancelled:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether no further transition is possible.
func (s InvoiceStatus) IsTerminal() bool {
	switch s {
	case InvoiceStatusConfirmed, InvoiceStatusExpired, InvoiceStatusCancelled:
		return true
	default:
		return false
	}
}

// invoiceTransitions is the whole state machine.
//
// Two entries are worth reading twice:
//
//   - pending → confirmed skips detected. A watcher that first sees a transfer
//     already buried under the confirmation threshold — after downtime, say —
//     must not have to invent an intermediate state it never observed.
//
//   - detected → pending is the reorg path. A transfer can be un-mined, and the
//     invoice then owes money again. Without this the invoice would be stuck
//     detected forever, and its amount held against a payment that no longer
//     exists.
//
// Note what is absent: detected → expired. Once money is on chain, letting the
// clock void the invoice would strand a real transfer.
var invoiceTransitions = map[InvoiceStatus][]InvoiceStatus{
	InvoiceStatusPending: {
		InvoiceStatusDetected,
		InvoiceStatusConfirmed,
		InvoiceStatusExpired,
		InvoiceStatusCancelled,
	},
	InvoiceStatusDetected: {
		InvoiceStatusConfirmed,
		InvoiceStatusPending,
	},
	InvoiceStatusConfirmed: {},
	InvoiceStatusExpired:   {},
	InvoiceStatusCancelled: {},
}

// CanTransitionTo reports whether next is reachable from s.
func (s InvoiceStatus) CanTransitionTo(next InvoiceStatus) bool {
	for _, allowed := range invoiceTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Invoice is a request for a specific amount of a specific token, at a fixed
// address, within a window.
//
// There is no per-invoice address: the service holds no keys. Identification is
// by amount instead, which is why PayAmount rather than BaseAmount is what the
// payer must send.
type Invoice struct {
	ID uuid.UUID
	// ExternalID is the merchant's own key, and the idempotency key for
	// creation. Optional.
	ExternalID string

	AssetID int64
	Network Network
	// PayAddress is the service-wide receiving address for this network, copied
	// onto the invoice so that rotating the configured address later does not
	// rewrite the history of where callers were told to pay.
	PayAddress string

	// BaseAmount is what the merchant asked for; PayAmount is that plus the
	// uniquifying offset, and is the only figure a payer should ever see.
	BaseAmount *big.Int
	PayAmount  *big.Int
	Nonce      int32

	Status        InvoiceStatus
	Confirmations int32

	Description string
	Metadata    []byte // opaque merchant JSON, stored and echoed, never parsed

	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time
	// AmountHoldUntil is when PayAmount may be handed to another invoice. It
	// outlives the invoice's terminal status on purpose: a transfer sent just
	// before expiry can land well after it, and must not be credited to
	// whichever invoice inherited the amount.
	AmountHoldUntil time.Time
	PaidAt          *time.Time
}

// Transition validates a status change and reports what to write.
func (i Invoice) Transition(next InvoiceStatus) error {
	if !next.Valid() {
		return fmt.Errorf("%w: %q is not a status", ErrInvalidTransition, next)
	}
	if i.Status == next {
		// Idempotent by design: watchers re-observe the same transfer on every
		// poll, and re-applying a settled state must not be an error.
		return nil
	}
	if !i.Status.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, i.Status, next)
	}
	return nil
}

// AmountMatches reports whether an observed transfer of value settles this
// invoice.
//
// The window is half-open: [PayAmount, PayAmount+step). The lower bound is
// inclusive because an exact payment is the normal case. The upper bound is
// exclusive because PayAmount+step is, by construction, some other invoice's
// amount — crediting it here would take that invoice's money.
//
// Everything outside the window, underpayment included, is somebody else's
// transfer as far as this invoice is concerned. That is the price of not having
// a unique address per invoice, and the reason unmatched transfers are recorded
// rather than dropped.
func (i Invoice) AmountMatches(value, step *big.Int) bool {
	if value == nil || i.PayAmount == nil || step == nil || step.Sign() <= 0 {
		return false
	}
	if value.Cmp(i.PayAmount) < 0 {
		return false
	}
	upper := new(big.Int).Add(i.PayAmount, step)
	return value.Cmp(upper) < 0
}

// IsExpired reports whether the payment window has closed at t.
func (i Invoice) IsExpired(t time.Time) bool {
	return !i.ExpiresAt.IsZero() && t.After(i.ExpiresAt)
}
