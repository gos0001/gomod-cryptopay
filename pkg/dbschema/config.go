package dbschema

import "github.com/gos0001/gomod-cryptopay/pkg/config"

type Config struct {
	// AutoSchema creates missing tables at startup. On by default so that
	// starting against an empty database just works — which is the point of
	// shipping an image. Set false where a deployment pipeline owns schema
	// changes; the service then refuses to serve against absent tables rather
	// than creating them itself.
	AutoSchema bool `json:"auto_schema"`
}

// LoadConfig reads from the postgres section, which pkg/postgres also consumes.
// Both are about the same database, and splitting them into two sections would
// make an operator hunt for where the schema switch lives.
func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{AutoSchema: true}
	return cfg, f.Section("postgres", &cfg)
}
