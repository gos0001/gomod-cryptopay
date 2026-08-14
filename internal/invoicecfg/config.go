// Package invoicecfg holds the settings shared by every invoice use case.
//
// A package rather than a section struct repeated in three places: invoice_create
// needs all of it, invoice_cancel needs the hold, invoice_expirer needs the hold
// and the interval. Three copies of the same struct diverge at the first edit,
// and amount_hold in particular has to agree across all of them — it is what
// keeps a late transfer from paying the wrong invoice.
//
// Not a use case, so it lives beside them rather than under usecases/.
package invoicecfg

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// maxTTL caps how long an invoice may stay open.
//
// The cap exists because a payment amount is held for the invoice's whole life
// plus AmountHold afterwards. A week-long invoice occupies one nonce for a
// week, and a few hundred of those exhaust the space for that base amount.
const maxTTL = 24 * time.Hour

type Config struct {
	// TTL is the default lifetime of an invoice; a request may shorten or
	// lengthen it up to maxTTL.
	TTL config.Duration `json:"ttl"`

	// AmountHold is how long a payment amount stays reserved after its invoice
	// reaches a terminal status.
	//
	// It must comfortably exceed the confirmation time of the slowest supported
	// chain: a transfer sent one second before expiry still has to land, be
	// noticed, and be recognised as belonging to that invoice rather than to
	// whichever one inherited the amount.
	AmountHold config.Duration `json:"amount_hold"`

	// ExpireInterval is how often the expirer runs. Zero switches it off, which
	// also stops amounts from ever being released.
	ExpireInterval config.Duration `json:"expire_interval"`

	// The service-wide receiving address per network. Copied onto each invoice
	// at creation, so rotating one later does not rewrite where past callers
	// were told to pay.
	PayAddressTron string `json:"pay_address_tron"`
	PayAddressBSC  string `json:"pay_address_bsc"`
}

// PayAddress returns the receiving address for a network.
func (c Config) PayAddress(n domain.Network) string {
	switch n {
	case domain.NetworkTron:
		return c.PayAddressTron
	case domain.NetworkBSC:
		return c.PayAddressBSC
	default:
		return ""
	}
}

// assetNetworks is the shape this package reads out of the assets section. Only
// the network matters here; asset_seeder owns the rest.
type assetNetwork struct {
	Network string `json:"network"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{
		TTL:            config.Duration(30 * time.Minute),
		AmountHold:     config.Duration(2 * time.Hour),
		ExpireInterval: config.Duration(time.Minute),
	}
	if err := f.Section("invoices", &cfg); err != nil {
		return cfg, err
	}

	if cfg.TTL.Std() <= 0 {
		return cfg, errors.New("config: invoices.ttl must be positive")
	}
	if cfg.TTL.Std() > maxTTL {
		return cfg, fmt.Errorf("config: invoices.ttl is %s, above the %s cap; "+
			"a long-lived invoice holds its payment amount for its whole life", cfg.TTL, maxTTL)
	}
	if cfg.AmountHold.Std() <= 0 {
		return cfg, errors.New("config: invoices.amount_hold must be positive; " +
			"without a hold, a late transfer would be credited to whichever invoice " +
			"inherited its amount")
	}

	// Cross-checked against the assets section rather than demanded
	// unconditionally: a deployment that only takes TRON should not have to
	// invent a BSC address. Checked at startup rather than at the first
	// request, because an invoice issued with no receiving address is useless
	// and the merchant's first payment is the worst place to find out.
	var assets []assetNetwork
	if err := f.Section("assets", &assets); err != nil {
		return cfg, err
	}

	for _, a := range assets {
		network, err := domain.ParseNetwork(a.Network)
		if err != nil {
			continue // asset_seeder reports this properly
		}
		if strings.TrimSpace(cfg.PayAddress(network)) == "" {
			return cfg, fmt.Errorf("config: invoices.pay_address_%s is required "+
				"because the assets section configures a %s token", network, network)
		}
	}

	return cfg, nil
}

// ResolveTTL returns the lifetime for one invoice: the requested value when
// given, the configured default otherwise, clamped to the cap either way.
func (c Config) ResolveTTL(requested time.Duration) (time.Duration, error) {
	if requested == 0 {
		return c.TTL.Std(), nil
	}
	if requested < 0 {
		return 0, fmt.Errorf("%w: expires_in must be positive", domain.ErrInvalidInput)
	}
	if requested > maxTTL {
		return 0, fmt.Errorf("%w: expires_in is above the %s cap", domain.ErrInvalidInput, maxTTL)
	}
	return requested, nil
}
