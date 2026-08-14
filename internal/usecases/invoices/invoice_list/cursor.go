package invoice_list

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/gos0001/gomod-cryptopay/internal/domain"
)

// cursor is the position of the last row of a page.
//
// Keyset rather than an offset: an offset shifts under concurrent inserts, so a
// client walking the list would see some invoices twice and miss others. The
// pair (created_at, id) is what the query orders by, and the id breaks ties
// between invoices created in the same microsecond.
type cursor struct {
	// Short field names because this is base64-encoded into a URL and nobody
	// reads it.
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"i"`
}

// encode renders a cursor for the client. Opaque on purpose: the moment a
// client parses it, the pagination scheme becomes part of the API contract.
func (c cursor) encode() string {
	// Marshalling a struct of two well-formed values cannot fail.
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeCursor parses what encode produced.
//
// A malformed cursor is an error rather than a silent restart. Quietly serving
// page one to a client that asked for page nine turns a batch walk into an
// endless loop over the first page, and nothing in the response says so.
func decodeCursor(s string) (cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, fmt.Errorf("%w: cursor is not valid base64", domain.ErrInvalidInput)
	}

	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return cursor{}, fmt.Errorf("%w: cursor is malformed", domain.ErrInvalidInput)
	}
	if c.CreatedAt.IsZero() || c.ID == uuid.Nil {
		return cursor{}, fmt.Errorf("%w: cursor is incomplete", domain.ErrInvalidInput)
	}

	return c, nil
}
