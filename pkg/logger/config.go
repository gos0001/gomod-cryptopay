package logger

import (
	"fmt"

	"go.uber.org/zap/zapcore"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

type Config struct {
	// Level is one of debug, info, warn, error.
	Level string `json:"level"`
	// Format is json or console. json for anything that ships logs somewhere,
	// console for a terminal.
	Format string `json:"format"`
}

const (
	FormatJSON    = "json"
	FormatConsole = "console"
)

// LoadConfig defaults to the production shape — info level, JSON output — so an
// operator who says nothing gets logs a collector can read, rather than colour
// escapes. The development configuration file overrides both.
func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{Level: "info", Format: FormatJSON}
	if err := f.Section("log", &cfg); err != nil {
		return cfg, err
	}

	if _, err := zapcore.ParseLevel(cfg.Level); err != nil {
		return cfg, fmt.Errorf("config: log.level %q is not one of debug, info, warn, error", cfg.Level)
	}
	if cfg.Format != FormatJSON && cfg.Format != FormatConsole {
		return cfg, fmt.Errorf("config: log.format %q is not %q or %q",
			cfg.Format, FormatJSON, FormatConsole)
	}
	return cfg, nil
}
