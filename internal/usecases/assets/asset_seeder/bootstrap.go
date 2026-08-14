package asset_seeder

import (
	"context"

	"go.uber.org/zap"
)

// Bootstrap adapts the use case to the bootstrap orchestrator's Task interface.
//
// No enable flag: a service with no assets cannot create a single invoice, so
// there is no useful deployment in which this is switched off. Which assets
// exist is the configuration decision; whether to have any is not.
type Bootstrap struct {
	uc     *Usecase
	logger *zap.SugaredLogger
}

func NewBootstrap(uc *Usecase, logger *zap.SugaredLogger) *Bootstrap {
	return &Bootstrap{uc: uc, logger: logger}
}

func (b *Bootstrap) Name() string { return "asset_seeder" }

func (b *Bootstrap) BootstrapRun(ctx context.Context) error {
	out, err := b.uc.Execute(ctx, Input{})
	if err != nil {
		return err
	}

	b.logger.Infow("assets seeded", "count", out.Seeded)
	return nil
}
