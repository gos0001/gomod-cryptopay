package cryptopay

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// CreateInvoiceRequest is the body of POST /api/v1/invoices.
type CreateInvoiceRequest struct {
	// Network is "tron" or "bsc".
	Network string `json:"network"`
	// Symbol names the asset the readable way; ContractAddress is the escape
	// hatch for when two contracts share a symbol on one chain. One of the two.
	Symbol          string `json:"symbol,omitempty"`
	ContractAddress string `json:"contract_address,omitempty"`

	// Amount is a decimal string in whole tokens — "10.50", not smallest units.
	Amount string `json:"amount"`

	// ExternalID is your own key and the idempotency key: repeating it returns
	// the existing invoice instead of allocating a second one. Use your order id.
	ExternalID string `json:"external_id,omitempty"`

	// ExpiresIn overrides the service's configured TTL, as a duration string
	// such as "45m". Capped at 24h by the service.
	ExpiresIn string `json:"expires_in,omitempty"`

	Description string          `json:"description,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

// CreateInvoice allocates an invoice with a unique payable amount.
//
// created is false when the invoice already existed under this ExternalID — the
// service answered 200 rather than 201. Both are success: a retried request
// cannot produce two invoices for one order, which is the point of sending an
// ExternalID at all.
func (c *Client) CreateInvoice(ctx context.Context, in CreateInvoiceRequest) (inv Invoice, created bool, err error) {
	var out struct {
		Invoice Invoice `json:"invoice"`
	}
	status, err := c.do(ctx, http.MethodPost, apiPrefix+"/invoices", nil, in, &out)
	if err != nil {
		return Invoice{}, false, err
	}
	return out.Invoice, status == http.StatusCreated, nil
}

// GetInvoice reads one invoice by id. Missing ids give ErrNotFound.
func (c *Client) GetInvoice(ctx context.Context, id string) (Invoice, error) {
	var out struct {
		Invoice Invoice `json:"invoice"`
	}
	_, err := c.do(ctx, http.MethodGet, apiPrefix+"/invoices/"+url.PathEscape(id), nil, nil, &out)
	return out.Invoice, err
}

// CancelInvoice cancels an invoice that is still pending. An invoice with a
// transfer already on chain cannot be cancelled — the service answers 409, which
// is ErrConflict.
func (c *Client) CancelInvoice(ctx context.Context, id string) (Invoice, error) {
	var out struct {
		Invoice Invoice `json:"invoice"`
	}
	_, err := c.do(ctx, http.MethodPost,
		apiPrefix+"/invoices/"+url.PathEscape(id)+"/cancel", nil, nil, &out)
	return out.Invoice, err
}

// InvoiceFilter narrows a listing. Every field is optional; the zero value lists
// everything, newest first.
type InvoiceFilter struct {
	Status     string
	Network    string
	AssetID    int64
	ExternalID string

	CreatedFrom time.Time
	CreatedTo   time.Time

	// Limit is the page size. The service clamps it rather than refusing an
	// oversized value.
	Limit int32
	// Cursor continues a previous page. Prefer AllInvoices, which handles it.
	Cursor string
}

func (f InvoiceFilter) query() url.Values {
	q := url.Values{}
	set := func(key, value string) {
		if value != "" {
			q.Set(key, value)
		}
	}
	set("status", f.Status)
	set("network", f.Network)
	set("external_id", f.ExternalID)
	set("cursor", f.Cursor)
	if f.AssetID != 0 {
		q.Set("asset_id", strconv.FormatInt(f.AssetID, 10))
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.FormatInt(int64(f.Limit), 10))
	}
	// RFC 3339: the service parses these with time.Parse, so a local format
	// would be rejected rather than misread.
	if !f.CreatedFrom.IsZero() {
		q.Set("created_from", f.CreatedFrom.UTC().Format(time.RFC3339))
	}
	if !f.CreatedTo.IsZero() {
		q.Set("created_to", f.CreatedTo.UTC().Format(time.RFC3339))
	}
	return q
}

// InvoicePage is one page of a listing.
type InvoicePage struct {
	Invoices []Invoice `json:"invoices"`
	// NextCursor is empty when this was the last page.
	NextCursor string `json:"next_cursor,omitempty"`
}

// ListInvoices reads one page. Use AllInvoices unless you are driving the
// pagination yourself — for an API endpoint of your own, say.
func (c *Client) ListInvoices(ctx context.Context, filter InvoiceFilter) (InvoicePage, error) {
	var page InvoicePage
	_, err := c.do(ctx, http.MethodGet, apiPrefix+"/invoices", filter.query(), nil, &page)
	return page, err
}

// AllInvoices walks every page, following the cursor.
//
// The loop over a cursor is where paginated APIs are most often got wrong —
// forgetting the terminating condition, or resending the same cursor forever —
// so it lives here rather than in each caller:
//
//	for inv, err := range c.AllInvoices(ctx, cryptopay.InvoiceFilter{Status: cryptopay.StatusPending}) {
//	    if err != nil {
//	        return err
//	    }
//	    ...
//	}
//
// The iteration stops after yielding an error; the error is yielded with a zero
// Invoice, so a caller that checks err first never reads a half-filled value.
func (c *Client) AllInvoices(ctx context.Context, filter InvoiceFilter) iter.Seq2[Invoice, error] {
	return func(yield func(Invoice, error) bool) {
		for {
			page, err := c.ListInvoices(ctx, filter)
			if err != nil {
				yield(Invoice{}, err)
				return
			}

			for _, inv := range page.Invoices {
				if !yield(inv, nil) {
					return
				}
			}

			// Empty cursor ends it. So does an empty page with a cursor still
			// set, which would otherwise be an infinite loop against a service
			// that misbehaves.
			if page.NextCursor == "" || len(page.Invoices) == 0 {
				return
			}
			filter.Cursor = page.NextCursor
		}
	}
}
