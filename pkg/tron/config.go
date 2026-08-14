package tron

import (
	"errors"
	"fmt"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// Defaults come from docs/chain-apis.md, which records live measurements rather
// than documentation.
const (
	// DefaultQPS sits below TronGrid's 15/s ceiling on purpose. Crossing it does
	// not cost one rejected request — it suspends the key for about 27 seconds,
	// so the limiter must stay under the ceiling rather than find it.
	DefaultQPS = 10

	// hardQPSCeiling is the documented and measured limit. Configuring above it
	// is refused: the only outcome would be repeated 27-second blackouts.
	hardQPSCeiling = 15

	// DefaultDailyBudget is the free tier's quota.
	DefaultDailyBudget = 100_000

	// MaxPageLimit is the API's ceiling. limit=201 answers HTTP 400 with an
	// empty body, so this is enforced client-side where the error can say why.
	MaxPageLimit = 200
)

type Config struct {
	APIURL string `json:"api_url"`
	APIKey string `json:"api_key"`

	// DailyRequestBudget is the quota per UTC day; zero means unlimited.
	DailyRequestBudget int `json:"daily_request_budget"`
	// QPS shapes the outbound rate.
	QPS float64 `json:"qps"`

	Timeout config.Duration `json:"timeout"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{
		APIURL:             "https://api.trongrid.io",
		DailyRequestBudget: DefaultDailyBudget,
		QPS:                DefaultQPS,
		Timeout:            config.Duration(15 * time.Second),
	}
	if err := f.Section("tron", &cfg); err != nil {
		return cfg, err
	}

	if cfg.APIURL == "" {
		return cfg, errors.New("config: tron.api_url must not be empty")
	}
	// The key is not required: the public endpoint answers without one. But it
	// then rate-limits dynamically and answers 403 with a 30-second penalty,
	// which is not something to discover in production.
	if cfg.QPS < 0 {
		return cfg, errors.New("config: tron.qps must not be negative")
	}
	if cfg.QPS > hardQPSCeiling {
		return cfg, fmt.Errorf("config: tron.qps is %.0f, above TronGrid's limit of %d; "+
			"exceeding it suspends the API key for about 27 seconds per breach",
			cfg.QPS, hardQPSCeiling)
	}
	if cfg.DailyRequestBudget < 0 {
		return cfg, errors.New("config: tron.daily_request_budget must not be negative")
	}
	if cfg.Timeout.Std() <= 0 {
		return cfg, errors.New("config: tron.timeout must be positive")
	}

	return cfg, nil
}

// HasAPIKey reports whether a key is configured, so a caller can warn about the
// keyless mode rather than silently running in it.
func (c Config) HasAPIKey() bool { return c.APIKey != "" }
