package postgres

import (
	"errors"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

type Config struct {
	DSN string `json:"dsn"`

	// AutoCreate creates the database named in the DSN when connecting shows it
	// is missing. Only that path needs the CREATEDB privilege, and it is never
	// taken when the database already exists.
	AutoCreate bool `json:"auto_create"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{AutoCreate: true}
	if err := f.Section("postgres", &cfg); err != nil {
		return cfg, err
	}

	if cfg.DSN == "" {
		return cfg, errors.New("config: postgres.dsn is required")
	}
	return cfg, nil
}
