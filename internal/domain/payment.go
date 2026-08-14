package domain

import (
	"errors"
	"math/big"
	"time"

	"github.com/google/uuid"
)

var ErrPaymentAlreadyRecorded = errors.New("payment already recorded")

// Payment is one incoming token transfer the watcher has seen, credited to an
// invoice.
//
// Network, TxHash and LogIndex together identify it on chain, and that triple
// is unique in storage: a watcher re-reads the same transfer on every poll, and
// the database is what makes re-reading harmless rather than a second credit.
type Payment struct {
	ID int64

	Network  Network
	TxHash   string
	LogIndex int32

	AssetID     int64
	FromAddress string
	ToAddress   string
	Amount      *big.Int

	// BlockNumber is zero on TRON: its transfer feed carries no block number at
	// all, so BlockTime is the authoritative position there and finality is
	// decided by comparing it against the solidified head's timestamp.
	//
	// On EVM chains both are present, and the number is what gets compared
	// against the finalised head.
	BlockNumber int64
	BlockTime   time.Time

	InvoiceID     *uuid.UUID
	Confirmations int32

	// RemovedAt is set when a reorg un-mined the transfer. A removed payment is
	// history: it no longer holds its invoice, which becomes payable again.
	RemovedAt *time.Time

	CreatedAt time.Time
}

// Live reports whether the payment still counts. A withdrawn one is kept for
// reconciliation but must not be treated as money received.
func (p Payment) Live() bool { return p.RemovedAt == nil }

// OrphanReason says why a transfer could not be credited. It is operator-facing
// only; nothing branches on it.
type OrphanReason string

const (
	// OrphanNoInvoice: no invoice holds that amount for that asset.
	OrphanNoInvoice OrphanReason = "no_matching_invoice"
	// OrphanInvoiceTerminal: the amount belongs to an invoice that is already
	// expired, cancelled or paid — a late or duplicate transfer.
	OrphanInvoiceTerminal OrphanReason = "invoice_terminal"
	// OrphanUnknownAsset: a transfer of a token this service does not track,
	// sent to the receiving address anyway.
	OrphanUnknownAsset OrphanReason = "unknown_asset"
)

// OrphanTransfer is money that arrived at the receiving address and could not
// be attributed.
//
// It is recorded rather than ignored because the alternative is silent loss:
// with amount-based matching, a payer who rounds their input produces a
// transfer nothing will ever claim, and the operator needs to see it to refund
// or credit it by hand.
type OrphanTransfer struct {
	ID int64

	Network  Network
	TxHash   string
	LogIndex int32

	// AssetID is zero when the token itself is unrecognised; ContractAddress is
	// always populated, so an unknown token is still identifiable.
	AssetID         int64
	ContractAddress string
	FromAddress     string
	ToAddress       string
	Amount          *big.Int

	BlockNumber int64
	BlockTime   time.Time

	Reason    OrphanReason
	CreatedAt time.Time
}

// ChainCursor is how far a watcher has scanned.
//
// Two positions rather than one because the chains are enumerated differently:
// BSC is scanned by block range, TRON by transfer timestamp. Each watcher uses
// its own field and leaves the other zero.
type ChainCursor struct {
	Network       Network
	LastBlock     int64
	LastTimestamp time.Time
	UpdatedAt     time.Time
}
