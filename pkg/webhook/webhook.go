// Package webhook delivers signed event payloads to the configured URL.
//
// Signing is not decoration. Without it a receiver has no way to tell an event
// from this service apart from a POST by anyone who learned the URL — and
// webhook URLs leak: into logs, into screenshots, into the browser history of
// whoever tested it once.
//
// Zero domain imports; the caller hands over bytes.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Headers a receiver reads.
const (
	HeaderSignature = "X-Cryptopay-Signature" // sha256=<hex hmac of "<timestamp>.<body>">
	HeaderTimestamp = "X-Cryptopay-Timestamp" // unix seconds
	HeaderEvent     = "X-Cryptopay-Event"     // e.g. invoice.confirmed
	HeaderEventID   = "X-Cryptopay-Event-Id"  // stable across retries: deduplicate on it
	HeaderAttempt   = "X-Cryptopay-Attempt"   // 1-based
	HeaderAPIKey    = "X-Cryptopay-Api-Key"   // optional, for gateway filtering
)

// errorBodyLimit is how much of a receiver's response is kept for the error
// message. A rejecting receiver usually explains itself in the first few lines,
// and the rest is not worth storing.
const errorBodyLimit = 512

type Sender struct {
	cfg    Config
	client *http.Client
	now    func() time.Time
}

func New(cfg Config) *Sender {
	return &Sender{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout.Std()},
		now:    time.Now,
	}
}

// Enabled reports whether a destination is configured at all.
func (s *Sender) Enabled() bool { return s.cfg.Enabled() }

// Send posts one event.
//
// A non-2xx response is an error, so the caller retries. Delivery is
// at-least-once: the event id is stable across attempts precisely so a receiver
// can deduplicate.
func (s *Sender) Send(ctx context.Context, eventID, event string, attempt int, payload []byte) error {
	if !s.cfg.Enabled() {
		return fmt.Errorf("webhook: no destination configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("webhook: build request: %w", err)
	}

	timestamp := strconv.FormatInt(s.now().Unix(), 10)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cryptopay-webhook/1")
	req.Header.Set(HeaderEvent, event)
	req.Header.Set(HeaderEventID, eventID)
	req.Header.Set(HeaderAttempt, strconv.Itoa(attempt))
	req.Header.Set(HeaderTimestamp, timestamp)

	if s.cfg.Secret != "" {
		req.Header.Set(HeaderSignature, "sha256="+Sign(s.cfg.Secret, timestamp, payload))
	}
	if s.cfg.APIKey != "" {
		req.Header.Set(HeaderAPIKey, s.cfg.APIKey)
	}

	res, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: post: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	// Drain a bounded amount so the connection can be reused, and keep a little
	// for the error message.
	body, _ := io.ReadAll(io.LimitReader(res.Body, errorBodyLimit))

	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("webhook: receiver responded %s: %s",
			res.Status, bytes.TrimSpace(body))
	}

	return nil
}

// Sign computes the signature over "<timestamp>.<body>".
//
// The timestamp is inside the signed string on purpose. Signing the body alone
// would leave any captured request valid forever, because its signature would
// keep verifying; a receiver rejects timestamps outside a few minutes and the
// replay dies with them.
func Sign(secret, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify is what a receiver runs against X-Cryptopay-Signature.
//
// Exported so consumers copy it rather than reimplement the comparison: this
// uses a constant-time compare, which a hand-rolled `==` would not.
//
// Verifying the signature is necessary but not sufficient — a receiver must also
// reject a timestamp outside a few minutes of now, or a captured request can be
// replayed indefinitely.
func Verify(secret, timestamp, signature string, payload []byte) bool {
	expected := "sha256=" + Sign(secret, timestamp, payload)
	return hmac.Equal([]byte(signature), []byte(expected))
}
