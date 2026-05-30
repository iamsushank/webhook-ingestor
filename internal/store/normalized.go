package store

import (
	"context"

	"webhook-ingestor/internal/model"
)

// InsertNormalized writes the immutable LLM fact. Idempotent on raw_event_id (UNIQUE),
// so a retried worker that re-runs the LLM can never create a second fact — the second
// write is a no-op. NULLIF collapses empty strings (UNCLASSIFIED rows) to SQL NULL.
func (s *Store) InsertNormalized(ctx context.Context, rawID string, n model.Normalized, llmModel, promptVersion string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO normalized_events
			(raw_event_id, classification, entity_key, canonical_state, event_time,
			 amount_minor, currency, vendor_state_text, confidence, llm_model, prompt_version)
		VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), $5,
		        $6, NULLIF($7,''), NULLIF($8,''), $9, $10, $11)
		ON CONFLICT (raw_event_id) DO NOTHING`,
		rawID, n.Classification, n.EntityKey, n.CanonicalState, n.EventTime,
		n.AmountMinor, n.Currency, n.VendorStateText, n.Confidence, llmModel, promptVersion)
	return err
}
