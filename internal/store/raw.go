package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"webhook-ingestor/internal/model"
)

// InsertRaw stores a payload idempotently, keyed by payload_hash. Returns the row
// id and whether it was newly inserted (false = exact duplicate replay). This is
// the ingestion-side idempotency gate; a duplicate never reaches the LLM.
func (s *Store) InsertRaw(ctx context.Context, hash string, payload []byte, source string) (id string, isNew bool, err error) {
	err = s.Pool.QueryRow(ctx, `
		INSERT INTO raw_events (payload_hash, raw_payload, source)
		VALUES ($1, $2, NULLIF($3, ''))
		ON CONFLICT (payload_hash) DO NOTHING
		RETURNING id`, hash, payload, source).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	// Conflict: the row already exists — return its id so the caller can ack identically.
	err = s.Pool.QueryRow(ctx, `SELECT id FROM raw_events WHERE payload_hash=$1`, hash).Scan(&id)
	return id, false, err
}

// Claim atomically leases the next processable row: a PENDING row, or a PROCESSING
// row whose lease expired (worker crash, or a backoff retry coming due). FOR UPDATE
// SKIP LOCKED lets N workers claim disjoint rows without blocking each other, and
// the attempts bump means even a worker-killing "poison" payload eventually caps out.
// Returns (nil, nil) when the queue is empty.
func (s *Store) Claim(ctx context.Context, lease time.Duration) (*model.RawEvent, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE raw_events SET
			status      = 'PROCESSING',
			attempts    = attempts + 1,
			lease_until = now() + make_interval(secs => $1)
		WHERE id = (
			SELECT id FROM raw_events
			WHERE status = 'PENDING'
			   OR (status = 'PROCESSING' AND lease_until < now())
			ORDER BY received_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, raw_payload, attempts`, lease.Seconds())

	var e model.RawEvent
	if err := row.Scan(&e.ID, &e.Payload, &e.Attempts); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // queue empty
		}
		return nil, err
	}
	return &e, nil
}

func (s *Store) MarkProcessed(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE raw_events SET status='PROCESSED', error=NULL, lease_until=NULL WHERE id=$1`, id)
	return err
}

// RetryOrFail handles a transient failure: reschedule with a backoff lease (the row
// stays PROCESSING and Claim re-picks it once the lease expires), or move it to the
// DLQ (status=FAILED) once attempts have hit the cap. The raw payload is retained
// either way, so a FAILED row is fully replayable.
func (s *Store) RetryOrFail(ctx context.Context, id string, attempts, maxAttempts int, backoff time.Duration, reason string) error {
	if attempts >= maxAttempts {
		_, err := s.Pool.Exec(ctx,
			`UPDATE raw_events SET status='FAILED', error=$2, lease_until=NULL WHERE id=$1`, id, reason)
		return err
	}
	_, err := s.Pool.Exec(ctx,
		`UPDATE raw_events SET error=$2, lease_until=now()+make_interval(secs => $3) WHERE id=$1`,
		id, reason, backoff.Seconds())
	return err
}

// Fail moves a row straight to the DLQ regardless of attempts. Used for permanent
// failures (non-retryable LLM 4xx, deterministic validation errors) where retrying
// the same payload would only burn time and money for the same result.
func (s *Store) Fail(ctx context.Context, id, reason string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE raw_events SET status='FAILED', error=$2, lease_until=NULL WHERE id=$1`, id, reason)
	return err
}

// EventView is the read model for GET /events/{id}: raw status joined with its fact.
type EventView struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	Error          *string    `json:"error,omitempty"`
	Classification *string    `json:"classification,omitempty"`
	EntityKey      *string    `json:"entity_key,omitempty"`
	CanonicalState *string    `json:"canonical_state,omitempty"`
	Confidence     *float64   `json:"confidence,omitempty"`
	ReceivedAt     time.Time  `json:"received_at"`
	ProcessedAt    *time.Time `json:"processed_at,omitempty"`
}

// GetEvent returns the raw status plus the normalized fact (if processing finished).
func (s *Store) GetEvent(ctx context.Context, id string) (*EventView, error) {
	var v EventView
	err := s.Pool.QueryRow(ctx, `
		SELECT r.id, r.status, r.attempts, r.error, r.received_at,
		       n.classification, n.entity_key, n.canonical_state, n.confidence, n.created_at
		FROM raw_events r
		LEFT JOIN normalized_events n ON n.raw_event_id = r.id
		WHERE r.id = $1`, id).
		Scan(&v.ID, &v.Status, &v.Attempts, &v.Error, &v.ReceivedAt,
			&v.Classification, &v.EntityKey, &v.CanonicalState, &v.Confidence, &v.ProcessedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}
