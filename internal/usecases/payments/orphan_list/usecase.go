// Package orphan_list reports transfers that arrived and could not be matched
// to an invoice.
//
// This is a reconciliation surface, not a payment one. With amount-based
// matching, a payer who rounds their input produces a transfer nothing will
// ever claim; without somewhere to see it, that money is invisible outside of
// psql.
package orphan_list

import (
	"context"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/view"
)

type Postgres interface {
	ListOrphanTransfers(ctx context.Context, pageSize int32) ([]domain.OrphanTransfer, error)
}

type Usecase struct {
	postgres Postgres
}

func New(pg *postgresadapter.Adapter) *Usecase {
	return &Usecase{postgres: pg}
}

func (uc *Usecase) Execute(ctx context.Context, in Input) (Output, error) {
	transfers, err := uc.postgres.ListOrphanTransfers(ctx, in.Limit)
	if err != nil {
		return Output{}, err
	}

	items := make([]view.Orphan, 0, len(transfers))
	for _, t := range transfers {
		items = append(items, view.NewOrphan(t))
	}

	return Output{Orphans: items}, nil
}
