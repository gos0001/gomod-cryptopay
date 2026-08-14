package bsc_watcher

import (
	"errors"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// batchSize bounds one settle pass.
const batchSize = 500

// maxChunksPerTick bounds catch-up work so a long backlog is worked through over
// several ticks rather than in one call that never returns.
//
// At the measured 0.45 s per block and a 2000-block chunk, ten chunks is roughly
// two and a half hours of chain per tick — enough to catch up quickly, bounded
// enough that the tick still ends.
const maxChunksPerTick = 10

type Config struct {
	// WatchInterval is the poll period. Zero switches the whole network off.
	WatchInterval config.Duration `json:"watch_interval"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{WatchInterval: config.Duration(5 * time.Second)}
	if err := f.Section("bsc", &cfg); err != nil {
		return cfg, err
	}

	if cfg.WatchInterval.Std() < 0 {
		return cfg, errors.New("config: bsc.watch_interval must not be negative " +
			"(zero switches the network off)")
	}
	return cfg, nil
}
