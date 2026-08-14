package cron

import (
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

type Config struct {
	// ShutdownTimeout bounds how long Stop waits for in-flight runs before
	// giving up on them.
	ShutdownTimeout config.Duration `json:"shutdown_timeout"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{ShutdownTimeout: config.Duration(15 * time.Second)}
	return cfg, f.Section("cron", &cfg)
}
