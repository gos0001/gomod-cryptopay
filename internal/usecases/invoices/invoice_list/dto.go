package invoice_list

import (
	"fmt"
	"strings"
	"time"

	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/view"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// Input is filled from the query string, so every field is json:"-".
type Input struct {
	Status     string `json:"-"`
	Network    string `json:"-"`
	AssetID    int64  `json:"-"`
	ExternalID string `json:"-"`

	CreatedFrom time.Time `json:"-"`
	CreatedTo   time.Time `json:"-"`

	Cursor string `json:"-"`
	Limit  int32  `json:"-"`

	// decoded is the parsed cursor, filled by Validate.
	decoded cursor
}

// Validate clamps paging and rejects unusable filters.
//
// Paging is clamped rather than refused — an oversized limit is a client bug,
// not an attack, and failing the request helps nobody. A bad status or cursor
// is refused, because silently ignoring either returns a different result set
// than the caller asked for.
func (in *Input) Validate() error {
	if in.Limit <= 0 {
		in.Limit = defaultLimit
	}
	if in.Limit > maxLimit {
		in.Limit = maxLimit
	}

	in.Status = strings.TrimSpace(in.Status)
	if in.Status != "" && !domain.InvoiceStatus(in.Status).Valid() {
		return fmt.Errorf("%w: status must be one of pending, detected, confirmed, expired, cancelled",
			domain.ErrInvalidInput)
	}

	in.Network = strings.TrimSpace(in.Network)
	if in.Network != "" {
		if _, err := domain.ParseNetwork(in.Network); err != nil {
			return fmt.Errorf("%w: network must be one of tron, bsc", domain.ErrInvalidInput)
		}
	}

	if !in.CreatedFrom.IsZero() && !in.CreatedTo.IsZero() && in.CreatedTo.Before(in.CreatedFrom) {
		return fmt.Errorf("%w: created_to is before created_from", domain.ErrInvalidInput)
	}

	if in.Cursor != "" {
		c, err := decodeCursor(in.Cursor)
		if err != nil {
			return err
		}
		in.decoded = c
	}

	return nil
}

type Output struct {
	Invoices []view.Invoice `json:"invoices"`
	// NextCursor is empty on the last page. A client stops when it is empty
	// rather than when the page is short — a page can be exactly full and still
	// be the last one.
	NextCursor string `json:"next_cursor,omitempty"`
}
