// Package asset_list reports which tokens this deployment accepts.
package asset_list

import (
	"context"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/view"
)

type Postgres interface {
	ListAssets(ctx context.Context, enabledOnly bool) ([]domain.Asset, error)
}

type Usecase struct {
	postgres Postgres
}

func New(pg *postgresadapter.Adapter) *Usecase {
	return &Usecase{postgres: pg}
}

// Execute returns the enabled assets only.
//
// A disabled asset cannot back a new invoice, so listing it would just invite a
// 404 on the next call. Old invoices still render it, but that comes from the
// invoice endpoints, not from here.
func (uc *Usecase) Execute(ctx context.Context, _ Input) (Output, error) {
	assets, err := uc.postgres.ListAssets(ctx, true)
	if err != nil {
		return Output{}, err
	}

	items := make([]view.Asset, 0, len(assets))
	for _, a := range assets {
		items = append(items, view.NewAsset(a))
	}

	return Output{Assets: items}, nil
}
