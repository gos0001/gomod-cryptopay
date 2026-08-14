package bsc_watcher

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/invoicecfg"
	"github.com/gos0001/gomod-cryptopay/internal/service/matching"
	"github.com/gos0001/gomod-cryptopay/pkg/config"
	"github.com/gos0001/gomod-cryptopay/pkg/evm"
)

const (
	payAddress = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
	contract   = "0x5fbdb2315678afecb367f032d93f642f64180aa3"
	sender     = "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266"
)

var now = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

type fakeChain struct {
	head      uint64
	finalized uint64
	finalErr  error

	// logsFor answers per requested range, so chunking can be observed.
	logsFor  func(from, to uint64) ([]evm.Log, error)
	queries  []evm.LogQuery
	logRange uint64

	confirmations int64
	reorgDepth    int64
	useFinalized  bool
}

func (f *fakeChain) BlockNumber(context.Context) (uint64, error) { return f.head, nil }

func (f *fakeChain) FinalizedBlockNumber(context.Context) (uint64, error) {
	return f.finalized, f.finalErr
}

func (f *fakeChain) GetLogs(_ context.Context, q evm.LogQuery) ([]evm.Log, error) {
	f.queries = append(f.queries, q)
	if f.logsFor == nil {
		return nil, nil
	}
	return f.logsFor(q.FromBlock, q.ToBlock)
}

func (f *fakeChain) LogRange() uint64      { return f.logRange }
func (f *fakeChain) Confirmations() int64  { return f.confirmations }
func (f *fakeChain) ReorgDepth() int64     { return f.reorgDepth }
func (f *fakeChain) UseFinalizedTag() bool { return f.useFinalized }

type fakeMatcher struct {
	applies []matching.Observed
	settles []string
	revokes []string
}

func (f *fakeMatcher) Apply(_ context.Context, in matching.Observed) (matching.Result, error) {
	f.applies = append(f.applies, in)
	return matching.Result{Outcome: matching.OutcomeCredited}, nil
}

func (f *fakeMatcher) Settle(_ context.Context, p domain.Payment) (matching.Result, error) {
	f.settles = append(f.settles, p.TxHash)
	return matching.Result{Outcome: matching.OutcomeCredited}, nil
}

func (f *fakeMatcher) Revoke(_ context.Context, _ domain.Network, txHash string, _ int32) error {
	f.revokes = append(f.revokes, txHash)
	return nil
}

type fakePostgres struct {
	assets    []domain.Asset
	cursor    domain.ChainCursor
	unsettled []domain.Payment

	savedBlocks  []int64
	rewoundDepth int64
	rewinds      int

	// live answers the absence check; rangeQueries records what it was asked.
	live         []domain.Payment
	rangeQueries [][2]int64
}

func (f *fakePostgres) ListEnabledAssetsByNetwork(context.Context, domain.Network) ([]domain.Asset, error) {
	return f.assets, nil
}

func (f *fakePostgres) GetChainCursor(context.Context, domain.Network) (domain.ChainCursor, error) {
	return f.cursor, nil
}

func (f *fakePostgres) SaveChainCursor(_ context.Context, _ domain.Network, block int64, _ time.Time) (domain.ChainCursor, error) {
	f.savedBlocks = append(f.savedBlocks, block)
	f.cursor.LastBlock = block
	return f.cursor, nil
}

func (f *fakePostgres) RewindChainCursor(_ context.Context, _ domain.Network, depth int64) (domain.ChainCursor, error) {
	f.rewinds++
	f.rewoundDepth = depth
	if f.cursor.LastBlock > depth {
		f.cursor.LastBlock -= depth
	} else {
		f.cursor.LastBlock = 0
	}
	return f.cursor, nil
}

func (f *fakePostgres) ListPaymentsAwaitingConfirmation(context.Context, domain.Network, int32) ([]domain.Payment, error) {
	return f.unsettled, nil
}

func (f *fakePostgres) ListLivePaymentsInBlockRange(_ context.Context, _ domain.Network, from, to int64) ([]domain.Payment, error) {
	f.rangeQueries = append(f.rangeQueries, [2]int64{from, to})

	var out []domain.Payment
	for _, p := range f.live {
		if p.BlockNumber >= from && p.BlockNumber <= to {
			out = append(out, p)
		}
	}
	return out, nil
}

func watcher(chain *fakeChain, m *fakeMatcher, pg *fakePostgres) *Usecase {
	if chain.logRange == 0 {
		chain.logRange = 100
	}
	if len(pg.assets) == 0 {
		pg.assets = []domain.Asset{{
			ID: 1, Network: domain.NetworkBSC, Symbol: "USDT",
			ContractAddress: contract, Decimals: 18, Step: big.NewInt(1), NonceMax: 10, Enabled: true,
		}}
	}
	return &Usecase{
		chain: chain, matcher: m, postgres: pg,
		cfg:      Config{WatchInterval: config.Duration(5 * time.Second)},
		invoices: invoicecfg.Config{PayAddressBSC: payAddress},
		logger:   zap.NewNop().Sugar(),
		now:      func() time.Time { return now },
	}
}

// transferLog builds a log the way a node reports one: value in data, sender and
// recipient as left-padded topics.
func transferLog(txHash string, block uint64, value string, removed bool) evm.Log {
	pad := func(addr string) string {
		return "0x" + "000000000000000000000000" + addr[2:]
	}
	amount, _ := new(big.Int).SetString(value, 10)
	return evm.Log{
		Address:     contract,
		TxHash:      txHash,
		BlockNumber: block,
		LogIndex:    0,
		Topics:      []string{evm.TopicTransfer, pad(sender), pad(payAddress)},
		Data:        fmt.Sprintf("0x%064x", amount),
		Removed:     removed,
	}
}

func TestExecuteAppliesTransfers(t *testing.T) {
	chain := &fakeChain{head: 150, finalized: 100, useFinalized: true, logRange: 100}
	chain.logsFor = func(from, to uint64) ([]evm.Log, error) {
		return []evm.Log{
			transferLog("0xa", 90, "7250000000000000001", false),  // below finality
			transferLog("0xb", 140, "1500000000000000000", false), // above it
		}, nil
	}
	m := &fakeMatcher{}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 80}}

	out, err := watcher(chain, m, pg).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Discovered != 2 {
		t.Fatalf("discovered = %d", out.Discovered)
	}
	if !m.applies[0].Final {
		t.Error("a log at or below the finalized head must be final")
	}
	if m.applies[1].Final {
		t.Error("a log above the finalized head must not be final")
	}

	// The 18-decimal value has to survive decoding intact — the last digit is
	// what a float64 would drop.
	want, _ := new(big.Int).SetString("7250000000000000001", 10)
	if m.applies[0].Value.Cmp(want) != 0 {
		t.Fatalf("value = %s, want %s", m.applies[0].Value, want)
	}
	if m.applies[0].From != sender {
		t.Errorf("from = %q, want the sender decoded from topics[1]", m.applies[0].From)
	}
}

// The filter must name the contracts and put the padded recipient in position 2,
// with a nil in position 1 so any sender matches.
func TestQueryFilterShape(t *testing.T) {
	chain := &fakeChain{head: 50, finalized: 40, useFinalized: true, logRange: 100}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 49}}

	if _, err := watcher(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{}); err != nil {
		t.Fatal(err)
	}

	q := chain.queries[0]
	if len(q.Addresses) != 1 || q.Addresses[0] != contract {
		t.Errorf("addresses = %v", q.Addresses)
	}
	if len(q.Topics) != 3 {
		t.Fatalf("topics = %v", q.Topics)
	}
	if q.Topics[0][0] != evm.TopicTransfer {
		t.Errorf("topics[0] = %v", q.Topics[0])
	}
	if q.Topics[1] != nil {
		t.Errorf("topics[1] = %v, want nil so any sender matches", q.Topics[1])
	}
	wantRecipient, _ := evm.PadTopic(payAddress)
	if q.Topics[2][0] != wantRecipient {
		t.Errorf("topics[2] = %v, want the padded recipient", q.Topics[2])
	}
}

// Per-chunk cursor saving is the point: after downtime the gap can be huge, and
// one span would exceed the endpoint's range limit and lose all progress on a
// restart.
func TestCatchUpChunksAndSavesAfterEach(t *testing.T) {
	chain := &fakeChain{head: 1000, finalized: 900, useFinalized: true, logRange: 100}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 500}}

	out, err := watcher(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}

	if len(chain.queries) < 2 {
		t.Fatalf("made %d queries; the range should have been chunked", len(chain.queries))
	}
	for i, q := range chain.queries {
		if span := q.ToBlock - q.FromBlock + 1; span > chain.logRange {
			t.Fatalf("chunk %d spans %d blocks, above the limit of %d", i, span, chain.logRange)
		}
	}
	// One cursor save per chunk, monotonically increasing.
	if len(pg.savedBlocks) != len(chain.queries) {
		t.Fatalf("%d chunks but %d cursor saves", len(chain.queries), len(pg.savedBlocks))
	}
	for i := 1; i < len(pg.savedBlocks); i++ {
		if pg.savedBlocks[i] <= pg.savedBlocks[i-1] {
			t.Fatalf("cursor went backwards: %v", pg.savedBlocks)
		}
	}
	if int64(out.ToBlock) != pg.savedBlocks[len(pg.savedBlocks)-1] {
		t.Errorf("reported to_block %d, last saved %d", out.ToBlock, pg.savedBlocks[len(pg.savedBlocks)-1])
	}
}

// A backlog longer than one tick can cover must stop and resume, not spin.
func TestCatchUpIsBoundedPerTick(t *testing.T) {
	chain := &fakeChain{head: 1_000_000, finalized: 900_000, useFinalized: true, logRange: 100}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 1}}

	if _, err := watcher(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{}); err != nil {
		t.Fatal(err)
	}
	if len(chain.queries) != maxChunksPerTick {
		t.Fatalf("made %d queries, want the per-tick cap of %d", len(chain.queries), maxChunksPerTick)
	}
}

// A first run starts at the head rather than at genesis: anything earlier
// predates the service, and no endpoint would serve that range.
func TestFirstRunStartsAtTheHead(t *testing.T) {
	chain := &fakeChain{head: 500_000, finalized: 499_000, useFinalized: true, logRange: 100}
	pg := &fakePostgres{}

	if _, err := watcher(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{}); err != nil {
		t.Fatal(err)
	}
	if len(chain.queries) != 1 {
		t.Fatalf("made %d queries", len(chain.queries))
	}
	if chain.queries[0].FromBlock != 500_000 {
		t.Fatalf("from = %d, want the head", chain.queries[0].FromBlock)
	}
}

// The branch this watcher exists for: an un-mined transfer hands the invoice back.
func TestRemovedLogIsRevoked(t *testing.T) {
	chain := &fakeChain{head: 150, finalized: 100, useFinalized: true, logRange: 100}
	chain.logsFor = func(uint64, uint64) ([]evm.Log, error) {
		return []evm.Log{transferLog("0xgone", 120, "1000", true)}, nil
	}
	m := &fakeMatcher{}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 100}}

	out, err := watcher(chain, m, pg).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}

	if out.Revoked != 1 || out.Discovered != 0 {
		t.Fatalf("revoked = %d discovered = %d", out.Revoked, out.Discovered)
	}
	if len(m.revokes) != 1 || m.revokes[0] != "0xgone" {
		t.Fatalf("revokes = %v", m.revokes)
	}
	if len(m.applies) != 0 {
		t.Errorf("a removed log must not be applied: %+v", m.applies)
	}
}

// Stepping over a gap the endpoint cannot serve would lose real payments, so the
// cursor holds and the tick ends without error — the fix is a different endpoint.
func TestRangeTooDeepHoldsTheCursor(t *testing.T) {
	chain := &fakeChain{head: 100_000, finalized: 99_000, useFinalized: true, logRange: 100}
	chain.logsFor = func(uint64, uint64) ([]evm.Log, error) {
		return nil, fmt.Errorf("%w: archive required", evm.ErrRangeTooDeep)
	}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 1000}}

	out, err := watcher(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatalf("the tick should end quietly, not fail: %v", err)
	}
	if len(pg.savedBlocks) != 0 {
		t.Fatalf("the cursor advanced to %v across an unreadable gap", pg.savedBlocks)
	}
	if out.Discovered != 0 {
		t.Errorf("discovered = %d", out.Discovered)
	}
}

// Any other log error must fail the tick and leave the cursor alone.
func TestOtherLogErrorsFailTheTick(t *testing.T) {
	chain := &fakeChain{head: 200, finalized: 100, useFinalized: true, logRange: 100}
	chain.logsFor = func(uint64, uint64) ([]evm.Log, error) {
		return nil, errors.New("connection reset")
	}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 150}}

	if _, err := watcher(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{}); err == nil {
		t.Fatal("want an error")
	}
	if len(pg.savedBlocks) != 0 {
		t.Fatalf("cursor advanced to %v", pg.savedBlocks)
	}
}

func TestFallsBackToConfirmationsWithoutTheFinalizedTag(t *testing.T) {
	chain := &fakeChain{
		head: 200, useFinalized: true, confirmations: 15, logRange: 100,
		finalErr: fmt.Errorf("%w: not supported", evm.ErrFinalizedTagUnsupported),
	}
	chain.logsFor = func(uint64, uint64) ([]evm.Log, error) {
		return []evm.Log{
			transferLog("0xdeep", 180, "1000", false),    // 200-15 = 185, so final
			transferLog("0xshallow", 190, "1000", false), // above it
		}, nil
	}
	m := &fakeMatcher{}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 150}}

	if _, err := watcher(chain, m, pg).Execute(context.Background(), Input{}); err != nil {
		t.Fatal(err)
	}
	if !m.applies[0].Final {
		t.Error("block 180 is 20 deep with a 15-confirmation rule; it should be final")
	}
	if m.applies[1].Final {
		t.Error("block 190 is only 10 deep; it should not be final")
	}
}

func TestRewindHappensOncePerProcess(t *testing.T) {
	chain := &fakeChain{head: 1000, finalized: 900, useFinalized: true, logRange: 100, reorgDepth: 64}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 500}}
	uc := watcher(chain, &fakeMatcher{}, pg)

	for i := 0; i < 3; i++ {
		if _, err := uc.Execute(context.Background(), Input{}); err != nil {
			t.Fatal(err)
		}
	}

	if pg.rewinds != 1 {
		t.Fatalf("rewound %d times; repeating it would walk the cursor backwards forever", pg.rewinds)
	}
	if pg.rewoundDepth != 64 {
		t.Errorf("depth = %d", pg.rewoundDepth)
	}
}

func TestSettleUsesBlockNumberAgainstTheFinalityLine(t *testing.T) {
	invoiceID := uuid.New()
	chain := &fakeChain{head: 200, finalized: 150, useFinalized: true, logRange: 100}
	pg := &fakePostgres{
		cursor: domain.ChainCursor{LastBlock: 199},
		unsettled: []domain.Payment{
			{TxHash: "ripe", BlockNumber: 140, InvoiceID: &invoiceID},
			{TxHash: "green", BlockNumber: 180, InvoiceID: &invoiceID},
			{TxHash: "no-block", BlockNumber: 0, InvoiceID: &invoiceID},
		},
	}
	m := &fakeMatcher{}

	out, err := watcher(chain, m, pg).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Settled != 1 {
		t.Fatalf("settled = %d", out.Settled)
	}
	if len(m.settles) != 1 || m.settles[0] != "ripe" {
		t.Fatalf("settles = %v", m.settles)
	}
}

// An operator running TRON only should not see errors from a BSC watcher with
// nothing to watch.
func TestNoAssetsIsNotAnError(t *testing.T) {
	chain := &fakeChain{head: 100, finalized: 90, useFinalized: true, logRange: 100}
	pg := &fakePostgres{assets: []domain.Asset{}}
	uc := watcher(chain, &fakeMatcher{}, pg)
	uc.postgres = &fakePostgres{assets: nil} // genuinely empty

	if _, err := uc.Execute(context.Background(), Input{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain.queries) != 0 {
		t.Error("nothing to watch, so nothing should have been queried")
	}
}

func TestExecuteRefusesABadReceivingAddress(t *testing.T) {
	uc := watcher(&fakeChain{head: 100, logRange: 100}, &fakeMatcher{}, &fakePostgres{})
	uc.invoices.PayAddressBSC = "not-an-address"

	if _, err := uc.Execute(context.Background(), Input{}); err == nil {
		t.Fatal("want an error")
	}
}

func TestValueFromData(t *testing.T) {
	want, _ := new(big.Int).SetString("7250000000000000001", 10)

	got, err := valueFromData(fmt.Sprintf("0x%064x", want))
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(want) != 0 {
		t.Fatalf("got %s, want %s", got, want)
	}

	for name, in := range map[string]string{
		"empty":    "0x",
		"non-hex":  "0xzz",
		"too long": "0x" + "ff" + fmt.Sprintf("%064x", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := valueFromData(in); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestAddressFromTopic(t *testing.T) {
	topics := []string{
		evm.TopicTransfer,
		"0x000000000000000000000000F39FD6E51AAD88F6F4CE6AB8827279CFFFB92266",
	}

	got, err := addressFromTopic(topics, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Lowercased to match how nodes report addresses and how assets are stored.
	if got != sender {
		t.Fatalf("got %q, want %q", got, sender)
	}

	if _, err := addressFromTopic(topics, 5); err == nil {
		t.Error("an index beyond the topics must fail")
	}
	if _, err := addressFromTopic([]string{"0xshort"}, 0); err == nil {
		t.Error("a malformed topic must fail")
	}
}

func TestLoadConfig(t *testing.T) {
	cfg, err := loadFrom(t, `{"bsc": {"rpc_urls": ["http://localhost:8545"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WatchInterval.Std() != 5*time.Second {
		t.Errorf("watch_interval = %s", cfg.WatchInterval)
	}

	// Zero is the documented off switch.
	if _, err := loadFrom(t, `{"bsc": {"watch_interval": "0s"}}`); err != nil {
		t.Fatalf("zero interval should be allowed: %v", err)
	}
}

func loadFrom(t *testing.T, contents string) (Config, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	file, err := config.Load(config.Path(path))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return LoadConfig(file)
}

// steady returns a watcher past its one-shot startup rewind, so the per-tick
// window pull-back is what the assertions see. Both mechanisms are real and they
// stack on the first tick; testing them together only measures their sum.
func steady(chain *fakeChain, m *fakeMatcher, pg *fakePostgres) *Usecase {
	uc := watcher(chain, m, pg)
	uc.rewound = true
	return uc
}

// A poller never receives a log marked `removed`, so a withdrawn transfer shows
// up as absence. Detecting it requires re-reading the reorg window every tick.
func TestWindowIsPulledBackOnceCaughtUp(t *testing.T) {
	chain := &fakeChain{head: 1000, finalized: 990, useFinalized: true, logRange: 2000, reorgDepth: 64}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 999}}

	if _, err := steady(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{}); err != nil {
		t.Fatal(err)
	}

	if len(chain.queries) != 1 {
		t.Fatalf("made %d queries; the window fits in one log_range", len(chain.queries))
	}
	if got := chain.queries[0].FromBlock; got != 1000-64+1 {
		t.Fatalf("from = %d, want the start of the reorg window %d", got, uint64(1000-64+1))
	}
	if got := chain.queries[0].ToBlock; got != 1000 {
		t.Errorf("to = %d, want the head", got)
	}
	// The cursor must not go backwards.
	if pg.savedBlocks[len(pg.savedBlocks)-1] != 1000 {
		t.Errorf("cursor = %v, want it at the head", pg.savedBlocks)
	}
}

// While catching up the cursor is far below the window, so it wins and progress
// continues; pulling back would stall the catch-up forever.
func TestWindowIsNotPulledBackWhileCatchingUp(t *testing.T) {
	chain := &fakeChain{head: 100_000, finalized: 99_000, useFinalized: true, logRange: 100, reorgDepth: 64}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 500}}

	if _, err := steady(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{}); err != nil {
		t.Fatal(err)
	}
	if got := chain.queries[0].FromBlock; got != 501 {
		t.Fatalf("from = %d, want to continue from the cursor at 501", got)
	}
}

// The payoff: a credited payment whose log has vanished hands its invoice back.
func TestMissingLogIsRevoked(t *testing.T) {
	invoiceID := uuid.New()
	chain := &fakeChain{head: 200, finalized: 100, useFinalized: true, logRange: 2000, reorgDepth: 64}
	chain.logsFor = func(uint64, uint64) ([]evm.Log, error) {
		// The chain no longer carries the transfer at block 150.
		return []evm.Log{transferLog("0xstillhere", 160, "1000", false)}, nil
	}
	pg := &fakePostgres{
		cursor: domain.ChainCursor{LastBlock: 199},
		live: []domain.Payment{
			{TxHash: "0xgone", LogIndex: 0, BlockNumber: 150, InvoiceID: &invoiceID},
		},
	}
	m := &fakeMatcher{}

	out, err := steady(chain, m, pg).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}

	if out.Revoked != 1 {
		t.Fatalf("revoked = %d, want 1", out.Revoked)
	}
	if len(m.revokes) != 1 || m.revokes[0] != "0xgone" {
		t.Fatalf("revokes = %v", m.revokes)
	}
	// The absence check must have been asked about the range that was scanned.
	if len(pg.rangeQueries) != 1 {
		t.Fatalf("range queries = %v", pg.rangeQueries)
	}
	if pg.rangeQueries[0] != [2]int64{137, 200} {
		t.Errorf("asked about %v, want the scanned chunk", pg.rangeQueries[0])
	}
}

// The dangerous mistake here would be revoking a payment that is still there.
func TestPresentLogIsNotRevoked(t *testing.T) {
	invoiceID := uuid.New()
	chain := &fakeChain{head: 200, finalized: 100, useFinalized: true, logRange: 2000, reorgDepth: 64}
	chain.logsFor = func(uint64, uint64) ([]evm.Log, error) {
		return []evm.Log{transferLog("0xhere", 150, "1000", false)}, nil
	}
	pg := &fakePostgres{
		cursor: domain.ChainCursor{LastBlock: 199},
		live: []domain.Payment{
			{TxHash: "0xhere", LogIndex: 0, BlockNumber: 150, InvoiceID: &invoiceID},
		},
	}
	m := &fakeMatcher{}

	out, err := steady(chain, m, pg).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Revoked != 0 || len(m.revokes) != 0 {
		t.Fatalf("a present transfer was revoked: revoked=%d %v", out.Revoked, m.revokes)
	}
}

// The log index is part of the identity: two transfers in one transaction are
// different payments, and matching on the hash alone would spare the wrong one.
func TestRevocationDistinguishesLogIndex(t *testing.T) {
	invoiceID := uuid.New()
	chain := &fakeChain{head: 200, finalized: 100, useFinalized: true, logRange: 2000, reorgDepth: 64}
	chain.logsFor = func(uint64, uint64) ([]evm.Log, error) {
		l := transferLog("0xsametx", 150, "1000", false)
		l.LogIndex = 0
		return []evm.Log{l}, nil
	}
	pg := &fakePostgres{
		cursor: domain.ChainCursor{LastBlock: 199},
		live: []domain.Payment{
			{TxHash: "0xsametx", LogIndex: 0, BlockNumber: 150, InvoiceID: &invoiceID}, // present
			{TxHash: "0xsametx", LogIndex: 1, BlockNumber: 150, InvoiceID: &invoiceID}, // gone
		},
	}
	m := &fakeMatcher{}

	out, err := steady(chain, m, pg).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Revoked != 1 {
		t.Fatalf("revoked = %d, want only log index 1", out.Revoked)
	}
}

// A payment outside the scanned range must not be touched: it was never looked
// for, so its absence proves nothing.
func TestPaymentOutsideTheScannedRangeIsLeftAlone(t *testing.T) {
	invoiceID := uuid.New()
	chain := &fakeChain{head: 200, finalized: 100, useFinalized: true, logRange: 2000, reorgDepth: 64}
	pg := &fakePostgres{
		cursor: domain.ChainCursor{LastBlock: 199},
		live: []domain.Payment{
			// Below the window: not scanned this tick.
			{TxHash: "0xold", LogIndex: 0, BlockNumber: 50, InvoiceID: &invoiceID},
		},
	}
	m := &fakeMatcher{}

	out, err := steady(chain, m, pg).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Revoked != 0 || len(m.revokes) != 0 {
		t.Fatalf("revoked something outside the scanned range: %v", m.revokes)
	}
}

// With no reorg depth configured the window is not pulled back at all.
func TestZeroReorgDepthDisablesThePullBack(t *testing.T) {
	chain := &fakeChain{head: 1000, finalized: 990, useFinalized: true, logRange: 2000, reorgDepth: 0}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastBlock: 999}}

	if _, err := steady(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{}); err != nil {
		t.Fatal(err)
	}
	if got := chain.queries[0].FromBlock; got != 1000 {
		t.Fatalf("from = %d, want to continue from the cursor", got)
	}
}
