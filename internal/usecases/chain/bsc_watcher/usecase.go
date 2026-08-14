// Package bsc_watcher scans BSC for incoming token transfers and settles the
// payments it has recorded.
//
// Unlike TRON, an EVM log carries a `removed` flag, so a transfer withdrawn by a
// reorg announces itself and the invoice can be handed back. That is the branch
// this watcher exists to get right; everything else is bookkeeping around it.
package bsc_watcher

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
	"github.com/gos0001/gomod-cryptopay/pkg/evm"
)

// Chain is the slice of the RPC client this watcher needs.
type Chain interface {
	BlockNumber(ctx context.Context) (uint64, error)
	FinalizedBlockNumber(ctx context.Context) (uint64, error)
	GetLogs(ctx context.Context, q evm.LogQuery) ([]evm.Log, error)
	LogRange() uint64
	Confirmations() int64
	ReorgDepth() int64
	UseFinalizedTag() bool
}

type Matcher interface {
	Apply(ctx context.Context, in matching.Observed) (matching.Result, error)
	Settle(ctx context.Context, payment domain.Payment) (matching.Result, error)
	Revoke(ctx context.Context, network domain.Network, txHash string, logIndex int32) error
}

type Postgres interface {
	ListEnabledAssetsByNetwork(ctx context.Context, network domain.Network) ([]domain.Asset, error)
	GetChainCursor(ctx context.Context, network domain.Network) (domain.ChainCursor, error)
	SaveChainCursor(ctx context.Context, network domain.Network, lastBlock int64, lastTimestamp time.Time) (domain.ChainCursor, error)
	RewindChainCursor(ctx context.Context, network domain.Network, depth int64) (domain.ChainCursor, error)
	ListPaymentsAwaitingConfirmation(ctx context.Context, network domain.Network, batchSize int32) ([]domain.Payment, error)
	ListLivePaymentsInBlockRange(ctx context.Context, network domain.Network, fromBlock, toBlock int64) ([]domain.Payment, error)
}

type Usecase struct {
	chain    Chain
	matcher  Matcher
	postgres Postgres
	cfg      Config
	invoices invoicecfg.Config
	logger   *zap.SugaredLogger

	// rewound guards the startup rollback so it happens once per process, not
	// once per tick — otherwise the cursor would walk backwards forever.
	rewound bool

	now func() time.Time
}

func New(
	client *evm.Client,
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
	Revoked    int
	Settled    int
	ToBlock    uint64
}

func (uc *Usecase) Execute(ctx context.Context, _ Input) (Output, error) {
	address := uc.invoices.PayAddress(domain.NetworkBSC)
	if address == "" {
		return Output{}, errors.New("bsc_watcher: no receiving address configured for bsc")
	}

	recipient, err := evm.PadTopic(address)
	if err != nil {
		return Output{}, fmt.Errorf("bsc_watcher: receiving address: %w", err)
	}

	assets, err := uc.postgres.ListEnabledAssetsByNetwork(ctx, domain.NetworkBSC)
	if err != nil {
		return Output{}, fmt.Errorf("bsc_watcher: list assets: %w", err)
	}
	if len(assets) == 0 {
		// Nothing configured to watch for. Not an error: an operator may run
		// TRON only.
		return Output{}, nil
	}
	contracts := make([]string, 0, len(assets))
	for _, a := range assets {
		contracts = append(contracts, a.ContractAddress)
	}

	head, err := uc.chain.BlockNumber(ctx)
	if err != nil {
		return Output{}, fmt.Errorf("bsc_watcher: head: %w", err)
	}

	finalityLine, err := uc.finalityLine(ctx, head)
	if err != nil {
		return Output{}, err
	}

	if err := uc.rewindOnce(ctx); err != nil {
		return Output{}, err
	}

	out, err := uc.scan(ctx, contracts, recipient, head, finalityLine)
	if err != nil {
		return out, err
	}

	settled, err := uc.settle(ctx, finalityLine)
	if err != nil {
		return out, err
	}
	out.Settled = settled

	if out.Discovered > 0 || out.Revoked > 0 || out.Settled > 0 {
		uc.logger.Infow("bsc tick", "discovered", out.Discovered, "revoked", out.Revoked,
			"settled", out.Settled, "to_block", out.ToBlock, "finalized", finalityLine)
	}
	return out, nil
}

// finalityLine is the highest block considered irreversible.
//
// The chain's own `finalized` tag is preferred: it is authoritative, one cheap
// call, and its lag was measured at one to three blocks. Counting confirmations
// is the fallback for an endpoint that does not serve the tag.
func (uc *Usecase) finalityLine(ctx context.Context, head uint64) (uint64, error) {
	if uc.chain.UseFinalizedTag() {
		finalized, err := uc.chain.FinalizedBlockNumber(ctx)
		if err == nil {
			return finalized, nil
		}
		if !errors.Is(err, evm.ErrFinalizedTagUnsupported) {
			return 0, fmt.Errorf("bsc_watcher: finalized head: %w", err)
		}
		uc.logger.Warnw("endpoint does not serve the finalized tag; "+
			"falling back to counting confirmations", "confirmations", uc.chain.Confirmations())
	}

	depth := uint64(uc.chain.Confirmations())
	if head < depth {
		return 0, nil
	}
	return head - depth, nil
}

// rewindOnce backs the cursor up at startup so blocks that were only shallowly
// confirmed when the process stopped are examined again.
func (uc *Usecase) rewindOnce(ctx context.Context) error {
	if uc.rewound {
		return nil
	}
	uc.rewound = true

	depth := uc.chain.ReorgDepth()
	if depth <= 0 {
		return nil
	}

	cursor, err := uc.postgres.RewindChainCursor(ctx, domain.NetworkBSC, depth)
	if err != nil {
		return fmt.Errorf("bsc_watcher: rewind cursor: %w", err)
	}
	if cursor.LastBlock > 0 {
		uc.logger.Infow("cursor rewound for reorg safety",
			"to_block", cursor.LastBlock, "depth", depth)
	}
	return nil
}

// scan walks from the cursor to the head in chunks, saving the cursor after each.
//
// Per-chunk saving is the point: after downtime the gap can be tens of thousands
// of blocks, and a single span would both exceed the endpoint's range limit and
// lose all progress on a restart.
func (uc *Usecase) scan(ctx context.Context, contracts []string, recipient string,
	head, finalityLine uint64,
) (Output, error) {
	var out Output

	cursor, err := uc.postgres.GetChainCursor(ctx, domain.NetworkBSC)
	if err != nil {
		return out, fmt.Errorf("bsc_watcher: read cursor: %w", err)
	}

	from := uint64(cursor.LastBlock) + 1
	if cursor.LastBlock == 0 {
		// First run: start at the head rather than at genesis. Anything earlier
		// predates the service, and no endpoint would serve that range anyway.
		from = head
	}

	// Pull the start back to cover the reorg window once we are caught up.
	//
	// This is how a withdrawn transfer is noticed at all. A poller never receives
	// a log marked `removed` — eth_getLogs answers with the canonical chain, so a
	// reorganised-out transfer is simply missing. Detection therefore has to be by
	// absence, and that needs the window re-read.
	//
	// While catching up, cursor+1 is far below the window and wins, so the cursor
	// keeps moving forward. It never moves backwards either way: it is still saved
	// as `to`.
	//
	// Costs no extra request in steady state: the window is 64 blocks against a
	// log_range of 2000, so this is the same single call with an earlier lower
	// bound.
	if depth := uint64(uc.chain.ReorgDepth()); depth > 0 {
		windowStart := uint64(1)
		if head+1 > depth {
			windowStart = head - depth + 1
		}
		if windowStart < from {
			from = windowStart
		}
	}

	if from > head {
		return out, nil
	}

	span := uc.chain.LogRange()
	for chunk := 0; chunk < maxChunksPerTick && from <= head; chunk++ {
		to := from + span - 1
		if to > head {
			to = head
		}

		logs, err := uc.chain.GetLogs(ctx, evm.LogQuery{
			FromBlock: from,
			ToBlock:   to,
			Addresses: contracts,
			Topics:    [][]string{{evm.TopicTransfer}, nil, {recipient}},
		})
		if err != nil {
			if errors.Is(err, evm.ErrRangeTooDeep) {
				// The cursor is outside what this endpoint retains. Deliberately
				// not skipped forward: the gap holds real payments, and stepping
				// over it silently would lose them. Fixed by pointing rpc_urls at
				// an endpoint with history.
				uc.logger.Errorw("the endpoint does not retain logs back to the cursor; "+
					"payments in this range cannot be seen — configure an endpoint with history",
					"cursor_block", cursor.LastBlock, "head", head,
					"gap_blocks", head-uint64(cursor.LastBlock), "error", err)
				return out, nil
			}
			return out, fmt.Errorf("bsc_watcher: logs %d..%d: %w", from, to, err)
		}

		seen := make(map[string]struct{}, len(logs))
		for _, l := range logs {
			seen[chainRef(l.TxHash, int32(l.LogIndex))] = struct{}{}

			if l.Removed {
				if err := uc.matcher.Revoke(ctx, domain.NetworkBSC, l.TxHash, int32(l.LogIndex)); err != nil {
					return out, fmt.Errorf("bsc_watcher: revoke %s: %w", l.TxHash, err)
				}
				out.Revoked++
				continue
			}

			value, err := valueFromData(l.Data)
			if err != nil {
				return out, fmt.Errorf("bsc_watcher: log %s: %w", l.TxHash, err)
			}
			sender, err := addressFromTopic(l.Topics, 1)
			if err != nil {
				return out, fmt.Errorf("bsc_watcher: log %s: %w", l.TxHash, err)
			}

			if _, err := uc.matcher.Apply(ctx, matching.Observed{
				Network:     domain.NetworkBSC,
				TxHash:      l.TxHash,
				LogIndex:    int32(l.LogIndex),
				Contract:    l.Address,
				From:        sender,
				To:          uc.invoices.PayAddress(domain.NetworkBSC),
				Value:       value,
				BlockNumber: int64(l.BlockNumber),
				// An eth_getLogs result carries no timestamp, and fetching the
				// block for one would cost a request per block. This is the
				// observation time, not the block's — honest about which, because
				// nothing on this chain depends on it: BSC finality is decided by
				// block number, and block_time only orders the settle queue.
				BlockTime: uc.now().UTC(),
				Final:     l.BlockNumber <= finalityLine,
			}); err != nil {
				return out, fmt.Errorf("bsc_watcher: apply %s: %w", l.TxHash, err)
			}
			out.Discovered++
		}

		// Anything credited in this range that is no longer in its logs has been
		// reorganised out.
		//
		// Checked per chunk rather than across the whole scan: locally it is
		// correct without knowing whether we are catching up, because a payment in
		// this range that is absent from this range's logs really is gone. A
		// global check would have to reason about which parts of the interval were
		// covered, and would revoke wrongly while catching up.
		revoked, err := uc.revokeMissing(ctx, from, to, seen)
		if err != nil {
			return out, err
		}
		out.Revoked += revoked

		// After the chunk, never before.
		if _, err := uc.postgres.SaveChainCursor(ctx, domain.NetworkBSC, int64(to), time.Time{}); err != nil {
			return out, fmt.Errorf("bsc_watcher: save cursor: %w", err)
		}
		out.ToBlock = to
		from = to + 1
	}

	if from <= head {
		uc.logger.Infow("still catching up", "next_block", from, "head", head)
	}
	return out, nil
}

// chainRef identifies one transfer within a transaction.
func chainRef(txHash string, logIndex int32) string {
	return fmt.Sprintf("%s:%d", txHash, logIndex)
}

// revokeMissing hands back the invoice of every credited payment in the range
// whose log has vanished.
//
// Only invoices still in `detected` are considered — the query enforces that. A
// confirmed payment is past the chain's own finality line, and un-crediting
// settled money because a log went missing would be worse than leaving it be.
func (uc *Usecase) revokeMissing(ctx context.Context, from, to uint64, seen map[string]struct{}) (int, error) {
	live, err := uc.postgres.ListLivePaymentsInBlockRange(ctx, domain.NetworkBSC, int64(from), int64(to))
	if err != nil {
		return 0, fmt.Errorf("bsc_watcher: list live payments in %d..%d: %w", from, to, err)
	}

	var revoked int
	for _, p := range live {
		if _, ok := seen[chainRef(p.TxHash, p.LogIndex)]; ok {
			continue
		}

		uc.logger.Warnw("a credited transfer is no longer in the chain's logs; "+
			"treating it as reorganised out",
			"tx", p.TxHash, "block", p.BlockNumber, "invoice", p.InvoiceID)

		if err := uc.matcher.Revoke(ctx, domain.NetworkBSC, p.TxHash, p.LogIndex); err != nil {
			return revoked, fmt.Errorf("bsc_watcher: revoke %s: %w", p.TxHash, err)
		}
		revoked++
	}
	return revoked, nil
}

func (uc *Usecase) settle(ctx context.Context, finalityLine uint64) (int, error) {
	pending, err := uc.postgres.ListPaymentsAwaitingConfirmation(ctx, domain.NetworkBSC, batchSize)
	if err != nil {
		return 0, fmt.Errorf("bsc_watcher: list unsettled payments: %w", err)
	}

	// Below the reorg window nothing re-reads a payment, so a detected one that
	// deep means finality has stopped advancing. Pathological, but not something
	// to stay silent about.
	var staleBefore uint64
	if depth := uint64(uc.chain.ReorgDepth()); depth > 0 && finalityLine > depth {
		staleBefore = finalityLine - depth
	}

	var settled int
	for _, p := range pending {
		if p.BlockNumber <= 0 || uint64(p.BlockNumber) > finalityLine {
			if staleBefore > 0 && p.BlockNumber > 0 && uint64(p.BlockNumber) < staleBefore {
				uc.logger.Warnw("a payment is unsettled below the reorg window, "+
					"where nothing will re-read it; finality appears stalled",
					"tx", p.TxHash, "block", p.BlockNumber, "finality_line", finalityLine)
			}
			continue
		}
		if _, err := uc.matcher.Settle(ctx, p); err != nil {
			return settled, fmt.Errorf("bsc_watcher: settle %s: %w", p.TxHash, err)
		}
		settled++
	}
	return settled, nil
}
