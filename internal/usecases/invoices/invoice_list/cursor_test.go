package invoice_list

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
)

func TestCursorRoundTrips(t *testing.T) {
	want := cursor{
		CreatedAt: time.Date(2026, 8, 13, 12, 0, 0, 123456789, time.UTC),
		ID:        uuid.MustParse("11111111-2222-3333-4444-555555555555"),
	}

	got, err := decodeCursor(want.encode())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("created_at = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
	if got.ID != want.ID {
		t.Errorf("id = %s, want %s", got.ID, want.ID)
	}
}

// Nanosecond precision has to survive: two invoices created in the same
// microsecond are exactly the case the id tiebreak exists for, and a cursor
// that rounds would skip or repeat one of them.
func TestCursorKeepsNanoseconds(t *testing.T) {
	want := time.Date(2026, 8, 13, 12, 0, 0, 999999999, time.UTC)

	got, err := decodeCursor(cursor{CreatedAt: want, ID: uuid.New()}.encode())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CreatedAt.Nanosecond() != want.Nanosecond() {
		t.Fatalf("nanoseconds = %d, want %d", got.CreatedAt.Nanosecond(), want.Nanosecond())
	}
}

// Quietly restarting from page one turns a batch walk into an endless loop over
// the first page, with nothing in the response to say so.
func TestDecodeCursorRejectsGarbage(t *testing.T) {
	tests := map[string]string{
		"not base64":   "!!!!",
		"not json":     "aGVsbG8",                                // "hello"
		"empty object": "e30",                                    // "{}"
		"no id":        "eyJ0IjoiMjAyNi0wOC0xM1QxMjowMDowMFoifQ", // {"t":"..."}
	}

	for name, s := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeCursor(s)
			if err == nil {
				t.Fatal("want an error")
			}
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Errorf("should wrap ErrInvalidInput: %v", err)
			}
		})
	}
}

// The cursor is base64url so it survives a query string unescaped.
func TestCursorIsURLSafe(t *testing.T) {
	for i := 0; i < 50; i++ {
		s := cursor{CreatedAt: time.Now().Add(time.Duration(i)), ID: uuid.New()}.encode()
		if strings.ContainsAny(s, "+/=") {
			t.Fatalf("cursor %q contains characters that need escaping", s)
		}
	}
}

type fakePostgres struct {
	invoices []domain.Invoice
	assets   []domain.Asset
	gotSize  int32
}

func (f *fakePostgres) ListInvoices(_ context.Context, filter postgresadapter.ListInvoicesFilter) ([]domain.Invoice, error) {
	f.gotSize = filter.PageSize
	if int(filter.PageSize) < len(f.invoices) {
		return f.invoices[:filter.PageSize], nil
	}
	return f.invoices, nil
}

func (f *fakePostgres) ListAssets(context.Context, bool) ([]domain.Asset, error) {
	return f.assets, nil
}

func invoices(n int) []domain.Invoice {
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	out := make([]domain.Invoice, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, domain.Invoice{
			ID:         uuid.New(),
			AssetID:    1,
			BaseAmount: big.NewInt(1),
			PayAmount:  big.NewInt(2),
			Status:     domain.InvoiceStatusPending,
			CreatedAt:  base.Add(-time.Duration(i) * time.Minute),
		})
	}
	return out
}

func run(t *testing.T, pg *fakePostgres, in Input) Output {
	t.Helper()
	if err := in.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	out, err := (&Usecase{postgres: pg}).Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return out
}

// One row beyond the limit is fetched so a full last page is not mistaken for
// a page with more behind it.
func TestExecuteAsksForOneExtraRow(t *testing.T) {
	pg := &fakePostgres{invoices: invoices(3), assets: []domain.Asset{{ID: 1, Decimals: 6}}}

	out := run(t, pg, Input{Limit: 2})

	if pg.gotSize != 3 {
		t.Errorf("page size = %d, want limit+1", pg.gotSize)
	}
	if len(out.Invoices) != 2 {
		t.Errorf("returned %d invoices, want the limit", len(out.Invoices))
	}
	if out.NextCursor == "" {
		t.Error("want a next cursor when more rows exist")
	}
}

// A page that is exactly full and also last must not offer a cursor, or the
// client fetches an empty page to find out.
func TestExecuteOmitsCursorOnExactlyFullLastPage(t *testing.T) {
	pg := &fakePostgres{invoices: invoices(2), assets: []domain.Asset{{ID: 1, Decimals: 6}}}

	out := run(t, pg, Input{Limit: 2})

	if len(out.Invoices) != 2 {
		t.Fatalf("returned %d invoices", len(out.Invoices))
	}
	if out.NextCursor != "" {
		t.Error("no more rows, so no cursor")
	}
}

func TestExecuteCursorPointsAtTheLastReturnedRow(t *testing.T) {
	all := invoices(3)
	pg := &fakePostgres{invoices: all, assets: []domain.Asset{{ID: 1, Decimals: 6}}}

	out := run(t, pg, Input{Limit: 2})

	got, err := decodeCursor(out.NextCursor)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != all[1].ID {
		t.Fatalf("cursor points at %s, want the second row %s", got.ID, all[1].ID)
	}
}

func TestValidateClampsLimit(t *testing.T) {
	in := Input{}
	if err := in.Validate(); err != nil || in.Limit != defaultLimit {
		t.Fatalf("limit = %d, err = %v", in.Limit, err)
	}

	in = Input{Limit: 10_000}
	if err := in.Validate(); err != nil || in.Limit != maxLimit {
		t.Fatalf("limit = %d, err = %v", in.Limit, err)
	}

	in = Input{Limit: -5}
	if err := in.Validate(); err != nil || in.Limit != defaultLimit {
		t.Fatalf("limit = %d, err = %v", in.Limit, err)
	}
}

// Ignoring an unknown status would return every invoice instead of none, which
// the caller has no way to notice.
func TestValidateRejectsBadFilters(t *testing.T) {
	tests := map[string]Input{
		"unknown status":  {Status: "paid"},
		"unknown network": {Network: "solana"},
		"reversed range": {
			CreatedFrom: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC),
			CreatedTo:   time.Date(2026, 8, 13, 11, 0, 0, 0, time.UTC),
		},
		"broken cursor": {Cursor: "!!!"},
	}

	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := in
			if err := candidate.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestValidateAcceptsKnownFilters(t *testing.T) {
	in := Input{Status: string(domain.InvoiceStatusConfirmed), Network: "bsc"}
	if err := in.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
