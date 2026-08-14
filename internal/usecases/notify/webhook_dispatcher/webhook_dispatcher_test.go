package webhook_dispatcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

type failure struct {
	id      int64
	backoff time.Duration
	reason  string
}

type fakePostgres struct {
	events []postgresadapter.WebhookEvent

	mu        sync.Mutex
	delivered []int64
	failed    []failure

	claimErr  error
	pruneErr  error
	markErr   error
	pruned    int64
	claimArgs struct {
		lease       time.Duration
		maxAttempts int32
		batchSize   int32
	}
}

func (f *fakePostgres) ClaimDueWebhookEvents(_ context.Context, lease time.Duration, maxAttempts, batchSize int32) ([]postgresadapter.WebhookEvent, error) {
	f.claimArgs.lease = lease
	f.claimArgs.maxAttempts = maxAttempts
	f.claimArgs.batchSize = batchSize
	return f.events, f.claimErr
}

func (f *fakePostgres) MarkWebhookDelivered(_ context.Context, id int64) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivered = append(f.delivered, id)
	return nil
}

func (f *fakePostgres) MarkWebhookFailed(_ context.Context, id int64, backoff time.Duration, reason string) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, failure{id: id, backoff: backoff, reason: reason})
	return nil
}

func (f *fakePostgres) PruneWebhookEvents(_ context.Context, _ time.Duration, _ int32) (int64, error) {
	return f.pruned, f.pruneErr
}

type fakeSender struct {
	enabled bool
	err     error

	// delay lets a test hold one delivery open to observe concurrency.
	delay time.Duration

	mu       sync.Mutex
	sent     []string
	attempts []int

	inFlight atomic.Int32
	peak     atomic.Int32
}

func (f *fakeSender) Enabled() bool { return f.enabled }

func (f *fakeSender) Send(_ context.Context, _, event string, attempt int, _ []byte) error {
	n := f.inFlight.Add(1)
	for {
		peak := f.peak.Load()
		if n <= peak || f.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	defer f.inFlight.Add(-1)

	if f.delay > 0 {
		time.Sleep(f.delay)
	}

	f.mu.Lock()
	f.sent = append(f.sent, event)
	f.attempts = append(f.attempts, attempt)
	f.mu.Unlock()

	return f.err
}

func cfg() Config {
	return Config{
		Interval:    config.Duration(10 * time.Second),
		BatchSize:   50,
		Concurrency: 4,
		MaxAttempts: 3,
		BackoffBase: config.Duration(10 * time.Second),
		BackoffMax:  config.Duration(time.Hour),
		Retention:   config.Duration(168 * time.Hour),
		Timeout:     config.Duration(10 * time.Second),
	}
}

func dispatcher(pg *fakePostgres, s *fakeSender, c Config) *Usecase {
	return &Usecase{postgres: pg, sender: s, cfg: c, logger: zap.NewNop().Sugar()}
}

func event(id int64, attempts int32) postgresadapter.WebhookEvent {
	return postgresadapter.WebhookEvent{
		ID: id, EventID: uuid.New(), Event: "invoice.confirmed",
		Payload: []byte(`{"a":1}`), Attempts: attempts,
	}
}

func TestDeliversAndMarksDelivered(t *testing.T) {
	pg := &fakePostgres{events: []postgresadapter.WebhookEvent{event(1, 1), event(2, 1)}}
	s := &fakeSender{enabled: true}

	out, err := dispatcher(pg, s, cfg()).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Delivered != 2 || out.Failed != 0 {
		t.Fatalf("delivered = %d failed = %d", out.Delivered, out.Failed)
	}
	if len(pg.delivered) != 2 {
		t.Fatalf("marked %v", pg.delivered)
	}
	if len(s.sent) != 2 {
		t.Fatalf("sent %v", s.sent)
	}
}

// The claim already incremented attempts, so the stored value is this attempt's
// number and goes into the header unchanged.
func TestAttemptNumberComesFromTheClaimedRow(t *testing.T) {
	pg := &fakePostgres{events: []postgresadapter.WebhookEvent{event(1, 2)}}
	s := &fakeSender{enabled: true}

	if _, err := dispatcher(pg, s, cfg()).Execute(context.Background(), Input{}); err != nil {
		t.Fatal(err)
	}
	if len(s.attempts) != 1 || s.attempts[0] != 2 {
		t.Fatalf("attempt = %v, want 2", s.attempts)
	}
}

// A receiver that rejected an event is not a failure of the tick: it is recorded
// against the row and retried later.
func TestReceiverRejectionDoesNotFailTheTick(t *testing.T) {
	pg := &fakePostgres{events: []postgresadapter.WebhookEvent{event(1, 1)}}
	s := &fakeSender{enabled: true, err: errors.New("500 from receiver")}

	out, err := dispatcher(pg, s, cfg()).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatalf("the tick should succeed: %v", err)
	}
	if out.Failed != 1 || out.Delivered != 0 {
		t.Fatalf("failed = %d delivered = %d", out.Failed, out.Delivered)
	}
	if len(pg.failed) != 1 {
		t.Fatalf("failures = %+v", pg.failed)
	}
	if !strings.Contains(pg.failed[0].reason, "500 from receiver") {
		t.Errorf("reason = %q", pg.failed[0].reason)
	}
}

// Failing to record the outcome IS fatal: the event would otherwise be silently
// forgotten.
func TestFailingToRecordTheOutcomeFailsTheTick(t *testing.T) {
	pg := &fakePostgres{
		events:  []postgresadapter.WebhookEvent{event(1, 1)},
		markErr: errors.New("outbox unavailable"),
	}
	s := &fakeSender{enabled: true}

	if _, err := dispatcher(pg, s, cfg()).Execute(context.Background(), Input{}); err == nil {
		t.Fatal("want an error")
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	c := cfg()
	c.MaxAttempts = 100 // so nothing is treated as exhausted here
	uc := dispatcher(&fakePostgres{}, &fakeSender{}, c)

	base := c.BackoffBase.Std()
	for attempt, want := range map[int]time.Duration{
		1: base,
		2: 2 * base,
		3: 4 * base,
		4: 8 * base,
	} {
		if got := uc.backoff(attempt); got != want {
			t.Errorf("attempt %d: backoff = %s, want %s", attempt, got, want)
		}
	}

	// Past the cap it stops doubling rather than growing past any useful value.
	if got := uc.backoff(20); got != c.BackoffMax.Std() {
		t.Errorf("attempt 20: backoff = %s, want the cap %s", got, c.BackoffMax)
	}
	// And an absurd attempt number must not overflow into a negative duration,
	// which would schedule the retry in the past.
	if got := uc.backoff(9999); got != c.BackoffMax.Std() {
		t.Errorf("attempt 9999: backoff = %s, want the cap", got)
	}
	if got := uc.backoff(0); got != base {
		t.Errorf("attempt 0: backoff = %s, want the base", got)
	}
}

// An exhausted event is simply not claimed again — the query filters on it — so
// the only job here is to say so out loud.
func TestReachingMaxAttemptsIsCountedSeparately(t *testing.T) {
	c := cfg() // MaxAttempts 3
	pg := &fakePostgres{events: []postgresadapter.WebhookEvent{event(1, 3)}}
	s := &fakeSender{enabled: true, err: errors.New("still down")}

	out, err := dispatcher(pg, s, c).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Exhausted != 1 || out.Failed != 0 {
		t.Fatalf("exhausted = %d failed = %d", out.Exhausted, out.Failed)
	}
	// The failure is still recorded, so last_error explains why it stopped.
	if len(pg.failed) != 1 {
		t.Fatalf("failures = %+v", pg.failed)
	}
}

// The claim query is what enforces the attempt ceiling, so the dispatcher has to
// pass it through.
func TestClaimIsGivenTheLimits(t *testing.T) {
	c := cfg()
	pg := &fakePostgres{}
	s := &fakeSender{enabled: true}

	if _, err := dispatcher(pg, s, c).Execute(context.Background(), Input{}); err != nil {
		t.Fatal(err)
	}

	if pg.claimArgs.maxAttempts != c.MaxAttempts {
		t.Errorf("max attempts = %d", pg.claimArgs.maxAttempts)
	}
	if pg.claimArgs.batchSize != c.BatchSize {
		t.Errorf("batch size = %d", pg.claimArgs.batchSize)
	}
	// The lease has to outlast an attempt, or a second dispatcher could pick up
	// an event this one is still sending.
	if pg.claimArgs.lease <= c.Timeout.Std() {
		t.Errorf("lease = %s, must exceed the send timeout %s", pg.claimArgs.lease, c.Timeout)
	}
}

// One hung receiver must not hold up the rest of the batch, which is why
// delivery is concurrent — and bounded, so a backlog does not open every
// connection at once.
func TestDeliveryIsConcurrentAndBounded(t *testing.T) {
	c := cfg()
	c.Concurrency = 3

	events := make([]postgresadapter.WebhookEvent, 12)
	for i := range events {
		events[i] = event(int64(i+1), 1)
	}
	pg := &fakePostgres{events: events}
	s := &fakeSender{enabled: true, delay: 40 * time.Millisecond}

	start := time.Now()
	out, err := dispatcher(pg, s, c).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if out.Delivered != 12 {
		t.Fatalf("delivered = %d", out.Delivered)
	}
	if peak := s.peak.Load(); peak > int32(c.Concurrency) {
		t.Fatalf("peak concurrency = %d, above the limit of %d", peak, c.Concurrency)
	}
	// Sequentially this would be 12 x 40ms = 480ms; at three at a time it should
	// be closer to 160ms.
	if elapsed > 400*time.Millisecond {
		t.Fatalf("took %s — deliveries do not appear to overlap", elapsed)
	}
}

// Pruning runs whether or not a receiver is configured: tying it to the sender
// would leave the table with nothing able to clean it.
func TestPruningRunsEvenWithNotificationsOff(t *testing.T) {
	pg := &fakePostgres{pruned: 7, events: []postgresadapter.WebhookEvent{event(1, 1)}}
	s := &fakeSender{enabled: false}

	out, err := dispatcher(pg, s, cfg()).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Pruned != 7 {
		t.Errorf("pruned = %d", out.Pruned)
	}
	if len(s.sent) != 0 {
		t.Errorf("a disabled sender must not be called: %v", s.sent)
	}
	if out.Delivered != 0 {
		t.Errorf("delivered = %d", out.Delivered)
	}
}

func TestClaimFailureFailsTheTick(t *testing.T) {
	pg := &fakePostgres{claimErr: errors.New("db down")}
	s := &fakeSender{enabled: true}

	if _, err := dispatcher(pg, s, cfg()).Execute(context.Background(), Input{}); err == nil {
		t.Fatal("want an error")
	}
}

func TestPruneFailureFailsTheTick(t *testing.T) {
	pg := &fakePostgres{pruneErr: errors.New("db down")}
	s := &fakeSender{enabled: true}

	if _, err := dispatcher(pg, s, cfg()).Execute(context.Background(), Input{}); err == nil {
		t.Fatal("want an error")
	}
}

func TestEmptyBatchIsQuiet(t *testing.T) {
	pg := &fakePostgres{}
	s := &fakeSender{enabled: true}

	out, err := dispatcher(pg, s, cfg()).Execute(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Delivered != 0 || out.Failed != 0 {
		t.Fatalf("got %+v", out)
	}
}

func loadFrom(t *testing.T, contents string) (Config, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	f, err := config.Load(config.Path(path))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return LoadConfig(f)
}

func TestLoadConfigDefaults(t *testing.T) {
	c, err := loadFrom(t, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Interval.Std() != 10*time.Second || c.BatchSize != 50 ||
		c.Concurrency != 4 || c.MaxAttempts != 10 {
		t.Fatalf("got %+v", c)
	}
	// Zero is the documented off switch.
	if _, err := loadFrom(t, `{"webhook": {"interval": "0s"}}`); err != nil {
		t.Fatalf("zero interval should be allowed: %v", err)
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	for name, contents := range map[string]string{
		"zero batch":        `{"webhook": {"batch_size": 0}}`,
		"zero concurrency":  `{"webhook": {"concurrency": 0}}`,
		"zero attempts":     `{"webhook": {"max_attempts": 0}}`,
		"base above max":    `{"webhook": {"backoff_base": "2h", "backoff_max": "1h"}}`,
		"zero retention":    `{"webhook": {"retention": "0s"}}`,
		"negative interval": `{"webhook": {"interval": "-1s"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadFrom(t, contents); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}
