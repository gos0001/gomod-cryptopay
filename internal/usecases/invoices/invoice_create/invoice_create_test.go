package invoice_create

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/invoicecfg"
	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

var (
	fixedTime = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	fixedID   = uuid.MustParse("11111111-2222-3333-4444-555555555555")
)

func tronUSDT(id int64) domain.Asset {
	return domain.Asset{
		ID: id, Network: domain.NetworkTron, Symbol: "USDT",
		ContractAddress: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
		Decimals:        6, Step: big.NewInt(100), NonceMax: 1000, Enabled: true,
	}
}

type fakePostgres struct {
	bySymbol   []domain.Asset
	byContract domain.Asset
	byID       domain.Asset
	existing   domain.Invoice

	created    postgresadapter.CreateInvoiceInput
	createErr  error
	lookupErr  error
	existErr   error
	createCall int
}

func (f *fakePostgres) ListAssetsByNetworkAndSymbol(context.Context, domain.Network, string) ([]domain.Asset, error) {
	return f.bySymbol, f.lookupErr
}

func (f *fakePostgres) GetEnabledAssetByContract(_ context.Context, _ domain.Network, contract string) (domain.Asset, error) {
	if f.lookupErr != nil {
		return domain.Asset{}, f.lookupErr
	}
	f.byContract.ContractAddress = contract
	return f.byContract, nil
}

func (f *fakePostgres) GetAssetByID(context.Context, int64) (domain.Asset, error) {
	return f.byID, nil
}

func (f *fakePostgres) CreateInvoice(_ context.Context, in postgresadapter.CreateInvoiceInput) (domain.Invoice, error) {
	f.createCall++
	f.created = in
	if f.createErr != nil {
		return domain.Invoice{}, f.createErr
	}
	return domain.Invoice{
		ID: in.ID, ExternalID: in.ExternalID, AssetID: in.Asset.ID,
		Network: in.Asset.Network, PayAddress: in.PayAddress,
		BaseAmount: in.BaseAmount,
		PayAmount:  new(big.Int).Add(in.BaseAmount, in.Asset.Step),
		Status:     domain.InvoiceStatusPending,
		ExpiresAt:  in.ExpiresAt,
	}, nil
}

func (f *fakePostgres) GetInvoiceByExternalID(context.Context, string) (domain.Invoice, error) {
	return f.existing, f.existErr
}

func newUsecase(pg Postgres) *Usecase {
	return &Usecase{
		postgres: pg,
		cfg: invoicecfg.Config{
			TTL:            config.Duration(30 * time.Minute),
			AmountHold:     config.Duration(2 * time.Hour),
			PayAddressTron: "TPayAddress",
			PayAddressBSC:  "0xpay",
		},
		now:   func() time.Time { return fixedTime },
		newID: func() uuid.UUID { return fixedID },
	}
}

func validInput() Input {
	in := Input{Network: "tron", Symbol: "USDT", Amount: "10.50"}
	if err := in.Validate(); err != nil {
		panic(err)
	}
	return in
}

func TestExecuteCreatesInvoice(t *testing.T) {
	pg := &fakePostgres{bySymbol: []domain.Asset{tronUSDT(1)}}
	out, err := newUsecase(pg).Execute(context.Background(), validInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !out.Created {
		t.Error("a first call should report Created")
	}
	if out.Invoice.PayAddress != "TPayAddress" {
		t.Errorf("pay_address = %q", out.Invoice.PayAddress)
	}
	// 10.50 at 6 decimals is 10_500_000; the fake adds one step of 100.
	if out.Invoice.PayAmountUnits != "10500100" {
		t.Errorf("pay_amount_units = %q", out.Invoice.PayAmountUnits)
	}
	if out.Invoice.Amount != "10.5" {
		t.Errorf("amount = %q", out.Invoice.Amount)
	}
	if pg.created.BaseAmount.Cmp(big.NewInt(10_500_000)) != 0 {
		t.Errorf("stored base amount = %s", pg.created.BaseAmount)
	}
}

// The hold has to outlive the invoice, or a transfer sent just before expiry
// pays whoever inherits the amount.
func TestExecuteHoldsAmountPastExpiry(t *testing.T) {
	pg := &fakePostgres{bySymbol: []domain.Asset{tronUSDT(1)}}
	if _, err := newUsecase(pg).Execute(context.Background(), validInput()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantExpiry := fixedTime.Add(30 * time.Minute)
	if !pg.created.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("expires_at = %s, want %s", pg.created.ExpiresAt, wantExpiry)
	}
	if want := wantExpiry.Add(2 * time.Hour); !pg.created.AmountHoldUntil.Equal(want) {
		t.Errorf("amount_hold_until = %s, want %s", pg.created.AmountHoldUntil, want)
	}
}

func TestExecuteHonoursExpiresIn(t *testing.T) {
	in := Input{Network: "tron", Symbol: "USDT", Amount: "1", ExpiresIn: "45m"}
	if err := in.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	pg := &fakePostgres{bySymbol: []domain.Asset{tronUSDT(1)}}
	if _, err := newUsecase(pg).Execute(context.Background(), in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := fixedTime.Add(45 * time.Minute); !pg.created.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %s, want %s", pg.created.ExpiresAt, want)
	}
}

func TestExecuteUnknownSymbolIsNotFound(t *testing.T) {
	pg := &fakePostgres{bySymbol: nil}
	_, err := newUsecase(pg).Execute(context.Background(), validInput())
	if !errors.Is(err, domain.ErrAssetNotFound) {
		t.Fatalf("got %v", err)
	}
}

// Picking one silently would issue an invoice in a token the caller did not
// ask for, and nothing downstream could tell.
func TestExecuteAmbiguousSymbolNamesTheCandidates(t *testing.T) {
	second := tronUSDT(2)
	second.ContractAddress = "TOtherContract"
	pg := &fakePostgres{bySymbol: []domain.Asset{tronUSDT(1), second}}

	_, err := newUsecase(pg).Execute(context.Background(), validInput())

	var ambiguous *AmbiguousAssetError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, ErrAmbiguousAsset) {
		t.Error("should unwrap to the sentinel")
	}
	if len(ambiguous.Contracts) != 2 {
		t.Fatalf("contracts = %v", ambiguous.Contracts)
	}
	if !strings.Contains(ambiguous.Error(), "TOtherContract") {
		t.Errorf("message should list the candidates: %s", ambiguous.Error())
	}
}

// The chain reports EVM addresses lowercase, so a checksummed one from the
// caller still has to match what was stored.
func TestExecuteLowercasesEVMContractLookup(t *testing.T) {
	bsc := domain.Asset{
		ID: 2, Network: domain.NetworkBSC, Symbol: "USDT",
		Decimals: 18, Step: big.NewInt(1), NonceMax: 10, Enabled: true,
	}
	pg := &fakePostgres{byContract: bsc}

	in := Input{Network: "bsc", ContractAddress: "0xAbCdEf", Amount: "1"}
	if err := in.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := newUsecase(pg).Execute(context.Background(), in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := pg.created.Asset.ContractAddress; got != "0xabcdef" {
		t.Fatalf("looked up %q, want the lowercased form", got)
	}
}

// A TRON address is base58, where case carries meaning.
func TestExecuteLeavesTronContractLookupAlone(t *testing.T) {
	pg := &fakePostgres{byContract: tronUSDT(1)}

	in := Input{Network: "tron", ContractAddress: "TR7NHqjeKQ", Amount: "1"}
	if err := in.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := newUsecase(pg).Execute(context.Background(), in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := pg.created.Asset.ContractAddress; got != "TR7NHqjeKQ" {
		t.Fatalf("looked up %q, want it untouched", got)
	}
}

func TestExecuteRejectsOverPreciseAmount(t *testing.T) {
	pg := &fakePostgres{bySymbol: []domain.Asset{tronUSDT(1)}}

	in := Input{Network: "tron", Symbol: "USDT", Amount: "10.5000001"}
	if err := in.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	_, err := newUsecase(pg).Execute(context.Background(), in)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
	// The reason matters: "amount is invalid" would leave the caller guessing.
	if !strings.Contains(err.Error(), "fractional") {
		t.Errorf("message should explain the precision problem: %v", err)
	}
}

func TestExecuteRejectsZeroAmount(t *testing.T) {
	pg := &fakePostgres{bySymbol: []domain.Asset{tronUSDT(1)}}

	in := Input{Network: "tron", Symbol: "USDT", Amount: "0"}
	if err := in.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := newUsecase(pg).Execute(context.Background(), in); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
}

// The point of the idempotency key: a merchant whose call timed out retries and
// gets the same invoice, not a second one.
func TestExecuteReplaysMatchingExternalID(t *testing.T) {
	asset := tronUSDT(1)
	pg := &fakePostgres{
		bySymbol:  []domain.Asset{asset},
		byID:      asset,
		createErr: domain.ErrExternalIDTaken,
		existing: domain.Invoice{
			ID: fixedID, ExternalID: "order-1", AssetID: 1,
			Network: domain.NetworkTron, PayAddress: "TPayAddress",
			BaseAmount: big.NewInt(10_500_000), PayAmount: big.NewInt(10_500_100),
			Status: domain.InvoiceStatusPending,
		},
	}

	in := validInput()
	in.ExternalID = "order-1"

	out, err := newUsecase(pg).Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Created {
		t.Error("a replay must not report Created")
	}
	if out.Invoice.ID != fixedID.String() {
		t.Errorf("id = %s", out.Invoice.ID)
	}
}

// Returning an invoice for a different amount under the same key would hide a
// real merchant bug until reconciliation.
func TestExecuteRejectsExternalIDReuseWithDifferentAmount(t *testing.T) {
	asset := tronUSDT(1)
	pg := &fakePostgres{
		bySymbol:  []domain.Asset{asset},
		byID:      asset,
		createErr: domain.ErrExternalIDTaken,
		existing: domain.Invoice{
			ID: fixedID, ExternalID: "order-1", AssetID: 1,
			BaseAmount: big.NewInt(999), PayAmount: big.NewInt(1099),
		},
	}

	in := validInput()
	in.ExternalID = "order-1"

	if _, err := newUsecase(pg).Execute(context.Background(), in); !errors.Is(err, domain.ErrExternalIDTaken) {
		t.Fatalf("got %v", err)
	}
}

func TestExecuteRejectsExternalIDReuseAcrossAssets(t *testing.T) {
	asset := tronUSDT(1)
	pg := &fakePostgres{
		bySymbol:  []domain.Asset{asset},
		byID:      asset,
		createErr: domain.ErrExternalIDTaken,
		existing: domain.Invoice{
			ID: fixedID, ExternalID: "order-1", AssetID: 7,
			BaseAmount: big.NewInt(10_500_000),
		},
	}

	in := validInput()
	in.ExternalID = "order-1"

	if _, err := newUsecase(pg).Execute(context.Background(), in); !errors.Is(err, domain.ErrExternalIDTaken) {
		t.Fatalf("got %v", err)
	}
}

func TestExecutePassesThroughExhaustion(t *testing.T) {
	pg := &fakePostgres{
		bySymbol:  []domain.Asset{tronUSDT(1)},
		createErr: domain.ErrAmountSpaceExhausted,
	}

	_, err := newUsecase(pg).Execute(context.Background(), validInput())
	if !errors.Is(err, domain.ErrAmountSpaceExhausted) {
		t.Fatalf("got %v", err)
	}
}

// Startup validation should prevent this, but issuing an invoice nobody can pay
// is worse than refusing.
func TestExecuteRefusesWithoutReceivingAddress(t *testing.T) {
	uc := newUsecase(&fakePostgres{bySymbol: []domain.Asset{tronUSDT(1)}})
	uc.cfg.PayAddressTron = ""

	if _, err := uc.Execute(context.Background(), validInput()); err == nil {
		t.Fatal("want an error")
	}
}

func TestValidate(t *testing.T) {
	tests := map[string]struct {
		in    Input
		wants string
	}{
		"no network":       {Input{Symbol: "USDT", Amount: "1"}, "network"},
		"bad network":      {Input{Network: "solana", Symbol: "U", Amount: "1"}, "network"},
		"no asset naming":  {Input{Network: "tron", Amount: "1"}, "symbol or contract_address"},
		"no amount":        {Input{Network: "tron", Symbol: "USDT"}, "amount"},
		"bad expires_in":   {Input{Network: "tron", Symbol: "U", Amount: "1", ExpiresIn: "soon"}, "expires_in"},
		"broken metadata":  {Input{Network: "tron", Symbol: "U", Amount: "1", Metadata: []byte("{oops")}, "metadata"},
		"long external_id": {Input{Network: "tron", Symbol: "U", Amount: "1", ExternalID: strings.Repeat("x", 201)}, "external_id"},
		"long description": {Input{Network: "tron", Symbol: "U", Amount: "1", Description: strings.Repeat("x", 501)}, "description"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			in := tc.in
			err := in.Validate()
			if err == nil {
				t.Fatal("want an error")
			}
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Errorf("should wrap ErrInvalidInput: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("message should mention %q: %v", tc.wants, err)
			}
		})
	}
}

func TestValidateTrimsAndAcceptsMinimalInput(t *testing.T) {
	in := Input{Network: "  tron ", Symbol: " USDT ", Amount: " 10.50 "}
	if err := in.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if in.Network != "tron" || in.Symbol != "USDT" || in.Amount != "10.50" {
		t.Fatalf("got %+v", in)
	}
}

// The public path exists so a customer's browser can create an invoice without
// holding an API key. These three restrictions are what make that safe.

// external_id is the idempotency key: accepting it anonymously would let anyone
// guess "order-42" and be handed that invoice in full — a read of somebody else's
// order dressed up as a write.
func TestRestrictToPublicRefusesExternalID(t *testing.T) {
	in := Input{Network: "tron", Symbol: "USDT", Amount: "10", ExternalID: "order-42"}
	if err := in.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	err := in.RestrictToPublic()
	if err == nil {
		t.Fatal("want an error")
	}
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("error should wrap ErrInvalidInput: %v", err)
	}
	if !strings.Contains(err.Error(), "external_id") {
		t.Errorf("message should name the field: %v", err)
	}
}

// Up to 8 KiB of arbitrary JSON per invoice, from an anonymous caller, is storage
// abuse — and a browser has nothing to put there.
func TestRestrictToPublicRefusesMetadata(t *testing.T) {
	in := Input{Network: "tron", Symbol: "USDT", Amount: "10", Metadata: []byte(`{"a":1}`)}
	if err := in.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	if err := in.RestrictToPublic(); err == nil {
		t.Fatal("want an error")
	}
}

// Ignored rather than refused: a merchant's page may well send it, and there is
// no reason to fail the payment over it. Capping at the configured TTL stops an
// anonymous caller from holding a nonce — and with it an amount — for a day.
func TestRestrictToPublicIgnoresExpiresIn(t *testing.T) {
	in := Input{Network: "tron", Symbol: "USDT", Amount: "10", ExpiresIn: "24h"}
	if err := in.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if in.expiresIn == 0 {
		t.Fatal("precondition: Validate should have parsed expires_in")
	}

	if err := in.RestrictToPublic(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if in.expiresIn != 0 || in.ExpiresIn != "" {
		t.Fatalf("expires_in survived: %q / %s", in.ExpiresIn, in.expiresIn)
	}
}

// Everything a payer legitimately needs still goes through.
func TestRestrictToPublicAllowsAnOrdinaryRequest(t *testing.T) {
	in := Input{Network: "tron", Symbol: "USDT", Amount: "10.50", Description: "Order for a widget"}
	if err := in.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	if err := in.RestrictToPublic(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if in.Amount != "10.50" || in.Description == "" {
		t.Fatalf("a legitimate field was dropped: %+v", in)
	}
}
