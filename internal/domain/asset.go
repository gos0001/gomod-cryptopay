package domain

import (
	"errors"
	"math/big"
)

var (
	// ErrAssetNotFound is raised when an invoice names an asset that is not
	// configured, or one that has been disabled.
	ErrAssetNotFound = errors.New("asset not found")
	// ErrAssetInvalid is raised by Validate for a malformed configuration entry.
	ErrAssetInvalid = errors.New("asset is invalid")
)

// Asset is one token on one chain: what the watcher looks for and what an
// invoice is denominated in.
//
// Assets are configuration, not code — the whole list comes from the ASSETS
// environment variable and is upserted into cp_assets at boot. Supporting a new
// token is a config line, not a release.
type Asset struct {
	ID int64

	Network Network
	// Symbol is a display label. It is not an identifier: two chains can both
	// carry a token called USDT, and nothing stops an operator configuring two
	// contracts under one symbol.
	Symbol string
	// ContractAddress identifies the token on its chain. Together with Network
	// it is the real key.
	ContractAddress string
	// Decimals is the token's own scale: 6 for TRC20 USDT, 18 for BEP-20 USDT.
	// Amounts are stored in smallest units, so this is only ever needed to
	// render a human-readable figure.
	Decimals int32

	// Step is the gap between two invoices that were asked for the same amount,
	// in smallest units. It is the resolution of the whole matching scheme:
	// a transfer is credited when it lands in [PayAmount, PayAmount+Step).
	//
	// Too small and a wallet that rounds its input misses the window; too large
	// and the surcharge on the last invoice of a batch becomes noticeable.
	// 0.0001 of a token is the usual answer — 100 units at 6 decimals, 10^14 at
	// 18.
	Step *big.Int
	// NonceMax caps how many invoices can be outstanding for one base amount.
	// Exhausting it is a real, reachable state; see ErrAmountSpaceExhausted.
	NonceMax int32

	Enabled bool
}

func (a Asset) Validate() error {
	if !a.Network.Valid() {
		return ErrUnknownNetwork
	}
	if a.Symbol == "" {
		return errors.New("asset: symbol is empty")
	}
	if a.ContractAddress == "" {
		return errors.New("asset: contract address is empty")
	}
	if a.Decimals < 0 || a.Decimals > 36 {
		return errors.New("asset: decimals out of range")
	}
	if a.Step == nil || a.Step.Sign() <= 0 {
		return errors.New("asset: step must be positive")
	}
	if a.NonceMax <= 0 {
		return errors.New("asset: nonce_max must be positive")
	}
	return nil
}
