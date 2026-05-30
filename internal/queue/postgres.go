package queue

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"webhook-ingestor/internal/store"
)

// Config tunes the lease + retry policy. Zero values get safe defaults in NewPostgres.
type Config struct {
	MaxAttempts  int
	Lease        time.Duration // visibility timeout for a claimed row
	BaseBackoff  time.Duration // first retry delay; doubles each attempt
	MaxBackoff   time.Duration // cap on retry delay
	PollInterval time.Duration // idle re-check; also bounds expired-lease recovery latency
}

// Postgres is the database-backed Queue: the raw_events table IS the queue, claimed
// via FOR UPDATE SKIP LOCKED under a lease. Swapping to SQS/Kafka means writing a new
// Queue implementation; the consumer and processor are untouched.
type Postgres struct {
	store  *store.Store
	cfg    Config
	log    *slog.Logger
	notify chan struct{}
}

func NewPostgres(st *store.Store, cfg Config, log *slog.Logger) *Postgres {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 5
	}
	if cfg.Lease <= 0 {
		cfg.Lease = 30 * time.Second
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 2 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 5 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	return &Postgres{store: st, cfg: cfg, log: log, notify: make(chan struct{}, 1)}
}

// Notify wakes one waiting Claim. Non-blocking and coalescing; the poll interval is
// the safety net if a signal is dropped.
func (q *Postgres) Notify() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// Claim leases the next processable row, blocking up to PollInterval for new work.
// Returns (nil, nil) on an idle timeout — the consumer simply loops. The wait also
// bounds how long an expired lease (crashed worker / due backoff) waits for recovery.
func (q *Postgres) Claim(ctx context.Context) (*Job, error) {
	raw, err := q.store.Claim(ctx, q.cfg.Lease)
	if err != nil {
		return nil, err
	}
	if raw != nil {
		return &Job{ID: raw.ID, Payload: raw.Payload, Attempts: raw.Attempts}, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-q.notify:
	case <-time.After(q.cfg.PollInterval):
	}
	return nil, nil
}

func (q *Postgres) Ack(ctx context.Context, id string) error {
	return q.store.MarkProcessed(ctx, id)
}

// Retry reschedules with exponential backoff, or dead-letters once attempts hit the cap.
func (q *Postgres) Retry(ctx context.Context, id string, attempts int, reason string) error {
	backoff := q.backoff(attempts)
	if err := q.store.RetryOrFail(ctx, id, attempts, q.cfg.MaxAttempts, backoff, reason); err != nil {
		return err
	}
	if attempts >= q.cfg.MaxAttempts {
		// DLQ. In production this is the chokepoint where an alert/metric fires (roadmap).
		q.log.Warn("event dead-lettered", "id", id, "attempts", attempts, "reason", reason)
	} else {
		q.log.Info("retry scheduled", "id", id, "attempt", attempts, "backoff", backoff, "reason", reason)
	}
	return nil
}

// Fail dead-letters a permanent failure immediately.
func (q *Postgres) Fail(ctx context.Context, id string, reason string) error {
	q.log.Warn("event dead-lettered", "id", id, "reason", reason)
	return q.store.Fail(ctx, id, reason)
}

// backoff returns an exponential delay with full jitter: random within [d/2, d] where
// d = BaseBackoff * 2^(attempts-1), capped at MaxBackoff. Jitter de-synchronizes
// retries so a provider outage doesn't produce a thundering herd.
func (q *Postgres) backoff(attempts int) time.Duration {
	d := q.cfg.BaseBackoff << (attempts - 1) // attempts is >= 1 (incremented at claim)
	if d <= 0 || d > q.cfg.MaxBackoff {      // <=0 catches int64 overflow at high attempts
		d = q.cfg.MaxBackoff
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}
