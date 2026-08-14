package webhook_dispatcher

import (
	"errors"
	"time"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

type Config struct {
	// Interval is how often the outbox is drained. Zero leaves the job
	// unscheduled — the cron orchestrator's off switch.
	Interval config.Duration `json:"interval"`

	// BatchSize bounds one drain, so a backlog is worked through steadily rather
	// than in one long run holding a connection.
	BatchSize int32 `json:"batch_size"`

	// Concurrency caps how many deliveries are in flight at once.
	//
	// Sequential delivery would let one hung receiver hold up everything behind
	// it: at a 10s timeout and a batch of 50 that is over eight minutes against a
	// 10s tick, which is a queue that never drains.
	Concurrency int `json:"concurrency"`

	// MaxAttempts before an event stops being claimed. With the default backoff
	// that spans hours — long enough to cover a receiver's deploy, short enough
	// that a permanently wrong URL stops being retried.
	MaxAttempts int32 `json:"max_attempts"`

	BackoffBase config.Duration `json:"backoff_base"`
	BackoffMax  config.Duration `json:"backoff_max"`

	// Retention is how long delivered and exhausted rows are kept.
	Retention config.Duration `json:"retention"`

	// Timeout mirrors the sender's; used to size the claim lease.
	Timeout config.Duration `json:"timeout"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{
		Interval:    config.Duration(10 * time.Second),
		BatchSize:   50,
		Concurrency: 4,
		MaxAttempts: 10,
		BackoffBase: config.Duration(10 * time.Second),
		BackoffMax:  config.Duration(time.Hour),
		Retention:   config.Duration(168 * time.Hour),
		Timeout:     config.Duration(10 * time.Second),
	}
	if err := f.Section("webhook", &cfg); err != nil {
		return cfg, err
	}

	if cfg.Interval.Std() < 0 {
		return cfg, errors.New("config: webhook.interval must not be negative " +
			"(zero leaves the dispatcher unscheduled)")
	}
	if cfg.BatchSize <= 0 {
		return cfg, errors.New("config: webhook.batch_size must be positive")
	}
	if cfg.Concurrency <= 0 {
		return cfg, errors.New("config: webhook.concurrency must be positive")
	}
	if cfg.MaxAttempts <= 0 {
		return cfg, errors.New("config: webhook.max_attempts must be positive")
	}
	if cfg.BackoffBase.Std() <= 0 || cfg.BackoffMax.Std() <= 0 {
		return cfg, errors.New("config: webhook.backoff_base and backoff_max must be positive")
	}
	if cfg.BackoffBase.Std() > cfg.BackoffMax.Std() {
		return cfg, errors.New("config: webhook.backoff_base is above backoff_max")
	}
	if cfg.Retention.Std() <= 0 {
		return cfg, errors.New("config: webhook.retention must be positive")
	}

	return cfg, nil
}

// Lease is how long a claimed event stays claimed.
//
// It must outlast a delivery attempt, or a second dispatcher could pick up an
// event this one is still sending — FOR UPDATE SKIP LOCKED protects the claim
// itself, not the whole attempt that follows it.
func (c Config) Lease() time.Duration {
	return c.Timeout.Std()*2 + 30*time.Second
}
