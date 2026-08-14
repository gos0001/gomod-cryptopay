package middleware

import (
	"errors"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// PublicConfig opens invoice creation to callers with no API key, for the case
// the endpoint is called straight from a customer's browser.
//
// Off by default, and that is not timidity: switching it on changes who may write
// to the database, and pulling a newer image must not do that on an operator's
// behalf.
type PublicConfig struct {
	// InvoiceCreate allows POST /api/v1/invoices without a key. Everything else
	// stays behind the key — a browser cannot list invoices, cancel one, or read
	// orphan transfers.
	InvoiceCreate bool `json:"invoice_create"`

	// RatePerMinute and Burst bound one client address.
	//
	// The limit is not about bandwidth. An invoice consumes a nonce, assets have
	// nonce_max of them (1000 by default), and a used amount stays reserved for
	// ttl + amount_hold after the invoice ends. Unlimited anonymous creation
	// therefore exhausts the amount space and denies payment to real customers —
	// that, not disk, is what this protects.
	RatePerMinute float64 `json:"rate_per_minute"`
	Burst         int     `json:"burst"`
}

func LoadPublicConfig(f *config.File) (PublicConfig, error) {
	cfg := PublicConfig{RatePerMinute: 30, Burst: 10}
	if err := f.Section("public_api", &cfg); err != nil {
		return cfg, err
	}

	if cfg.RatePerMinute <= 0 {
		return cfg, errors.New("config: public_api.rate_per_minute must be positive " +
			"(set public_api.invoice_create to false to close the endpoint instead)")
	}
	if cfg.Burst <= 0 {
		return cfg, errors.New("config: public_api.burst must be positive")
	}

	return cfg, nil
}
