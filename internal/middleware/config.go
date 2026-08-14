package middleware

import (
	"errors"
	"fmt"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// minKeyLength is a floor, not a policy. It exists to catch a placeholder like
// "changeme" reaching production, not to enforce entropy — a caller who wants a
// weak 24-character key can still have one.
const minKeyLength = 24

type Config struct {
	// Keys are accepted on X-Api-Key. A list rather than a single value so a key
	// can be rotated without a window where neither the old nor the new one
	// works: add the new key, migrate callers, drop the old one.
	Keys []string `json:"keys"`
}

// LoadConfig has no default. A service that mints payment invoices must never
// come up open to the internet because a section was missed.
func LoadConfig(f *config.File) (Config, error) {
	var cfg Config
	if err := f.Section("api", &cfg); err != nil {
		return cfg, err
	}

	if len(cfg.Keys) == 0 {
		return cfg, errors.New("config: api.keys is required and must hold at least one key")
	}

	for i, k := range cfg.Keys {
		if len(k) < minKeyLength {
			// The index, never the key itself — this error reaches the logs.
			return cfg, fmt.Errorf("config: api.keys[%d] is shorter than %d characters; "+
				"generate one with `openssl rand -hex 32`", i, minKeyLength)
		}
	}

	return cfg, nil
}
