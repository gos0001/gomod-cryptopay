package bootstrap

import (
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

type Config struct {
	// Timeout bounds every startup task together, not each one. A boot that
	// hangs must fail rather than leave the process alive and not listening.
	Timeout config.Duration `json:"timeout"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{Timeout: config.Duration(60 * time.Second)}
	return cfg, f.Section("bootstrap", &cfg)
}
