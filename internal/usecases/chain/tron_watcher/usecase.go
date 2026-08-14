// Package tron_watcher polls TronGrid for incoming transfers and settles the
// payments it has already recorded.
//
// A tick costs exactly two requests regardless of how many transfers arrived:
// one page of the transfer feed, and one solidified head. That is the whole
// reason the design survives a 100k daily quota, and it works because the feed's
// records carry a block timestamp and finality is a comparison against the
// solidified head's — no per-transfer lookup, which is what an earlier design
// assumed would be needed.
package tron_watcher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/invoicecfg"
	"github.com/gos0001/gomod-cryptopay/internal/service/matching"
	"github.com/gos0001/gomod-cryptopay/pkg/tron"
)

// Chain is the slice of the TronGrid client this watcher needs.
type Chain interface {
	TRC20Transfers(ctx context.Context, q tron.TransfersQuery) (tron.TransfersPage, error)
	SolidifiedBlock(ctx context.Context) (tron.Block, error)
}

// Matcher is the engine that decides what a transfer means.
type Matcher interface {
	Apply(ctx context.Context, in matching.Observed) (matching.Result, error)
	Settle(ctx context.Context, payment domain.Payment) (matching.Result, error)
}

type Postgres interface {
	GetChainCursor(ctx context.Context, network domain.Network) (domain.ChainCursor, error)
	SaveChainCursor(ctx context.Context, network domain.Network, lastBlock int64, lastTimestamp time.Time) (domain.ChainCursor, error)
	ListPaymentsAwaitingConfirmation(ctx context.Context, network domain.Network, batchSize int32) ([]domain.Payment, error)
}

type Usecase struct {
	chain    Chain
	matcher  Matcher
	postgres Postgres
	cfg      Config
	invoices invoicecfg.Config
	logger   *zap.SugaredLogger
	now      func() time.Time
}

func New(
	client *tron.Client,
	matcher *matching.Service,
	pg *postgresadapter.Adapter,
	cfg Config,
	invoices invoicecfg.Config,
	logger *zap.SugaredLogger,
) *Usecase {
	return &Usecase{
		chain: client, matcher: matcher, postgres: pg,
		cfg: cfg, invoices: invoices, logger: logger, now: time.Now,
	}
}

type Input struct{}

type Output struct {
	Discovered int
	Settled    int
	Stale      int
}

// Execute runs one tick: discover, then settle.
//
// Both halves matter, and the second is the one that is easy to leave out. A
// transfer first seen before it was final will not appear in the feed again once
// the cursor has moved past it, so without the settle pass those invoices would
// stay in detected forever.
func (uc *Usecase) Execute(ctx context.Context, _ Input) (Output, error) {
	address := uc.invoices.PayAddress(domain.NetworkTron)
	if address == "" {
		return Output{}, errors.New("tron_watcher: no receiving address configured for tron")
	}

	// One request. The finality line is fetched first so that discovery can
	// classify what it finds in the same tick.
	solidified, err := uc.chain.SolidifiedBlock(ctx)
	if err != nil {
		return Output{}, fmt.Errorf("tron_watcher: solidified head: %w", err)
	}

	discovered, err := uc.discover(ctx, address, solidified.Time)
	if err != nil {
		return Output{}, err
	}

	settled, stale, err := uc.settle(ctx, solidified.Time)
	if err != nil {
		return Output{}, err
	}

	out := Output{Discovered: discovered, Settled: settled, Stale: stale}
	if discovered > 0 || settled > 0 {
		uc.logger.Infow("tron tick", "discovered", discovered, "settled", settled,
			"solidified_block", solidified.Number)
	}
	return out, nil
}

// discover reads the feed from the cursor and hands each transfer to the matcher.
func (uc *Usecase) discover(ctx context.Context, address string, finalityLine time.Time) (int, error) {
	cursor, err := uc.postgres.GetChainCursor(ctx, domain.NetworkTron)
	if err != nil {
		return 0, fmt.Errorf("tron_watcher: read cursor: %w", err)
	}

	page, err := uc.chain.TRC20Transfers(ctx, tron.TransfersQuery{
		Address:      address,
		MinTimestamp: cursor.LastTimestamp,
		Limit:        batchSize,
	})
	if err != nil {
		return 0, fmt.Errorf("tron_watcher: read transfers: %w", err)
	}

	newest := cursor.LastTimestamp
	for _, t := range page.Transfers {
		_, err := uc.matcher.Apply(ctx, matching.Observed{
			Network:  domain.NetworkTron,
			TxHash:   t.TxID,
			LogIndex: 0, // one record is one transfer on TRON
			Contract: t.ContractAddress,
			From:     t.From,
			To:       t.To,
			Value:    t.Value,
			// No block number is available; BlockTime is the position.
			BlockTime: t.BlockTime,
			Final:     !t.BlockTime.After(finalityLine),
		})
		if err != nil {
			// Stop here rather than skipping: the cursor must not advance past a
			// transfer that was not handled, or the money is lost silently.
			return 0, fmt.Errorf("tron_watcher: apply %s: %w", t.TxID, err)
		}

		if t.BlockTime.After(newest) {
			newest = t.BlockTime
		}
	}

	// Saved after processing, never before: a crash in between would otherwise
	// skip everything in this page.
	//
	// The cursor moves to the newest timestamp seen and the next query uses it
	// inclusively. Several transfers can share one block timestamp, so advancing
	// past it would drop a sibling in the same block; re-reading a couple of
	// records instead is free, because the unique index makes it a no-op.
	if newest.After(cursor.LastTimestamp) {
		if _, err := uc.postgres.SaveChainCursor(ctx, domain.NetworkTron, 0, newest); err != nil {
			return 0, fmt.Errorf("tron_watcher: save cursor: %w", err)
		}
	}

	return len(page.Transfers), nil
}

// settle promotes payments whose transfer has crossed the finality line.
func (uc *Usecase) settle(ctx context.Context, finalityLine time.Time) (settled, stale int, err error) {
	pending, err := uc.postgres.ListPaymentsAwaitingConfirmation(ctx, domain.NetworkTron, batchSize)
	if err != nil {
		return 0, 0, fmt.Errorf("tron_watcher: list unsettled payments: %w", err)
	}

	staleBefore := uc.now().Add(-uc.cfg.StaleAfter.Std())

	for _, p := range pending {
		if p.BlockTime.After(finalityLine) {
			// Still inside the reversible window. Worth a warning once it has
			// been there far longer than the window itself: TRON gives no signal
			// for a transfer that was un-mined, and a payment that never
			// solidifies is the only trace it leaves.
			if p.BlockTime.Before(staleBefore) {
				stale++
				uc.logger.Warnw("a TRON payment has not solidified long past the finality window; "+
					"it may have been un-mined, which TRON does not report",
					"tx", p.TxHash, "block_time", p.BlockTime, "invoice", p.InvoiceID)
			}
			continue
		}

		// Settle rather than Apply: the stored payment already knows its asset
		// and its invoice, and it carries no contract address for Apply to
		// resolve from.
		if _, err := uc.matcher.Settle(ctx, p); err != nil {
			return settled, stale, fmt.Errorf("tron_watcher: settle %s: %w", p.TxHash, err)
		}
		settled++
	}

	return settled, stale, nil
}
