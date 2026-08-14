package schema_ensure

import "context"

// Bootstrap adapts the use case to the bootstrap orchestrator's Task interface.
// No config gate: a service that cannot guarantee its tables has nothing to
// serve, so this task is never optional. DB_AUTO_SCHEMA already covers the
// "someone else owns the schema" case, one layer down.
type Bootstrap struct {
	uc *Usecase
}

func NewBootstrap(uc *Usecase) *Bootstrap { return &Bootstrap{uc: uc} }

func (b *Bootstrap) Name() string { return "schema_ensure" }

func (b *Bootstrap) BootstrapRun(ctx context.Context) error {
	_, err := b.uc.Execute(ctx, Input{})
	return err
}
