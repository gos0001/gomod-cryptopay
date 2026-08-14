package invoice_create

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/view"
)

// maxDescriptionLen and maxExternalIDLen bound what a merchant can store. Both
// are echoed back and one of them is indexed; neither is a place for a payload.
const (
	maxDescriptionLen = 500
	maxExternalIDLen  = 200
	maxMetadataBytes  = 8 << 10
)

type Input struct {
	Network string `json:"network"`
	// Symbol names the asset the readable way. ContractAddress is the escape
	// hatch for when two contracts share a symbol on one chain.
	Symbol          string `json:"symbol"`
	ContractAddress string `json:"contract_address"`

	// Amount is a decimal string in whole tokens — "10.50", not smallest units.
	// A string rather than a number because JSON numbers are float64, and an
	// 18-decimal amount does not survive one.
	Amount string `json:"amount"`

	// ExternalID is the merchant's own key and the idempotency key. Optional.
	ExternalID string `json:"external_id"`

	// ExpiresIn overrides the configured TTL, written as a duration string.
	ExpiresIn string `json:"expires_in"`

	// There is no webhook_url here on purpose. The destination is the operator's
	// setting, not the caller's: this is a self-hosted module with one receiver,
	// so taking a URL from a request would have the service posting wherever a
	// caller pointed it and would expose the receiver's domain to anyone holding
	// an API key.
	Description string          `json:"description"`
	Metadata    json.RawMessage `json:"metadata"`

	// expiresIn is the parsed form of ExpiresIn, filled by Validate.
	expiresIn time.Duration
}

// Validate checks and normalises the request. It returns errors wrapping
// domain.ErrInvalidInput so the handler can answer 400 without a second switch.
//
// The amount itself is not parsed here: doing that needs the asset's decimals,
// which the use case has to look up first.
func (in *Input) Validate() error {
	in.Network = strings.TrimSpace(in.Network)
	in.Symbol = strings.TrimSpace(in.Symbol)
	in.ContractAddress = strings.TrimSpace(in.ContractAddress)
	in.Amount = strings.TrimSpace(in.Amount)
	in.ExternalID = strings.TrimSpace(in.ExternalID)
	in.Description = strings.TrimSpace(in.Description)

	if _, err := domain.ParseNetwork(in.Network); err != nil {
		return fmt.Errorf("%w: network must be one of tron, bsc", domain.ErrInvalidInput)
	}
	if in.Symbol == "" && in.ContractAddress == "" {
		return fmt.Errorf("%w: either symbol or contract_address is required", domain.ErrInvalidInput)
	}
	if in.Amount == "" {
		return fmt.Errorf("%w: amount is required", domain.ErrInvalidInput)
	}

	if len(in.ExternalID) > maxExternalIDLen {
		return fmt.Errorf("%w: external_id is longer than %d characters",
			domain.ErrInvalidInput, maxExternalIDLen)
	}
	if len(in.Description) > maxDescriptionLen {
		return fmt.Errorf("%w: description is longer than %d characters",
			domain.ErrInvalidInput, maxDescriptionLen)
	}

	if len(in.Metadata) > maxMetadataBytes {
		return fmt.Errorf("%w: metadata is larger than %d bytes",
			domain.ErrInvalidInput, maxMetadataBytes)
	}
	// Stored as JSONB, so malformed JSON would fail at the database with an
	// error the caller cannot read. Rejected here instead, where the message
	// can name the field.
	if len(in.Metadata) > 0 && !json.Valid(in.Metadata) {
		return fmt.Errorf("%w: metadata is not valid JSON", domain.ErrInvalidInput)
	}

	if in.ExpiresIn != "" {
		d, err := time.ParseDuration(in.ExpiresIn)
		if err != nil {
			return fmt.Errorf("%w: expires_in must be a duration such as \"45m\"",
				domain.ErrInvalidInput)
		}
		in.expiresIn = d
	}

	return nil
}

// RestrictToPublic applies the rules for a caller that presented no API key.
//
// Called after Validate, from the handler, when middleware admitted the request
// through the public path. Each restriction closes something an anonymous caller
// could otherwise do:
//
//   - external_id is the idempotency key, so accepting it would let anyone guess
//     "order-42" and be handed that invoice back in full — id, amounts,
//     description and metadata. It is a read of somebody else's order dressed as
//     a write.
//   - metadata is up to 8 KiB of arbitrary JSON stored per invoice. A browser has
//     no business filling it, and an anonymous caller filling it is storage abuse.
//   - expires_in is ignored rather than refused, because a browser has no reason
//     to send it and a merchant's page might anyway: capping at the configured
//     TTL stops an anonymous caller from holding a nonce — and with it an amount —
//     for a day.
func (in *Input) RestrictToPublic() error {
	if in.ExternalID != "" {
		return fmt.Errorf("%w: external_id is not accepted without %s",
			domain.ErrInvalidInput, "X-Api-Key")
	}
	if len(in.Metadata) > 0 {
		return fmt.Errorf("%w: metadata is not accepted without %s",
			domain.ErrInvalidInput, "X-Api-Key")
	}

	in.ExpiresIn = ""
	in.expiresIn = 0

	return nil
}

type Output struct {
	Invoice view.Invoice `json:"invoice"`
	// Created distinguishes a fresh invoice from one returned again for a
	// repeated external_id. Not serialised — the status code already says it,
	// and the handler is what turns this into 201 versus 200.
	Created bool `json:"-"`
}
