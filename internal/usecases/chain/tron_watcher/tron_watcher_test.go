package tron_watcher

import (
	"context"
	"errors"
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
	"github.com/gos0001/gomod-cryptopay/pkg/tron"
)

var (
	now        = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	solidified = now.Add(-57 * time.Second) // the measured TRON finality window
	payAddress = "TWd4WrZ9wn84f5x1hZhL4DHvk738ns5jwb"
)

type fakeChain struct {
	page     tron.TransfersPage
	pageErr  error
	solid    tron.Block
	solidErr error

	queries []tron.TransfersQuery
}

func (f *fakeChain) TRC20Transfers(_ context.Context, q tron.TransfersQuery) (tron.TransfersPage, error) {
	f.queries = append(f.queries, q)
	return f.page, f.pageErr
}

func (f *fakeChain) SolidifiedBlock(context.Context) (tron.Block, error) {
	return f.solid, f.solidErr
}

type applied struct {
	txHash string
	final  bool
}

type fakeMatcher struct {
	applies  []applied
	settles  []string
	applyErr error
}

func (f *fakeMatcher) Apply(_ context.Context, in matching.Observed) (matching.Result, error) {
	f.applies = append(f.applies, applied{txHash: in.TxHash, final: in.Final})
	if f.applyErr != nil {
		return matching.Result{}, f.applyErr
	}
	return matching.Result{Outcome: matching.OutcomeCredited}, nil
}

func (f *fakeMatcher) Settle(_ context.Context, p domain.Payment) (matching.Result, error) {
	f.settles = append(f.settles, p.TxHash)
	return matching.Result{Outcome: matching.OutcomeCredited}, nil
}

type fakePostgres struct {
	cursor    domain.ChainCursor
	unsettled []domain.Payment

	savedTimestamps []time.Time
}

func (f *fakePostgres) GetChainCursor(context.Context, domain.Network) (domain.ChainCursor, error) {
	return f.cursor, nil
}

func (f *fakePostgres) SaveChainCursor(_ context.Context, _ domain.Network, _ int64, ts time.Time) (domain.ChainCursor, error) {
	f.savedTimestamps = append(f.savedTimestamps, ts)
	f.cursor.LastTimestamp = ts
	return f.cursor, nil
}

func (f *fakePostgres) ListPaymentsAwaitingConfirmation(context.Context, domain.Network, int32) ([]domain.Payment, error) {
	return f.unsettled, nil
}

func watcher(chain *fakeChain, m *fakeMatcher, pg *fakePostgres) *Usecase {
	return &Usecase{
		chain: chain, matcher: m, postgres: pg,
		cfg: Config{
			WatchInterval: config.Duration(5 * time.Second),
			StaleAfter:    config.Duration(5 * time.Minute),
		},
		invoices: invoicecfg.Config{PayAddressTron: payAddress},
		logger:   zap.NewNop().Sugar(),
		now:      func() time.Time { return now },
	}
}

func transfer(txID string, at time.Time) tron.Transfer {
	return tron.Transfer{
		TxID: txID, From: "TSender", To: payAddress,
		Value: big.NewInt(10_500_100), ContractAddress: "TR7NHq",
		Symbol: "USDT", Decimals: 6, BlockTime: at,
	}
}

func TestExecuteAppliesEachTransfer(t *testing.T) {
	chain := &fakeChain{
		solid: tron.Block{Number: 100, Time: solidified},
		page: tron.TransfersPage{Transfers: []tron.Transfer{
			transfer("tx1", solidified.Add(-time.Minute)), // already final
			transfer("tx2", now),                          // still reversible
		}},
	}
	m := &fakeMatcher{}

	out, err := watcher(chain, m, &fakePostgres{}).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Discovered != 2 {
		t.Errorf("discovered = %d", out.Discovered)
	}
	if len(m.applies) != 2 {
		t.Fatalf("applies = %+v", m.applies)
	}
	// Finality is the watcher's job, and it is decided by comparing the
	// transfer's timestamp against the solidified head's.
	if !m.applies[0].final {
		t.Error("a transfer below the solidified head must be final")
	}
	if m.applies[1].final {
		t.Error("a transfer above the solidified head must not be final")
	}
}

// The cursor moves to the newest timestamp seen and the next query uses it
// inclusively: several transfers can share one block timestamp, and stepping
// past it would drop a sibling in the same block.
func TestCursorAdvancesToTheNewestTimestampInclusively(t *testing.T) {
	newest := now.Add(-10 * time.Second)
	chain := &fakeChain{
		solid: tron.Block{Number: 100, Time: solidified},
		page: tron.TransfersPage{Transfers: []tron.Transfer{
			transfer("tx1", now.Add(-time.Minute)),
			transfer("tx2", newest),
			transfer("tx3", newest), // same block
		}},
	}
	pg := &fakePostgres{}

	if _, err := watcher(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{}); err != nil {
		t.Fatal(err)
	}

	if len(pg.savedTimestamps) != 1 {
		t.Fatalf("saved %d cursors", len(pg.savedTimestamps))
	}
	if got := pg.savedTimestamps[0]; !got.Equal(newest) {
		t.Fatalf("cursor = %s, want the newest timestamp %s", got, newest)
	}
	// And the query used the previous cursor as the lower bound, unmodified.
	if q := chain.queries[0]; !q.MinTimestamp.IsZero() {
		t.Errorf("min_timestamp = %s, want the zero cursor on a first run", q.MinTimestamp)
	}
}

// A failure mid-page must not advance the cursor, or the transfers after the
// failure are lost with no trace.
func TestCursorStaysPutWhenAMatchFails(t *testing.T) {
	chain := &fakeChain{
		solid: tron.Block{Number: 100, Time: solidified},
		page:  tron.TransfersPage{Transfers: []tron.Transfer{transfer("tx1", now)}},
	}
	pg := &fakePostgres{}
	m := &fakeMatcher{applyErr: errors.New("storage is down")}

	if _, err := watcher(chain, m, pg).Execute(context.Background(), Input{}); err == nil {
		t.Fatal("want the tick to fail")
	}
	if len(pg.savedTimestamps) != 0 {
		t.Fatalf("the cursor advanced to %v despite a failure", pg.savedTimestamps)
	}
}

func TestCursorIsNotSavedWhenNothingIsNew(t *testing.T) {
	chain := &fakeChain{solid: tron.Block{Number: 100, Time: solidified}}
	pg := &fakePostgres{cursor: domain.ChainCursor{LastTimestamp: now.Add(-time.Hour)}}

	if _, err := watcher(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{}); err != nil {
		t.Fatal(err)
	}
	if len(pg.savedTimestamps) != 0 {
		t.Errorf("saved %v for an empty page", pg.savedTimestamps)
	}
}

// The settle pass is the reason a payment first seen unconfirmed ever reaches
// confirmed: the feed will not return it again once the cursor has passed it.
func TestSettlePromotesPaymentsBelowTheFinalityLine(t *testing.T) {
	invoiceID := uuid.New()
	chain := &fakeChain{solid: tron.Block{Number: 100, Time: solidified}}
	pg := &fakePostgres{unsettled: []domain.Payment{
		{TxHash: "ripe", BlockTime: solidified.Add(-time.Minute), InvoiceID: &invoiceID},
		{TxHash: "green", BlockTime: now, InvoiceID: &invoiceID},
	}}
	m := &fakeMatcher{}

	out, err := watcher(chain, m, pg).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}

	if out.Settled != 1 {
		t.Errorf("settled = %d, want only the one past the finality line", out.Settled)
	}
	if len(m.settles) != 1 || m.settles[0] != "ripe" {
		t.Fatalf("settles = %v", m.settles)
	}
}

// TRON reports nothing when a transfer is un-mined, so a payment stuck far past
// the 57-second window is the only trace it leaves.
func TestStalePaymentIsCountedAndWarned(t *testing.T) {
	invoiceID := uuid.New()
	chain := &fakeChain{solid: tron.Block{Number: 100, Time: solidified}}
	pg := &fakePostgres{unsettled: []domain.Payment{
		// Above the finality line yet older than stale_after: impossible unless
		// something went wrong.
		{TxHash: "stuck", BlockTime: now.Add(-10 * time.Minute), InvoiceID: &invoiceID},
	}}

	// Push the solidified head far back so the payment stays "not final".
	chain.solid.Time = now.Add(-time.Hour)

	out, err := watcher(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Stale != 1 {
		t.Fatalf("stale = %d, want 1", out.Stale)
	}
	if out.Settled != 0 {
		t.Errorf("settled = %d, want none", out.Settled)
	}
}

func TestExecuteRefusesWithoutAReceivingAddress(t *testing.T) {
	uc := watcher(&fakeChain{solid: tron.Block{Number: 1, Time: solidified}}, &fakeMatcher{}, &fakePostgres{})
	uc.invoices.PayAddressTron = ""

	if _, err := uc.Execute(context.Background(), Input{}); err == nil {
		t.Fatal("want an error")
	}
}

// The finality line is fetched before discovery so the transfers found in this
// tick can be classified without a second call.
func TestFinalityLineFailureAbortsTheTick(t *testing.T) {
	chain := &fakeChain{solidErr: errors.New("trongrid is down")}
	pg := &fakePostgres{}

	if _, err := watcher(chain, &fakeMatcher{}, pg).Execute(context.Background(), Input{}); err == nil {
		t.Fatal("want an error")
	}
	if len(chain.queries) != 0 {
		t.Error("discovery should not run without a finality line")
	}
}

func TestLoadConfigDefaultsAndValidation(t *testing.T) {
	// Zero interval is the documented off switch and must load.
	if _, err := loadFrom(t, `{"tron": {"watch_interval": "0s"}}`); err != nil {
		t.Fatalf("zero interval should be allowed: %v", err)
	}
	if _, err := loadFrom(t, `{"tron": {"stale_after": "0s"}}`); err == nil {
		t.Fatal("zero stale_after should be refused")
	}

	cfg, err := loadFrom(t, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WatchInterval.Std() != 5*time.Second {
		t.Errorf("watch_interval = %s", cfg.WatchInterval)
	}
	if cfg.StaleAfter.Std() != 5*time.Minute {
		t.Errorf("stale_after = %s", cfg.StaleAfter)
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
