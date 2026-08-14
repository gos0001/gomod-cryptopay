// Package view holds the JSON shapes the API returns.
//
// Four use cases return an invoice, and a copy of the shape in each of them is
// how a client ends up parsing two schemas that drifted apart. The types live
// here instead, and each use case's Output embeds or lists them.
//
// Presentation only: no logic, no storage, and the conversions are the one way
// round — domain in, view out.
package view

import (
	"encoding/json"
	"math/big"
	"time"

	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/pkg/cryptoamount"
)

// Invoice is the shape every invoice endpoint returns.
type Invoice struct {
	ID         string `json:"id"`
	ExternalID string `json:"external_id,omitempty"`

	Network  string `json:"network"`
	Symbol   string `json:"symbol"`
	Contract string `json:"contract_address"`
	Decimals int32  `json:"decimals"`

	// PayAddress and PayAmount are what the payer must use.
	//
	// Nothing else here is safe to put in front of them: Amount is what the
	// merchant asked for, and a transfer of exactly that would fall outside the
	// matching window and be filed as an orphan.
	PayAddress string `json:"pay_address"`
	PayAmount  string `json:"pay_amount"`
	// PayAmountUnits is the same figure in the token's smallest units, for
	// reconciliation that should not pass through anyone's decimal parser.
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

// NewInvoice renders an invoice. The asset is passed separately because the
// invoice stores only its id, and the decimals needed to format the amounts
// live on the asset.
func NewInvoice(inv domain.Invoice, asset domain.Asset) Invoice {
	return Invoice{
		ID:         inv.ID.String(),
		ExternalID: inv.ExternalID,

		Network:  string(inv.Network),
		Symbol:   asset.Symbol,
		Contract: asset.ContractAddress,
		Decimals: asset.Decimals,

		PayAddress:     inv.PayAddress,
		PayAmount:      cryptoamount.Format(inv.PayAmount, asset.Decimals),
		PayAmountUnits: units(inv.PayAmount),
		Amount:         cryptoamount.Format(inv.BaseAmount, asset.Decimals),

		Status:        string(inv.Status),
		Confirmations: inv.Confirmations,

		Description: inv.Description,
		Metadata:    json.RawMessage(inv.Metadata),

		CreatedAt: inv.CreatedAt,
		ExpiresAt: inv.ExpiresAt,
		PaidAt:    inv.PaidAt,
	}
}

// Asset is one configured token as the API presents it.
type Asset struct {
	Network  string `json:"network"`
	Symbol   string `json:"symbol"`
	Contract string `json:"contract_address"`
	Decimals int32  `json:"decimals"`

	// Step and NonceMax are exposed so an integrator can reason about the
	// matching window themselves: a transfer settles an invoice when it lands
	// in [pay_amount, pay_amount + step).
	Step     string `json:"step"`
	NonceMax int32  `json:"nonce_max"`
}

func NewAsset(a domain.Asset) Asset {
	return Asset{
		Network:  string(a.Network),
		Symbol:   a.Symbol,
		Contract: a.ContractAddress,
		Decimals: a.Decimals,
		Step:     units(a.Step),
		NonceMax: a.NonceMax,
	}
}

// Orphan is a transfer that arrived and could not be attributed.
type Orphan struct {
	Network  string `json:"network"`
	TxHash   string `json:"tx_hash"`
	LogIndex int32  `json:"log_index"`

	Contract string `json:"contract_address"`
	From     string `json:"from_address"`
	To       string `json:"to_address"`
	// Amount is in smallest units and deliberately not formatted: the token may
	// be one this service does not know, in which case there are no decimals to
	// format it with.
	Amount string `json:"amount_units"`

	BlockNumber int64     `json:"block_number"`
	BlockTime   time.Time `json:"block_time"`

	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

func NewOrphan(o domain.OrphanTransfer) Orphan {
	return Orphan{
		Network:     string(o.Network),
		TxHash:      o.TxHash,
		LogIndex:    o.LogIndex,
		Contract:    o.ContractAddress,
		From:        o.FromAddress,
		To:          o.ToAddress,
		Amount:      units(o.Amount),
		BlockNumber: o.BlockNumber,
		BlockTime:   o.BlockTime,
		Reason:      string(o.Reason),
		CreatedAt:   o.CreatedAt,
	}
}

func units(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}
