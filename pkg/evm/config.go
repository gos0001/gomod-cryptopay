package evm

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// Defaults come from docs/chain-apis.md, which records live measurements.
const (
	// DefaultLogRange is a chunk width, not a limit of the chain. At BSC's
	// measured 0.45 s per block this is about fifteen minutes of history per
	// request — small enough that a node with a modest result cap copes, wide
	// enough that catching up does not take thousands of calls.
	DefaultLogRange = 2000

	// DefaultConfirmations is only a fallback for an endpoint that does not
	// serve the `finalized` tag. Where the tag is available it is authoritative
	// and this number is unused.
	DefaultConfirmations = 15

	// DefaultReorgDepth is how far the cursor is rewound at startup. Generous
	// against a measured finality lag of 1–3 blocks.
	DefaultReorgDepth = 64
)

type Config struct {
	// RPCURLs is rotated per request, so several endpoints spread load — and
	// therefore quota — across whatever keys they carry. Failover is the
	// secondary benefit.
	//
	// ⚠️ The bsc-dataseed.* family is unusable here: those nodes answer
	// eth_blockNumber but reject eth_getLogs at every range. Verified live.
	RPCURLs []string `json:"rpc_urls"`

	LogRange uint64 `json:"log_range"`

	// UseFinalizedTag prefers the chain's own finality signal over counting
	// confirmations.
	UseFinalizedTag bool  `json:"use_finalized_tag"`
	Confirmations   int64 `json:"confirmations"`

	ReorgDepth int64 `json:"reorg_depth"`

	Timeout config.Duration `json:"timeout"`

	// FailureCooldown is how long an endpoint is skipped after a transport
	// failure before it is tried again.
	FailureCooldown config.Duration `json:"failure_cooldown"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{
		LogRange:        DefaultLogRange,
		UseFinalizedTag: true,
		Confirmations:   DefaultConfirmations,
		ReorgDepth:      DefaultReorgDepth,
		Timeout:         config.Duration(20 * time.Second),
		FailureCooldown: config.Duration(30 * time.Second),
	}
	if err := f.Section("bsc", &cfg); err != nil {
		return cfg, err
	}

	urls := make([]string, 0, len(cfg.RPCURLs))
	for i, u := range cfg.RPCURLs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return cfg, fmt.Errorf("config: bsc.rpc_urls[%d] is not an http or https URL: %q", i, u)
		}
		urls = append(urls, u)
	}
	if len(urls) == 0 {
		return cfg, errors.New("config: bsc.rpc_urls must list at least one endpoint " +
			"(during development that is the local Hardhat node, http://localhost:8545)")
	}
	cfg.RPCURLs = urls

	if cfg.LogRange == 0 {
		return cfg, errors.New("config: bsc.log_range must be positive")
	}
	if cfg.Confirmations < 0 {
		return cfg, errors.New("config: bsc.confirmations must not be negative")
	}
	if cfg.ReorgDepth < 0 {
		return cfg, errors.New("config: bsc.reorg_depth must not be negative")
	}
	if cfg.Timeout.Std() <= 0 {
		return cfg, errors.New("config: bsc.timeout must be positive")
	}

	return cfg, nil
}
