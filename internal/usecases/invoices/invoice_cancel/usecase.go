// Package invoice_cancel withdraws a pending invoice.
package invoice_cancel

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/invoicecfg"
	"github.com/gos0001/gomod-cryptopay/internal/view"
)

type Postgres interface {
	CancelInvoice(ctx context.Context, id uuid.UUID, holdUntil time.Time) (domain.Invoice, error)
	GetInvoiceByID(ctx context.Context, id uuid.UUID) (domain.Invoice, error)
	GetAssetByID(ctx context.Context, id int64) (domain.Asset, error)
}

type Usecase struct {
	postgres Postgres
	cfg      invoicecfg.Config
	now      func() time.Time
}

func New(pg *postgresadapter.Adapter, cfg invoicecfg.Config) *Usecase {
	return &Usecase{postgres: pg, cfg: cfg, now: time.Now}
}

// Execute cancels the invoice if it is still pending.
//
// The hold on its payment amount is extended rather than dropped: a transfer
// already in flight when the merchant cancelled still has to be recognised as
// belonging to this invoice, and filed as an orphan against it — not credited
// to whichever invoice would otherwise inherit the amount.
func (uc *Usecase) Execute(ctx context.Context, in Input) (Output, error) {
	holdUntil := uc.now().Add(uc.cfg.AmountHold.Std())

	invoice, err := uc.postgres.CancelInvoice(ctx, in.ID, holdUntil)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidTransition) {
			// The compare-and-set matched nothing, which means either the
			// invoice does not exist or it is no longer pending. Only a second
			// read can tell those apart, and they are different answers: 404
			// versus 409.
			return Output{}, uc.explainRefusal(ctx, in.ID)
		}
		return Output{}, err
	}

	asset, err := uc.postgres.GetAssetByID(ctx, invoice.AssetID)
	if err != nil {
		return Output{}, err
	}

	return Output{Invoice: view.NewInvoice(invoice, asset)}, nil
}

func (uc *Usecase) explainRefusal(ctx context.Context, id uuid.UUID) error {
	if _, err := uc.postgres.GetInvoiceByID(ctx, id); err != nil {
		return err // ErrInvoiceNotFound, or something worse
	}
	return domain.ErrInvalidTransition
}
