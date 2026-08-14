package main

import (
	"fmt"
	"os"

	"github.com/gos0001/gomod-cryptopay/internal/invoicecfg"
	"github.com/gos0001/gomod-cryptopay/internal/middleware"
	"github.com/gos0001/gomod-cryptopay/internal/orchestrator/bootstrap"
	"github.com/gos0001/gomod-cryptopay/internal/orchestrator/cron"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/assets/asset_seeder"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/chain/bsc_watcher"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/chain/tron_watcher"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/notify/webhook_dispatcher"
	"github.com/gos0001/gomod-cryptopay/pkg/config"
	"github.com/gos0001/gomod-cryptopay/pkg/dbschema"
	"github.com/gos0001/gomod-cryptopay/pkg/evm"
	"github.com/gos0001/gomod-cryptopay/pkg/logger"
	pkgpostgres "github.com/gos0001/gomod-cryptopay/pkg/postgres"
	"github.com/gos0001/gomod-cryptopay/pkg/tron"
	"github.com/gos0001/gomod-cryptopay/pkg/webhook"
)

// checkConfigFile validates the configuration and exits, without starting
// anything. Returns a process exit code.
//
// It calls every package's LoadConfig by hand rather than building the wire
// graph. Going through InitializeApp would be less code, but constructing the
// graph also opens the database — and a configuration check that cannot run
// without a reachable Postgres is useless in CI, which is most of the point.
//
// The cost is this list, which has to grow when a package gains configuration. A
// missing entry does not break the service, only weakens the check.
func checkConfigFile(path config.Path) int {
	file, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	loaders := []struct {
		name string
		load func(*config.File) error
	}{
		{"app", func(f *config.File) error { _, err := LoadConfig(f); return err }},
		{"log", func(f *config.File) error { _, err := logger.LoadConfig(f); return err }},
		{"postgres", func(f *config.File) error { _, err := pkgpostgres.LoadConfig(f); return err }},
		{"postgres.auto_schema", func(f *config.File) error { _, err := dbschema.LoadConfig(f); return err }},
		{"api", func(f *config.File) error { _, err := middleware.LoadConfig(f); return err }},
		{"cors", func(f *config.File) error { _, err := middleware.LoadCORSConfig(f); return err }},
		{"public_api", func(f *config.File) error { _, err := middleware.LoadPublicConfig(f); return err }},
		{"bootstrap", func(f *config.File) error { _, err := bootstrap.LoadConfig(f); return err }},
		{"cron", func(f *config.File) error { _, err := cron.LoadConfig(f); return err }},
		{"assets", func(f *config.File) error { _, err := asset_seeder.LoadConfig(f); return err }},
		// After assets: it cross-checks that every configured network has a
		// receiving address, so the clearer error surfaces first.
		{"invoices", func(f *config.File) error { _, err := invoicecfg.LoadConfig(f); return err }},
		{"tron", func(f *config.File) error { _, err := tron.LoadConfig(f); return err }},
		{"bsc", func(f *config.File) error { _, err := evm.LoadConfig(f); return err }},
		// The watchers read the same two sections for their own fields; without
		// these entries watch_interval and stale_after would be reported unknown.
		{"tron.watch", func(f *config.File) error { _, err := tron_watcher.LoadConfig(f); return err }},
		{"bsc.watch", func(f *config.File) error { _, err := bsc_watcher.LoadConfig(f); return err }},
		{"webhook", func(f *config.File) error { _, err := webhook.LoadConfig(f); return err }},
		{"webhook.delivery", func(f *config.File) error { _, err := webhook_dispatcher.LoadConfig(f); return err }},
	}

	failed := false
	for _, l := range loaders {
		if err := l.load(file); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			failed = true
		}
	}

	// Warnings are printed even on failure: an unknown key is often the reason a
	// required value looks missing.
	for _, w := range file.Warnings() {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	if failed {
		return 1
	}

	fmt.Printf("%s is valid\n", path)
	return 0
}
