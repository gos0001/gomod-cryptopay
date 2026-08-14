// Package invoice_expirer closes overdue invoices and frees the payment amounts
// whose hold has run out.
package invoice_expirer

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/invoicecfg"
)

// batchSize bounds one pass. A backlog is worked through over several ticks
// rather than in one long transaction: a thousand overdue invoices must not
// hold the table while the next tick waits.
const batchSize = 500

type Postgres interface {
	ExpirePendingInvoices(ctx context.Context, hold time.Duration, batchSize int32) ([]domain.Invoice, error)
	ReleaseExpiredAmountHolds(ctx context.Context) (int64, error)
}

type Usecase struct {
	postgres Postgres
	cfg      invoicecfg.Config
	logger   *zap.SugaredLogger
}

func New(pg *postgresadapter.Adapter, cfg invoicecfg.Config, logger *zap.SugaredLogger) *Usecase {
	return &Usecase{postgres: pg, cfg: cfg, logger: logger}
}

type Input struct{}

type Output struct {
	Expired  int
	Released int64
}

// Execute does two separate things, and the second is the one that is easy to
// forget.
//
// Expiring an invoice does not free its payment amount — the hold deliberately
// outlives the invoice, so a transfer sent just before the deadline cannot pay
// whoever inherits the amount. Releasing holds is therefore its own sweep, and
// without it no amount is ever reused: the uniqueness index is built on a
// boolean column precisely because Postgres will not index a predicate
// containing now().
//
// Both run every tick, and the second runs even if the first found nothing:
// there are always holds from invoices that expired long ago.
func (uc *Usecase) Execute(ctx context.Context, _ Input) (Output, error) {
	expired, err := uc.postgres.ExpirePendingInvoices(ctx, uc.cfg.AmountHold.Std(), batchSize)
	if err != nil {
		return Output{}, fmt.Errorf("expire overdue invoices: %w", err)
	}

	released, err := uc.postgres.ReleaseExpiredAmountHolds(ctx)
	if err != nil {
		return Output{}, fmt.Errorf("release expired amount holds: %w", err)
	}

	if len(expired) > 0 || released > 0 {
		uc.logger.Infow("invoice sweep", "expired", len(expired), "amounts_released", released)
	}

	return Output{Expired: len(expired), Released: released}, nil
}
