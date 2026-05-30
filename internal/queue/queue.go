// Package queue is the work-source boundary. The consumer (worker.Pool) and the
// domain pipeline (processor) depend only on the Queue interface, so the backing
// broker — Postgres today, SQS/Kafka tomorrow — can be swapped without touching them.
package queue

import (
	"context"
	"errors"
	"fmt"
)

// Job is one unit of work: an opaque payload plus the metadata the retry policy needs.
type Job struct {
	ID       string
	Payload  []byte
	Attempts int
}

// Queue is a lease-based work source with an at-least-once lifecycle.
//
// Claim long-polls (blocks until a job is ready or an internal window elapses),
// which lets broker-native blocking reads — SQS ReceiveMessage, Kafka poll — back it
// directly. Ack completes a job; Retry reschedules with backoff or dead-letters at
// the cap; Fail dead-letters immediately. Notify is an optional low-latency hint and
// is a no-op for brokers that already long-poll.
type Queue interface {
	Claim(ctx context.Context) (*Job, error)
	Ack(ctx context.Context, id string) error
	Retry(ctx context.Context, id string, attempts int, reason string) error
	Fail(ctx context.Context, id string, reason string) error
	Notify()
}

// Permanent wraps an error that must not be retried because the input won't improve:
// a non-retryable provider 4xx, or a deterministic validation failure. The consumer
// dead-letters these immediately instead of spending retry attempts (and money) on them.
type Permanent struct{ Err error }

func (e *Permanent) Error() string { return e.Err.Error() }
func (e *Permanent) Unwrap() error { return e.Err }

// Permanentf builds a Permanent error.
func Permanentf(format string, a ...any) error {
	return &Permanent{Err: fmt.Errorf(format, a...)}
}

// IsPermanent reports whether err, or anything it wraps, is Permanent.
func IsPermanent(err error) bool {
	_, ok := errors.AsType[*Permanent](err)
	return ok
}
