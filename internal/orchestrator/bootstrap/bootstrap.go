// The Run policy below is yours to tune. The marker comments are not: the CLI
// splices at them, and deleting one silently stops the next generator from
// registering anything here.
package bootstrap

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/gos0001/gomod-cryptopay/internal/usecases/assets/asset_seeder"
	"github.com/gos0001/gomod-cryptopay/internal/usecases/sys/schema_ensure"
	// gostack:imports
)

// Task is one startup step. A use case that runs at boot implements this from
// its own bootstrap.go — one handler file per caller, named after the caller.
type Task interface {
	Name() string
	BootstrapRun(ctx context.Context) error
}

// Bootstrap sequences startup tasks before the HTTP listener opens. Sequencing
// only: the work lives in the use cases, the same way routing lives in the
// controller and the logic behind it does not.
type Bootstrap struct {
	logger *zap.SugaredLogger
	cfg    Config
	tasks  []Task
}

func New(
	logger *zap.SugaredLogger,
	cfg Config,
	schemaEnsure *schema_ensure.Bootstrap,
	assetSeeder *asset_seeder.Bootstrap,
	// gostack:params
) *Bootstrap {
	b := &Bootstrap{logger: logger, cfg: cfg}
	// Order matters and this one is first: every later task queries tables that
	// only exist once this has run.
	b.tasks = append(b.tasks, schemaEnsure)
	b.tasks = append(b.tasks, assetSeeder)
	// gostack:routes

	return b
}

// Run executes every task in registration order. The first failure aborts the
// boot: serving traffic against a half-prepared system is worse than not
// starting at all.
func (b *Bootstrap) Run(ctx context.Context) error {
	if len(b.tasks) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, b.cfg.Timeout.Std())
	defer cancel()

	for _, task := range b.tasks {
		b.logger.Infow("bootstrap task", "task", task.Name())
		if err := task.BootstrapRun(ctx); err != nil {
			return fmt.Errorf("bootstrap %s: %w", task.Name(), err)
		}
	}

	b.logger.Infow("bootstrap complete", "tasks", len(b.tasks))
	return nil
}
