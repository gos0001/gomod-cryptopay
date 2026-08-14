package tron_watcher

import (
	"errors"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// batchSize bounds one settle pass. Discovery is bounded by the API's own page
// limit of 200.
const batchSize = 200

type Config struct {
	// WatchInterval is the poll period. Zero switches the whole network off —
	// the cron orchestrator's own off switch, and the only one there is.
	//
	// Five seconds costs two requests per tick, about 35% of a 100k daily quota.
	// See docs/chain-apis.md for the arithmetic.
	WatchInterval config.Duration `json:"watch_interval"`

	// StaleAfter is how long a payment may sit unconfirmed before it is worth
	// complaining about.
	//
	// TRON offers no signal for a transfer that was un-mined — unlike an EVM log,
	// which carries `removed`. A payment that never crosses the solidified head
	// is the only trace such a thing leaves, and the finality window is 57
	// seconds, so anything still waiting after minutes is worth an operator's
	// attention.
	StaleAfter config.Duration `json:"stale_after"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{
		WatchInterval: config.Duration(5 * time.Second),
		StaleAfter:    config.Duration(5 * time.Minute),
	}
	if err := f.Section("tron", &cfg); err != nil {
		return cfg, err
	}

	if cfg.WatchInterval.Std() < 0 {
		return cfg, errors.New("config: tron.watch_interval must not be negative " +
			"(zero switches the network off)")
	}
	if cfg.StaleAfter.Std() <= 0 {
		return cfg, errors.New("config: tron.stale_after must be positive")
	}

	return cfg, nil
}
