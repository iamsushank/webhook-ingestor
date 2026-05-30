package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"webhook-ingestor/internal/model"
)

// ApplyShipment advances the shipment projection iff the new state outranks the
// current one. A single conditional UPSERT is an atomic compare-and-set: concurrent
// workers and out-of-order events both converge to the max-rank state, and a late
// lower-rank event is silently ignored (the WHERE filters it out).
func (s *Store) ApplyShipment(ctx context.Context, key, state string, rank int, eventTime *time.Time) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO entities (entity_key, type, current_state, current_rank, last_event_time)
		VALUES ($1, 'SHIPMENT', $2, $3, $4)
		ON CONFLICT (entity_key) DO UPDATE
			SET current_state   = EXCLUDED.current_state,
			    current_rank    = EXCLUDED.current_rank,
			    last_event_time = EXCLUDED.last_event_time,
			    updated_at      = now()
			WHERE entities.current_rank < EXCLUDED.current_rank`,
		key, state, rank, eventTime)
	return err
}

// ApplyInvoice advances the invoice projection under a row lock, honouring the
// predecessor-aware transition table (VOIDED only from ISSUED, REFUNDED only from
// PAID). A brand-new invoice is seeded with the first observed state; an illegal or
// late transition is ignored, leaving the projection untouched.
func (s *Store) ApplyInvoice(ctx context.Context, key, state string, eventTime *time.Time, amountMinor *int64, currency string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	var current string
	err = tx.QueryRow(ctx,
		`SELECT current_state FROM entities WHERE entity_key=$1 FOR UPDATE`, key).Scan(&current)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `
			INSERT INTO entities (entity_key, type, current_state, current_rank, last_event_time, amount_minor, currency)
			VALUES ($1, 'INVOICE', $2, 0, $3, $4, NULLIF($5,''))`,
			key, state, eventTime, amountMinor, currency); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if !model.InvoiceTransitionAllowed(current, state) {
			return tx.Commit(ctx) // illegal/out-of-order transition -> keep current projection
		}
		if _, err := tx.Exec(ctx, `
			UPDATE entities SET
				current_state   = $2,
				last_event_time = $3,
				amount_minor    = COALESCE($4, amount_minor),
				currency        = COALESCE(NULLIF($5,''), currency),
				updated_at      = now()
			WHERE entity_key = $1`,
			key, state, eventTime, amountMinor, currency); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GetEntity returns the current projection for a shipment/invoice, or (nil, nil) if unknown.
func (s *Store) GetEntity(ctx context.Context, key string) (*model.Entity, error) {
	var e model.Entity
	err := s.Pool.QueryRow(ctx, `
		SELECT entity_key, type, current_state, current_rank, last_event_time, amount_minor, currency, updated_at
		FROM entities WHERE entity_key=$1`, key).
		Scan(&e.EntityKey, &e.Type, &e.CurrentState, &e.CurrentRank,
			&e.LastEventTime, &e.AmountMinor, &e.Currency, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}
