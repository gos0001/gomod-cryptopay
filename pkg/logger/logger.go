package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New builds the logger from an explicit level and format.
//
// Level and format are separate settings rather than derived from an
// environment name, because they are independent: a staging deployment wants
// debug logging in JSON, and neither "development" nor "production" says that.
func New(cfg Config) (*zap.SugaredLogger, error) {
	level, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		return nil, fmt.Errorf("logger: level %q: %w", cfg.Level, err)
	}

	var zc zap.Config
	switch cfg.Format {
	case FormatConsole:
		zc = zap.NewDevelopmentConfig()
	default:
		zc = zap.NewProductionConfig()
	}
	zc.Level = zap.NewAtomicLevelAt(level)

	l, err := zc.Build()
	if err != nil {
		return nil, fmt.Errorf("logger: build: %w", err)
	}
	return l.Sugar(), nil
}
