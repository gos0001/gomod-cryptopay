// Package invoice_get returns one invoice by its id.
package invoice_get

import (
	"context"

	"github.com/google/uuid"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/view"
)

type Postgres interface {
	GetInvoiceByID(ctx context.Context, id uuid.UUID) (domain.Invoice, error)
	GetAssetByID(ctx context.Context, id int64) (domain.Asset, error)
}

type Usecase struct {
	postgres Postgres
}

func New(pg *postgresadapter.Adapter) *Usecase {
	return &Usecase{postgres: pg}
}

func (uc *Usecase) Execute(ctx context.Context, in Input) (Output, error) {
	invoice, err := uc.postgres.GetInvoiceByID(ctx, in.ID)
	if err != nil {
		return Output{}, err
	}

	// The asset is read separately rather than joined: it carries the decimals
	// the amounts are rendered with, and a disabled asset must still render the
	// invoices that reference it.
	asset, err := uc.postgres.GetAssetByID(ctx, invoice.AssetID)
	if err != nil {
		return Output{}, err
	}

	return Output{Invoice: view.NewInvoice(invoice, asset)}, nil
}
