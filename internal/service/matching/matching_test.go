package matching

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/invoicecfg"
	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

var (
	fixedNow   = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	invoiceID  = uuid.MustParse("11111111-2222-3333-4444-555555555555")
	eventID    = uuid.MustParse("99999999-8888-7777-6666-555555555555")
	usdtOnBSC  = "0x55d398326f99059ff775485246999027b3197955"
	payAddress = "0x70997970c51812dc3a010c7d01b50e0d17dc79c8"
)

func asset() domain.Asset {
	return domain.Asset{
		ID: 1, Network: domain.NetworkBSC, Symbol: "USDT",
		ContractAddress: usdtOnBSC, Decimals: 18,
		Step: big.NewInt(100_000_000_000_000), NonceMax: 1000, Enabled: true,
	}
}

func invoiceIn(status domain.InvoiceStatus) domain.Invoice {
	return domain.Invoice{
		ID: invoiceID, AssetID: 1, Network: domain.NetworkBSC, PayAddress: payAddress,
		BaseAmount: big.NewInt(1_000_000_000_000_000_000),
		PayAmount:  big.NewInt(1_000_000_000_000_000_000),
		Status:     status, ExpiresAt: fixedNow.Add(time.Hour),
		AmountHoldUntil: fixedNow.Add(3 * time.Hour),
	}
}

// fake records everything written, and stands in for the transaction by simply
// running the callback against itself. Rollback is simulated by discarding the
// recorded writes when the callback returns an error.
type fake struct {
	asset   domain.Asset
	assetOK bool

	invoice   domain.Invoice
	invoiceOK bool

	payment   domain.Payment
	paymentOK bool

	// recorded writes
	payments    []domain.Payment
	orphans     []domain.OrphanTransfer
	events      []postgresadapter.WebhookEvent
	removed     []string
	transitions []transition

	// behaviour switches
	paymentExists   bool
	transitionFails bool
	enqueueFails    bool
}

type transition struct {
	from, to  domain.InvoiceStatus
	paidAt    *time.Time
	holdUntil time.Time
}

func (f *fake) GetAssetByContract(context.Context, domain.Network, string) (domain.Asset, error) {
	if !f.assetOK {
		return domain.Asset{}, domain.ErrAssetNotFound
	}
	return f.asset, nil
}

func (f *fake) GetAssetByID(context.Context, int64) (domain.Asset, error) {
	return f.asset, nil
}

func (f *fake) FindInvoiceForAmount(context.Context, int64, *big.Int) (domain.Invoice, error) {
	if !f.invoiceOK {
		return domain.Invoice{}, domain.ErrInvoiceNotFound
	}
	return f.invoice, nil
}

func (f *fake) GetInvoiceByID(context.Context, uuid.UUID) (domain.Invoice, error) {
	if !f.invoiceOK {
		return domain.Invoice{}, domain.ErrInvoiceNotFound
	}
	return f.invoice, nil
}

func (f *fake) RecordPayment(_ context.Context, p domain.Payment) (domain.Payment, bool, error) {
	f.payments = append(f.payments, p)
	// created=false on a re-read, exactly as the real ON CONFLICT DO NOTHING.
	return p, !f.paymentExists, nil
}

func (f *fake) GetPaymentByChainRef(context.Context, domain.Network, string, int32) (domain.Payment, error) {
	if !f.paymentOK {
		return domain.Payment{}, domain.ErrNotFound
	}
	return f.payment, nil
}

func (f *fake) MarkPaymentRemoved(_ context.Context, _ domain.Network, txHash string, _ int32) (domain.Payment, error) {
	f.removed = append(f.removed, txHash)
	at := fixedNow
	out := f.payment
	out.RemovedAt = &at
	return out, nil
}

func (f *fake) TransitionInvoice(_ context.Context, _ uuid.UUID, from, to domain.InvoiceStatus,
	_ int32, paidAt *time.Time, holdUntil time.Time,
) (domain.Invoice, error) {
	if f.transitionFails {
		return domain.Invoice{}, domain.ErrInvalidTransition
	}
	f.transitions = append(f.transitions,
		transition{from: from, to: to, paidAt: paidAt, holdUntil: holdUntil})
	out := f.invoice
	out.Status = to
	out.PaidAt = paidAt
	return out, nil
}

func (f *fake) RecordOrphanTransfer(_ context.Context, o domain.OrphanTransfer) error {
	f.orphans = append(f.orphans, o)
	return nil
}

func (f *fake) EnqueueWebhookEvent(_ context.Context, e postgresadapter.WebhookEvent) error {
	if f.enqueueFails {
		return errors.New("outbox is unavailable")
	}
	f.events = append(f.events, e)
	return nil
}

// WithTx runs the callback against the same fake, and discards everything it
// wrote if it fails — which is what a rollback looks like from outside.
func (f *fake) WithTx(ctx context.Context, fn func(tx Postgres) error) error {
	before := *f
	if err := fn(f); err != nil {
		payments, orphans, events, removed, transitions :=
			before.payments, before.orphans, before.events, before.removed, before.transitions
		*f = before
		f.payments, f.orphans, f.events, f.removed, f.transitions =
			payments, orphans, events, removed, transitions
		return err
	}
	return nil
}

// notifier stands in for the webhook sender: the engine only asks whether events
// are worth queueing at all.
type notifier struct{ on bool }

func (n notifier) Enabled() bool { return n.on }

// service builds the engine. The string argument used to be the webhook URL and
// is now only a switch: a non-empty value means notifications are on, matching
// how every existing call site reads.
func service(f *fake, webhookURL string) *Service {
	return &Service{
		postgres: f,
		cfg: invoicecfg.Config{
			AmountHold:    config.Duration(2 * time.Hour),
			PayAddressBSC: payAddress,
		},
		webhooks: notifier{on: webhookURL != ""},
		logger:   zap.NewNop().Sugar(),
		now:      func() time.Time { return fixedNow },
		newID:    func() uuid.UUID { return eventID },
	}
}

func observed(value *big.Int, final bool) Observed {
	return Observed{
		Network: domain.NetworkBSC, TxHash: "0xtx1", LogIndex: 0,
		Contract: usdtOnBSC, From: "0xsender", To: payAddress,
		Value: value, BlockNumber: 500, BlockTime: fixedNow, Final: final,
	}
}

func exact() *big.Int { return big.NewInt(1_000_000_000_000_000_000) }

func TestApplyCreditsAnExactPayment(t *testing.T) {
	f := &fake{asset: asset(), assetOK: true, invoice: invoiceIn(domain.InvoiceStatusPending), invoiceOK: true}

	got, err := service(f, "https://hook").Apply(context.Background(), observed(exact(), false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Outcome != OutcomeCredited {
		t.Errorf("outcome = %q", got.Outcome)
	}
	if got.Status != domain.InvoiceStatusDetected {
		t.Errorf("status = %q, want detected for a non-final transfer", got.Status)
	}
	if len(f.payments) != 1 {
		t.Fatalf("recorded %d payments", len(f.payments))
	}
	if len(f.transitions) != 1 || f.transitions[0].to != domain.InvoiceStatusDetected {
		t.Fatalf("transitions = %+v", f.transitions)
	}
	if len(f.events) != 1 || f.events[0].Event != EventDetected {
		t.Fatalf("events = %+v", f.events)
	}
}

// A transfer already past the finality line on first sight goes straight to
// confirmed — after downtime that is the normal case, and inventing an
// intermediate state it was never observed in would be a lie.
func TestApplyGoesStraightToConfirmedWhenFinal(t *testing.T) {
	f := &fake{asset: asset(), assetOK: true, invoice: invoiceIn(domain.InvoiceStatusPending), invoiceOK: true}

	got, err := service(f, "https://hook").Apply(context.Background(), observed(exact(), true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Status != domain.InvoiceStatusConfirmed {
		t.Fatalf("status = %q", got.Status)
	}
	if f.transitions[0].from != domain.InvoiceStatusPending {
		t.Errorf("from = %q", f.transitions[0].from)
	}
	if f.transitions[0].paidAt == nil {
		t.Error("paid_at should be stamped on confirmation")
	}
	if f.events[0].Event != EventConfirmed {
		t.Errorf("event = %q", f.events[0].Event)
	}
}

// The branch that would break if the code decided anything from
// RecordPayment's `created` flag: a transfer first seen unconfirmed comes back
// final later with created=false, and must still settle the invoice.
func TestApplyAdvancesDetectedToConfirmedOnARepeat(t *testing.T) {
	f := &fake{
		asset: asset(), assetOK: true,
		invoice: invoiceIn(domain.InvoiceStatusDetected), invoiceOK: true,
		paymentExists: true, // the payment row is already there
	}

	got, err := service(f, "https://hook").Apply(context.Background(), observed(exact(), true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Outcome != OutcomeCredited || got.Status != domain.InvoiceStatusConfirmed {
		t.Fatalf("outcome = %q status = %q — a known payment that turned final must settle",
			got.Outcome, got.Status)
	}
	if len(f.transitions) != 1 || f.transitions[0].from != domain.InvoiceStatusDetected {
		t.Fatalf("transitions = %+v", f.transitions)
	}
}

// Re-reading a transfer whose invoice is already in the right state must do
// nothing at all — no second payment, no repeated event.
func TestApplyIsIdempotentWhenNothingChanges(t *testing.T) {
	f := &fake{
		asset: asset(), assetOK: true,
		invoice: invoiceIn(domain.InvoiceStatusDetected), invoiceOK: true,
		paymentExists: true,
	}

	got, err := service(f, "https://hook").Apply(context.Background(), observed(exact(), false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Outcome != OutcomeUnchanged {
		t.Errorf("outcome = %q", got.Outcome)
	}
	if len(f.transitions) != 0 {
		t.Errorf("transitions = %+v, want none", f.transitions)
	}
	if len(f.events) != 0 {
		t.Errorf("events = %+v, want none", f.events)
	}
}

func TestApplyOrphansByReason(t *testing.T) {
	tests := map[string]struct {
		setup func(*fake)
		want  domain.OrphanReason
	}{
		"unknown token": {
			setup: func(f *fake) { f.assetOK = false },
			want:  domain.OrphanUnknownAsset,
		},
		"no invoice holds that amount": {
			setup: func(f *fake) { f.assetOK, f.invoiceOK = true, false },
			want:  domain.OrphanNoInvoice,
		},
		"invoice already expired": {
			setup: func(f *fake) {
				f.assetOK, f.invoiceOK = true, true
				f.invoice = invoiceIn(domain.InvoiceStatusExpired)
			},
			want: domain.OrphanInvoiceTerminal,
		},
		"invoice already cancelled": {
			setup: func(f *fake) {
				f.assetOK, f.invoiceOK = true, true
				f.invoice = invoiceIn(domain.InvoiceStatusCancelled)
			},
			want: domain.OrphanInvoiceTerminal,
		},
		"invoice already paid": {
			setup: func(f *fake) {
				f.assetOK, f.invoiceOK = true, true
				f.invoice = invoiceIn(domain.InvoiceStatusConfirmed)
			},
			want: domain.OrphanInvoiceTerminal,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := &fake{asset: asset()}
			tc.setup(f)

			got, err := service(f, "https://hook").Apply(context.Background(), observed(exact(), false))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.Outcome != OutcomeOrphaned || got.Reason != tc.want {
				t.Fatalf("outcome = %q reason = %q, want orphaned/%q", got.Outcome, got.Reason, tc.want)
			}
			if len(f.orphans) != 1 || f.orphans[0].Reason != tc.want {
				t.Fatalf("orphans = %+v", f.orphans)
			}
			// Money that could not be attributed must not touch an invoice.
			if len(f.payments) != 0 || len(f.transitions) != 0 {
				t.Fatalf("an orphan wrote %d payments and %d transitions",
					len(f.payments), len(f.transitions))
			}
		})
	}
}

// An orphan for an unknown token still has to be identifiable, so the contract
// address is carried even when the asset id is not.
func TestOrphanForUnknownTokenKeepsTheContract(t *testing.T) {
	f := &fake{asset: asset()}

	if _, err := service(f, "").Apply(context.Background(), observed(exact(), false)); err != nil {
		t.Fatal(err)
	}

	if f.orphans[0].ContractAddress != usdtOnBSC {
		t.Errorf("contract = %q", f.orphans[0].ContractAddress)
	}
	if f.orphans[0].AssetID != 0 {
		t.Errorf("asset id = %d, want zero for an unrecognised token", f.orphans[0].AssetID)
	}
}

// Losing the compare-and-set race is not an error: the payment is recorded, and
// whoever got there first has already moved the invoice.
func TestApplyToleratesLosingTheTransitionRace(t *testing.T) {
	f := &fake{
		asset: asset(), assetOK: true,
		invoice: invoiceIn(domain.InvoiceStatusPending), invoiceOK: true,
		transitionFails: true,
	}

	got, err := service(f, "https://hook").Apply(context.Background(), observed(exact(), false))
	if err != nil {
		t.Fatalf("losing the race must not fail the tick: %v", err)
	}
	if got.Outcome != OutcomeUnchanged {
		t.Errorf("outcome = %q", got.Outcome)
	}
	if len(f.payments) != 1 {
		t.Errorf("the payment should still be recorded, got %d", len(f.payments))
	}
}

func TestApplyRejectsNonPositiveValue(t *testing.T) {
	for name, v := range map[string]*big.Int{
		"nil":      nil,
		"zero":     big.NewInt(0),
		"negative": big.NewInt(-1),
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{asset: asset(), assetOK: true}
			if _, err := service(f, "").Apply(context.Background(), observed(v, false)); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

// An outbox that only ever grows is worse than no outbox, so with notifications
// switched off nothing is queued at all — but the credit still happens.
func TestNotificationsOffQueueNothingButStillCredit(t *testing.T) {
	f := &fake{asset: asset(), assetOK: true, invoice: invoiceIn(domain.InvoiceStatusPending), invoiceOK: true}

	got, err := service(f, "").Apply(context.Background(), observed(exact(), false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Outcome != OutcomeCredited {
		t.Errorf("outcome = %q", got.Outcome)
	}
	if len(f.events) != 0 {
		t.Fatalf("events = %+v, want none", f.events)
	}
}

func TestFailingToQueueTheEventRollsBackTheCredit(t *testing.T) {
	f := &fake{
		asset: asset(), assetOK: true,
		invoice: invoiceIn(domain.InvoiceStatusPending), invoiceOK: true,
		enqueueFails: true,
	}

	if _, err := service(f, "https://hook").Apply(context.Background(), observed(exact(), false)); err == nil {
		t.Fatal("want the transaction to fail")
	}
	if len(f.payments) != 0 || len(f.transitions) != 0 {
		t.Fatalf("rollback left %d payments and %d transitions behind",
			len(f.payments), len(f.transitions))
	}
}

func TestRevokeReturnsTheInvoiceToPending(t *testing.T) {
	inv := invoiceIn(domain.InvoiceStatusDetected)
	f := &fake{
		asset: asset(), assetOK: true, invoice: inv, invoiceOK: true,
		paymentOK: true,
		payment:   domain.Payment{Network: domain.NetworkBSC, TxHash: "0xtx1", InvoiceID: &invoiceID},
	}

	if err := service(f, "https://hook").Revoke(context.Background(), domain.NetworkBSC, "0xtx1", 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(f.removed) != 1 || f.removed[0] != "0xtx1" {
		t.Fatalf("removed = %v", f.removed)
	}
	if len(f.transitions) != 1 ||
		f.transitions[0].from != domain.InvoiceStatusDetected ||
		f.transitions[0].to != domain.InvoiceStatusPending {
		t.Fatalf("transitions = %+v, want detected -> pending", f.transitions)
	}
	if len(f.events) != 1 || f.events[0].Event != EventReverted {
		t.Fatalf("events = %+v", f.events)
	}
}

// A confirmed payment being withdrawn means the chain reorganised past its own
// finality line. Un-crediting settled money silently would be worse than
// refusing to act and shouting.
func TestRevokeRefusesToUncreditAConfirmedInvoice(t *testing.T) {
	f := &fake{
		asset: asset(), assetOK: true,
		invoice: invoiceIn(domain.InvoiceStatusConfirmed), invoiceOK: true,
		paymentOK: true,
		payment:   domain.Payment{Network: domain.NetworkBSC, TxHash: "0xtx1", InvoiceID: &invoiceID},
	}

	if err := service(f, "https://hook").Revoke(context.Background(), domain.NetworkBSC, "0xtx1", 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(f.transitions) != 0 {
		t.Fatalf("a confirmed invoice must not be moved, got %+v", f.transitions)
	}
	// The payment is still marked: the record of the withdrawal is the point.
	if len(f.removed) != 1 {
		t.Errorf("removed = %v", f.removed)
	}
}

func TestRevokeIsIdempotent(t *testing.T) {
	at := fixedNow
	f := &fake{
		asset: asset(), assetOK: true, invoice: invoiceIn(domain.InvoiceStatusPending), invoiceOK: true,
		paymentOK: true,
		payment: domain.Payment{
			Network: domain.NetworkBSC, TxHash: "0xtx1",
			InvoiceID: &invoiceID, RemovedAt: &at,
		},
	}

	if err := service(f, "https://hook").Revoke(context.Background(), domain.NetworkBSC, "0xtx1", 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.removed) != 0 || len(f.transitions) != 0 {
		t.Fatalf("an already-revoked payment was touched again: removed=%v transitions=%+v",
			f.removed, f.transitions)
	}
}

// A removal for a transfer that was never credited — an orphan, or a log for
// somebody else — is simply nothing to undo.
func TestRevokeIgnoresAnUnknownTransfer(t *testing.T) {
	f := &fake{asset: asset(), assetOK: true}

	if err := service(f, "https://hook").Revoke(context.Background(), domain.NetworkBSC, "0xnope", 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.removed) != 0 {
		t.Errorf("removed = %v", f.removed)
	}
}

// TRON supplies no block number; zero must reach storage as absence, not as
// block zero.
func TestApplyCarriesAbsentBlockNumberForTron(t *testing.T) {
	f := &fake{asset: asset(), assetOK: true, invoice: invoiceIn(domain.InvoiceStatusPending), invoiceOK: true}

	in := observed(exact(), false)
	in.Network = domain.NetworkTron
	in.BlockNumber = 0

	if _, err := service(f, "").Apply(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if f.payments[0].BlockNumber != 0 {
		t.Fatalf("block number = %d", f.payments[0].BlockNumber)
	}
}

// The hold is extended past the invoice's own end so a duplicate or late
// transfer cannot pay whichever invoice inherits the amount.
func TestCreditExtendsTheAmountHold(t *testing.T) {
	f := &fake{asset: asset(), assetOK: true, invoice: invoiceIn(domain.InvoiceStatusPending), invoiceOK: true}

	if _, err := service(f, "").Apply(context.Background(), observed(exact(), true)); err != nil {
		t.Fatal(err)
	}
	if len(f.transitions) != 1 {
		t.Fatalf("transitions = %+v", f.transitions)
	}

	want := fixedNow.Add(2 * time.Hour) // AmountHold from the fixture
	if got := f.transitions[0].holdUntil; !got.Equal(want) {
		t.Fatalf("hold until = %s, want %s", got, want)
	}
}

// Found on the bench: the BSC watcher rewinds its cursor on every start, so it
// re-observes transfers it already credited. Before this was handled, such a
// re-read whose invoice had since become terminal was filed as an orphan — a
// duplicate record of money already credited, appearing afresh on every restart.
func TestReReadOfACreditedTransferIsNotAnOrphan(t *testing.T) {
	f := &fake{
		asset: asset(), assetOK: true,
		invoice: invoiceIn(domain.InvoiceStatusConfirmed), invoiceOK: true,
		paymentOK: true,
		payment: domain.Payment{
			Network: domain.NetworkBSC, TxHash: "0xtx1", LogIndex: 0,
			InvoiceID: &invoiceID, BlockNumber: 500,
		},
	}

	got, err := service(f, "https://hook").Apply(context.Background(), observed(exact(), true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(f.orphans) != 0 {
		t.Fatalf("a re-read of a credited transfer was filed as an orphan: %+v", f.orphans)
	}
	if got.Outcome == OutcomeOrphaned {
		t.Fatalf("outcome = %q", got.Outcome)
	}
	if len(f.payments) != 0 {
		t.Errorf("no second payment should be written, got %d", len(f.payments))
	}
}

// A re-read that is still not final does nothing at all — the quiet path taken
// on almost every tick.
func TestReReadOfANonFinalTransferIsQuiet(t *testing.T) {
	f := &fake{
		asset: asset(), assetOK: true,
		invoice: invoiceIn(domain.InvoiceStatusDetected), invoiceOK: true,
		paymentOK: true,
		payment: domain.Payment{
			Network: domain.NetworkBSC, TxHash: "0xtx1", InvoiceID: &invoiceID,
		},
	}

	got, err := service(f, "https://hook").Apply(context.Background(), observed(exact(), false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Outcome != OutcomeUnchanged {
		t.Errorf("outcome = %q", got.Outcome)
	}
	if len(f.transitions) != 0 || len(f.events) != 0 {
		t.Errorf("nothing should have been written: %+v %+v", f.transitions, f.events)
	}
}

// A re-read that has since become final settles the invoice.
func TestReReadThatTurnedFinalSettles(t *testing.T) {
	f := &fake{
		asset: asset(), assetOK: true,
		invoice: invoiceIn(domain.InvoiceStatusDetected), invoiceOK: true,
		paymentOK: true,
		payment: domain.Payment{
			Network: domain.NetworkBSC, TxHash: "0xtx1", InvoiceID: &invoiceID,
		},
	}

	got, err := service(f, "https://hook").Apply(context.Background(), observed(exact(), true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != domain.InvoiceStatusConfirmed {
		t.Fatalf("status = %q", got.Status)
	}
	if len(f.events) != 1 || f.events[0].Event != EventConfirmed {
		t.Fatalf("events = %+v", f.events)
	}
}

// A transfer that was withdrawn and has reappeared on chain is new money again.
func TestReappearedTransferTakesTheNormalPath(t *testing.T) {
	at := fixedNow
	f := &fake{
		asset: asset(), assetOK: true,
		invoice: invoiceIn(domain.InvoiceStatusPending), invoiceOK: true,
		paymentOK: true,
		payment: domain.Payment{
			Network: domain.NetworkBSC, TxHash: "0xtx1",
			InvoiceID: &invoiceID, RemovedAt: &at,
		},
	}

	got, err := service(f, "https://hook").Apply(context.Background(), observed(exact(), false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Outcome != OutcomeCredited {
		t.Fatalf("outcome = %q, want it credited again", got.Outcome)
	}
}
