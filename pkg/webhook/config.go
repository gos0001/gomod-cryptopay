package webhook

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// minSecretLength is a floor to catch a placeholder reaching production, not a
// policy on entropy.
const minSecretLength = 16

type Config struct {
	// URL receives every event. Empty switches notifications off entirely, and
	// nothing is written to the outbox at all — a queue with no consumer only
	// grows.
	//
	// It lives in configuration rather than in the invoice-creation request on
	// purpose. This is a self-hosted module with one operator and one receiver,
	// so accepting a destination from an API caller would solve a problem that
	// does not exist while creating two that do: the service would make requests
	// to wherever a caller pointed it, and the receiver's domain would be visible
	// to whoever holds an API key.
	URL string `json:"url"`

	// Secret signs each request. Strongly recommended: without it a receiver
	// cannot tell an event from this service apart from a POST by anyone who
	// learned the URL — and webhook URLs leak, into logs, screenshots, and the
	// browser history of whoever tested it once.
	Secret string `json:"secret"`

	// APIKey is sent verbatim as X-Cryptopay-Api-Key, for receivers behind a
	// gateway that filters on a header.
	//
	// Not a substitute for Secret. A static key proves only that the sender knows
	// it: anyone who captures one request can replay it verbatim with any payload
	// they like. The signature covers the body and the timestamp, so neither is
	// possible.
	APIKey string `json:"api_key"`

	// Timeout for one delivery attempt. Kept short: a slow receiver must not hold
	// up the queue behind it.
	Timeout config.Duration `json:"timeout"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{Timeout: config.Duration(10 * time.Second)}
	if err := f.Section("webhook", &cfg); err != nil {
		return cfg, err
	}

	cfg.URL = strings.TrimSpace(cfg.URL)
	if cfg.URL != "" {
		if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
			return cfg, fmt.Errorf("config: webhook.url must be an http or https URL, got %q", cfg.URL)
		}
		// Refused rather than warned about: an unsigned webhook is
		// indistinguishable from a forgery, and the receiver has no way to notice
		// that signing was simply never switched on.
		if cfg.Secret == "" {
			return cfg, errors.New("config: webhook.secret is required when webhook.url is set; " +
				"without it a receiver cannot tell a real event from a forged one — " +
				"generate one with `openssl rand -hex 32`")
		}
		if len(cfg.Secret) < minSecretLength {
			return cfg, fmt.Errorf("config: webhook.secret is shorter than %d characters", minSecretLength)
		}
	}

	if cfg.Timeout.Std() <= 0 {
		return cfg, errors.New("config: webhook.timeout must be positive")
	}

	return cfg, nil
}

// Enabled reports whether a destination is configured at all.
func (c Config) Enabled() bool { return c.URL != "" }
