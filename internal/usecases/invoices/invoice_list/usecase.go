// Package invoice_list returns a filtered, cursor-paginated page of invoices.
package invoice_list

import (
	"context"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/view"
)

type Postgres interface {
	ListInvoices(ctx context.Context, f postgresadapter.ListInvoicesFilter) ([]domain.Invoice, error)
	ListAssets(ctx context.Context, enabledOnly bool) ([]domain.Asset, error)
}

type Usecase struct {
	postgres Postgres
}

func New(pg *postgresadapter.Adapter) *Usecase {
	return &Usecase{postgres: pg}
}

func (uc *Usecase) Execute(ctx context.Context, in Input) (Output, error) {
	// One more row than asked for, to learn whether a next page exists without
	// a second query. A short page is not the signal — a page can be exactly
	// full and still be the last one.
	filter := postgresadapter.ListInvoicesFilter{
		Status:          domain.InvoiceStatus(in.Status),
		AssetID:         in.AssetID,
		Network:         domain.Network(in.Network),
		ExternalID:      in.ExternalID,
		CreatedFrom:     in.CreatedFrom,
		CreatedTo:       in.CreatedTo,
		CursorCreatedAt: in.decoded.CreatedAt,
		CursorID:        in.decoded.ID,
		PageSize:        in.Limit + 1,
	}

	invoices, err := uc.postgres.ListInvoices(ctx, filter)
	if err != nil {
		return Output{}, err
	}

	var next string
	if len(invoices) > int(in.Limit) {
		last := invoices[in.Limit-1]
		next = cursor{CreatedAt: last.CreatedAt, ID: last.ID}.encode()
		invoices = invoices[:in.Limit]
	}

	// Assets are configuration and there are a handful of them, so one read
	// covers the whole page. Fetching per invoice would be a query per row for
	// data that is the same across most of them.
	//
	// Disabled ones are included: an invoice issued before a token was switched
	// off still has to render, and it needs that token's decimals to do it.
	assets, err := uc.postgres.ListAssets(ctx, false)
	if err != nil {
		return Output{}, err
	}
	byID := make(map[int64]domain.Asset, len(assets))
	for _, a := range assets {
		byID[a.ID] = a
	}

	items := make([]view.Invoice, 0, len(invoices))
	for _, inv := range invoices {
		items = append(items, view.NewInvoice(inv, byID[inv.AssetID]))
	}

	return Output{Invoices: items, NextCursor: next}, nil
}
