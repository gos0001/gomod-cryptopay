// Package asset_seeder writes the configured token list into storage at
// startup.
//
// Assets are configuration, not data: the file is the source of truth and the
// table is a projection of it, refreshed on every boot. That is what makes
// supporting a new token a config line rather than a release.
package asset_seeder

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
)

// Postgres is the slice of the adapter this use case needs.
type Postgres interface {
	UpsertAsset(ctx context.Context, in domain.Asset) (domain.Asset, error)
	DisableAssetsNotIn(ctx context.Context, keepIDs []int64) (int64, error)
}

type Usecase struct {
	postgres Postgres
	cfg      Config
	logger   *zap.SugaredLogger
}

// New takes the concrete adapter because wire resolves concrete types, not
// interfaces.
func New(pg *postgresadapter.Adapter, cfg Config, logger *zap.SugaredLogger) *Usecase {
	return &Usecase{postgres: pg, cfg: cfg, logger: logger}
}

type Input struct{}

type Output struct {
	Seeded   int
	Disabled int64
}

// Execute upserts every configured asset, then disables the ones that are no
// longer configured.
//
// Disabled, never deleted: invoices carry an asset_id, and dropping the row
// would take their history with it. A disabled asset stops accepting new
// invoices while the old ones stay readable.
func (uc *Usecase) Execute(ctx context.Context, _ Input) (Output, error) {
	keep := make([]int64, 0, len(uc.cfg.Assets))

	for _, want := range uc.cfg.Assets {
		stored, err := uc.postgres.UpsertAsset(ctx, want)
		if err != nil {
			return Output{}, fmt.Errorf("upsert %s on %s: %w", want.Symbol, want.Network, err)
		}
		keep = append(keep, stored.ID)
	}

	disabled, err := uc.postgres.DisableAssetsNotIn(ctx, keep)
	if err != nil {
		return Output{}, fmt.Errorf("disable removed assets: %w", err)
	}

	if disabled > 0 {
		// Worth a warning rather than an info line: an asset vanishing from the
		// configuration is usually intended, but if it was a typo the operator
		// has just stopped accepting a token they still sell in.
		uc.logger.Warnw("assets disabled because they are no longer configured", "count", disabled)
	}

	return Output{Seeded: len(keep), Disabled: disabled}, nil
}
