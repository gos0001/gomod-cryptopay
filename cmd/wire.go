//go:build wireinject

package main

import (
	"github.com/google/wire"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/controller/http_v1"
	"github.com/gos0001/gomod-cryptopay/internal/invoicecfg"
	"github.com/gos0001/gomod-cryptopay/internal/middleware"
	"github.com/gos0001/gomod-cryptopay/internal/orchestrator/bootstrap"
	"github.com/gos0001/gomod-cryptopay/internal/orchestrator/cron"
	"github.com/gos0001/gomod-cryptopay/internal/service/matching"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/assets/asset_list"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/assets/asset_seeder"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/chain/bsc_watcher"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/chain/tron_watcher"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/invoices/invoice_cancel"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/invoices/invoice_create"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/invoices/invoice_expirer"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/invoices/invoice_get"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/invoices/invoice_list"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/notify/webhook_dispatcher"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/payments/orphan_list"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/sys/schema_ensure"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/sys/sys_health"
	"github.com/gos0001/gomod-cryptopay/pkg/config"
	"github.com/gos0001/gomod-cryptopay/pkg/dbschema"
	"github.com/gos0001/gomod-cryptopay/pkg/evm"
	"github.com/gos0001/gomod-cryptopay/pkg/logger"
	pkgpostgres "github.com/gos0001/gomod-cryptopay/pkg/postgres"
	"github.com/gos0001/gomod-cryptopay/pkg/tron"
	"github.com/gos0001/gomod-cryptopay/pkg/webhook"
	"github.com/gos0001/gomod-cryptopay/schema"
	// gostack:imports
)

// InitializeApp takes the configuration path rather than reading it from the
// environment. config.Path is a named type because wire resolves by type alone,
// and a bare string would collide with every other string in the graph.
func InitializeApp(configPath config.Path) (*App, error) {
	wire.Build(
		config.Set,
		LoadConfig,
		logger.Set,
		pkgpostgres.Set,
		schema.Set,
		dbschema.Set,
		postgresadapter.Set,
		invoicecfg.Set,
		schema_ensure.Set,
		asset_seeder.Set,
		invoice_create.Set,
		invoice_get.Set,
		invoice_list.Set,
		invoice_cancel.Set,
		invoice_expirer.Set,
		// The chain clients finally enter the graph: until now nothing consumed
		// them, and wire rejects a provider set with no consumer.
		tron.Set,
		evm.Set,
		matching.Set,
		tron_watcher.Set,
		bsc_watcher.Set,
		webhook.Set,
		webhook_dispatcher.Set,
		asset_list.Set,
		orphan_list.Set,
		sys_health.Set,
		middleware.Set,
		http_v1.Set,
		bootstrap.Set,
		cron.Set,
		// gostack:providers
		NewApp,
	)
	return nil, nil
}
