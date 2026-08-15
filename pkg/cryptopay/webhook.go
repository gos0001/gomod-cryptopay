package cryptopay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/webhook"
)

// Event names. invoice.confirmed is the only one that means paid.
const (
	// EventDetected: a matching transfer is on chain and not yet final. Show
	// progress; release nothing. A reorg can still take it away.
	EventDetected = "invoice.detected"
	// EventConfirmed: final. Fulfil the order.
	EventConfirmed = "invoice.confirmed"
	// EventReverted: a detected transfer vanished in a reorg and the invoice is
	// payable again. Undo whatever EventDetected showed.
	EventReverted = "invoice.reverted"
)

// Headers cryptopay sends with every delivery.
const (
	HeaderSignature = webhook.HeaderSignature
	HeaderTimestamp = webhook.HeaderTimestamp
	HeaderEvent     = webhook.HeaderEvent
	HeaderEventID   = webhook.HeaderEventID
	HeaderAttempt   = webhook.HeaderAttempt
)

// DefaultTolerance is how far a delivery's timestamp may be from now.
//
// The timestamp is inside the signed string precisely so a captured request
// cannot be replayed forever; without this check the signature alone would stay
// valid for all time, which is the property the design is trying to remove.
const DefaultTolerance = 5 * time.Minute

// maxWebhookBody bounds what the handler will read. A signature check on an
// unbounded body is a way to be knocked over by anyone who can reach the route.
const maxWebhookBody = 1 << 20

// Event is one verified delivery.
type Event struct {
	// ID is stable across retries — deduplicate on it. Redelivery is normal:
	// a timeout, a deploy or a 500 all produce it.
	ID string
	// Attempt is 1-based.
	Attempt int

	Event     string `json:"event"`
	InvoiceID string `json:"invoice_id"`
	Status    string `json:"status"`
	Network   string `json:"network"`
	Symbol    string `json:"symbol"`
	// PayAmount is in the token's smallest units, not whole tokens: the payload
	// is for machines. Divide by 10^Decimals only for display.
	PayAmount string `json:"pay_amount"`
	Decimals  int32  `json:"decimals"`

	// Raw is the body exactly as it arrived, for logging or for a field this
	// struct does not yet know about.
	Raw []byte
}

// PayAmountBig parses PayAmount, which is in smallest units. False when absent
// or malformed.
func (e Event) PayAmountBig() (*big.Int, bool) { return parseUnits(e.PayAmount) }

// Errors a verification can fail with. All of them are answered 401 by
// WebhookHandler: from the sender's side there is no difference, and telling a
// forger which check they failed is free help.
var (
	ErrNoSignature      = errors.New("cryptopay: webhook has no signature header")
	ErrBadSignature     = errors.New("cryptopay: webhook signature does not match")
	ErrStaleTimestamp   = errors.New("cryptopay: webhook timestamp is outside the tolerance")
	ErrMalformedPayload = errors.New("cryptopay: webhook payload is not valid JSON")
)

// VerifyRequest authenticates a delivery and decodes it.
//
// body must be the bytes exactly as they arrived. Re-serialising parsed JSON
// changes them and the signature will not match — that is the single most common
// reason a receiver rejects every genuine event.
//
// tolerance of zero means DefaultTolerance.
//
// Use this when you have your own router or middleware; WebhookHandler wraps it
// with the reading and the status codes.
func VerifyRequest(secret string, r *http.Request, body []byte, tolerance time.Duration) (Event, error) {
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}

	signature := r.Header.Get(HeaderSignature)
	timestamp := r.Header.Get(HeaderTimestamp)
	if signature == "" || timestamp == "" {
		return Event{}, ErrNoSignature
	}

	// Freshness is checked before the HMAC so a flood of stale replays costs a
	// string parse rather than a hash over a megabyte.
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return Event{}, fmt.Errorf("%w: %q is not a unix timestamp", ErrStaleTimestamp, timestamp)
	}
	// Absolute difference: a receiver whose clock runs behind the sender's would
	// otherwise see every delivery as being from the future and accept nothing.
	if drift := time.Since(time.Unix(seconds, 0)); drift > tolerance || drift < -tolerance {
		return Event{}, fmt.Errorf("%w: %s off", ErrStaleTimestamp, drift.Round(time.Second))
	}

	if !webhook.Verify(secret, timestamp, signature, body) {
		return Event{}, ErrBadSignature
	}

	event := Event{
		ID:      r.Header.Get(HeaderEventID),
		Attempt: 1,
		Raw:     body,
	}
	if attempt, err := strconv.Atoi(r.Header.Get(HeaderAttempt)); err == nil && attempt > 0 {
		event.Attempt = attempt
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return Event{}, fmt.Errorf("%w: %s", ErrMalformedPayload, err)
	}

	return event, nil
}

// WebhookHandlerOption configures WebhookHandler.
type WebhookHandlerOption func(*webhookHandler)

// WithTolerance overrides how far a delivery's timestamp may be from now.
// Widen it only if your clocks genuinely drift — the window is how long a
// captured request stays replayable.
func WithTolerance(d time.Duration) WebhookHandlerOption {
	return func(h *webhookHandler) {
		if d > 0 {
			h.tolerance = d
		}
	}
}

// WithMaxBody overrides the accepted body size.
func WithMaxBody(n int64) WebhookHandlerOption {
	return func(h *webhookHandler) {
		if n > 0 {
			h.maxBody = n
		}
	}
}

// WithErrorLog receives verification failures and handler errors. Without it
// they are answered but not reported, and a receiver that rejects everything
// because of a mistyped secret looks identical to one nobody is calling.
func WithErrorLog(fn func(error)) WebhookHandlerOption {
	return func(h *webhookHandler) {
		if fn != nil {
			h.logErr = fn
		}
	}
}

type webhookHandler struct {
	secret    string
	handle    func(context.Context, Event) error
	tolerance time.Duration
	maxBody   int64
	logErr    func(error)
}

// WebhookHandler returns an http.Handler that verifies a delivery and passes it
// to fn.
//
//	mux.Handle("/hooks/cryptopay", cryptopay.WebhookHandler(secret,
//	    func(ctx context.Context, e cryptopay.Event) error {
//	        if e.Event != cryptopay.EventConfirmed {
//	            return nil
//	        }
//	        return orders.MarkPaid(ctx, e.InvoiceID)
//	    }))
//
// Answers: 401 when verification fails for any reason, 400 when the body is not
// JSON, 500 when fn returns an error, 200 otherwise. A 500 means the event is
// redelivered later, so returning an error is the right move when your database
// is momentarily unavailable — and the wrong one for an event you have decided
// to ignore.
//
// Two things this deliberately does not do:
//
//   - **Deduplicate.** Event.ID is stable across retries, but the record of what
//     you have already processed belongs in your database. A library holding that
//     in memory would forget it on the first restart and lie about it in between.
//   - **Run fn in the background.** Answering before the work is done would turn
//     a failed write into a lost payment notification. If fn is slow, make it
//     enqueue and return: deliveries slower than the service's webhook.timeout
//     are treated as failed and redelivered, which means duplicate work unless fn
//     is idempotent.
func WebhookHandler(secret string, fn func(context.Context, Event) error, opts ...WebhookHandlerOption) http.Handler {
	h := &webhookHandler{
		secret:    secret,
		handle:    fn,
		tolerance: DefaultTolerance,
		maxBody:   maxWebhookBody,
		logErr:    func(error) {},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBody))
	if err != nil {
		h.logErr(fmt.Errorf("cryptopay: read webhook body: %w", err))
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	event, err := VerifyRequest(h.secret, r, body, h.tolerance)
	if err != nil {
		h.logErr(err)
		if errors.Is(err, ErrMalformedPayload) {
			http.Error(w, "malformed payload", http.StatusBadRequest)
			return
		}
		// One answer for every authentication failure. Telling a forger which
		// check they failed is free help.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if err := h.handle(r.Context(), event); err != nil {
		h.logErr(fmt.Errorf("cryptopay: handling %s %s: %w", event.Event, event.ID, err))
		// 500 so the service retries. The body reaches the sender's last_error
		// column, which is where an operator looks when deliveries stop.
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
