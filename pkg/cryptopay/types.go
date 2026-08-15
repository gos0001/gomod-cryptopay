package cryptopay

import (
	"encoding/json"
	"math/big"
	"time"
)

// Invoice is what every invoice endpoint returns.
//
// Amounts are decimal strings, exactly as the API sends them. They are not
// parsed into a float anywhere in this package and should not be in yours: an
// 18-decimal amount does not survive a float64, and a rounded figure falls
// outside the matching window, which means it is filed as an orphan transfer
// rather than paying anything.
type Invoice struct {
	ID         string `json:"id"`
	ExternalID string `json:"external_id,omitempty"`

	Network  string `json:"network"`
	Symbol   string `json:"symbol"`
	Contract string `json:"contract_address"`
	Decimals int32  `json:"decimals"`

	// PayAddress and PayAmount are what the payer must use.
	//
	// Show PayAmount, never Amount. Amount is what was requested; a transfer of
	// it lands outside the credit window. The first invoice at a given amount
	// takes nonce 0, where the two are identical — which is why reading the wrong
	// one survives testing and fails on the second customer.
	PayAddress string `json:"pay_address"`
	PayAmount  string `json:"pay_amount"`
	// PayAmountUnits is the same figure in the token's smallest units, for
	// comparisons that should not pass through a decimal parser.
	PayAmountUnits string `json:"pay_amount_units"`

	// Amount is the requested figure, before the uniquifying offset.
	Amount string `json:"amount"`

	Status        string `json:"status"`
	Confirmations int32  `json:"confirmations"`

	Description string          `json:"description,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	PaidAt    *time.Time `json:"paid_at,omitempty"`
}

// PayAmountUnitsBig parses PayAmountUnits, the figure to compare an observed
// transfer against. The bool is false when the field is absent or malformed.
func (i Invoice) PayAmountUnitsBig() (*big.Int, bool) { return parseUnits(i.PayAmountUnits) }

// Asset is one configured token.
type Asset struct {
	Network  string `json:"network"`
	Symbol   string `json:"symbol"`
	Contract string `json:"contract_address"`
	Decimals int32  `json:"decimals"`
	// Step is the increment between two invoices' amounts, in smallest units.
	Step     string `json:"step"`
	NonceMax int32  `json:"nonce_max"`
}

// StepBig parses Step. False when absent or malformed.
func (a Asset) StepBig() (*big.Int, bool) { return parseUnits(a.Step) }

// Orphan is a transfer that arrived and could not be attributed to an invoice —
// the wrong amount, an unknown token, or a payment against an invoice that had
// already ended. Nothing is ever silently dropped; it lands here for a human.
type Orphan struct {
	Network  string `json:"network"`
	TxHash   string `json:"tx_hash"`
	LogIndex int32  `json:"log_index"`

	Contract string `json:"contract_address"`
	From     string `json:"from_address"`
	To       string `json:"to_address"`
	// Amount is in smallest units and deliberately not formatted: the token may
	// be one the service does not know, in which case there are no decimals to
	// format it with.
	Amount string `json:"amount_units"`

	BlockNumber int64     `json:"block_number"`
	BlockTime   time.Time `json:"block_time"`

	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// AmountBig parses Amount. False when absent or malformed.
func (o Orphan) AmountBig() (*big.Int, bool) { return parseUnits(o.Amount) }

// Health is what /healthz answers.
type Health struct {
	Status   string `json:"status"`
	Database string `json:"database"`
}

func (h Health) Healthy() bool { return h.Status == "ok" }

// Invoice statuses. Constants rather than strings so a typo is a compile error.
//
// confirmed, expired and cancelled are terminal. Note what is missing:
// detected never becomes expired, because once money is on chain the clock must
// not void the invoice.
const (
	StatusPending   = "pending"
	StatusDetected  = "detected"
	StatusConfirmed = "confirmed"
	StatusExpired   = "expired"
	StatusCancelled = "cancelled"
)

// Networks the service watches.
const (
	NetworkTron = "tron"
	NetworkBSC  = "bsc"
)

func parseUnits(s string) (*big.Int, bool) {
	if s == "" {
		return nil, false
	}
	v, ok := new(big.Int).SetString(s, 10)
	return v, ok
}
