// Package normalize is the validation gate between the LLM and the database. It
// turns a raw llm.Response into a strict model.Normalized fact, or rejects it. The
// LLM obtains JSON; this package decides whether that JSON is allowed to be persisted.
package normalize

import (
	"fmt"
	"strings"
	"time"

	"webhook-ingestor/internal/llm"
	"webhook-ingestor/internal/model"
)

// Normalizer validates and canonicalizes LLM output.
type Normalizer struct {
	// MinConfidence: a classified result below this is downgraded to UNCLASSIFIED.
	MinConfidence float64
}

func New(minConfidence float64) *Normalizer {
	return &Normalizer{MinConfidence: minConfidence}
}

// Apply validates and canonicalizes a raw LLM response into an internal fact.
//
// It returns an error only for structurally incoherent output (state not in the
// class's lifecycle, missing entity_key) — the worker treats that as a failure and
// retries/DLQs it, so bad data never reaches the projection. Genuine uncertainty
// (confidence below the floor) is downgraded to UNCLASSIFIED, which is a valid,
// storable outcome — not an error.
func (n *Normalizer) Apply(r llm.Response) (model.Normalized, error) {
	class := model.Classification(strings.ToUpper(strings.TrimSpace(r.Classification)))

	// Confidence gate: unsure -> UNCLASSIFIED (not a retry).
	if class != model.ClassUnclassified && r.Confidence < n.MinConfidence {
		class = model.ClassUnclassified
	}

	out := model.Normalized{
		Classification:  class,
		VendorStateText: strings.TrimSpace(r.VendorStateText),
		Confidence:      r.Confidence,
		EventTime:       parseEventTime(r.EventTime),
	}

	switch class {
	case model.ClassUnclassified:
		return out, nil // no entity, no state, no money

	case model.ClassShipment:
		state := strings.ToUpper(strings.TrimSpace(r.CanonicalState))
		if !model.IsShipmentState(state) {
			return model.Normalized{}, fmt.Errorf("shipment canonical_state %q not recognized", r.CanonicalState)
		}
		key := CanonicalKey(r.EntityKey)
		if key == "" {
			return model.Normalized{}, fmt.Errorf("shipment missing entity_key")
		}
		out.CanonicalState = state
		out.EntityKey = key
		return out, nil // shipments carry no money

	case model.ClassInvoice:
		state := strings.ToUpper(strings.TrimSpace(r.CanonicalState))
		if !model.IsInvoiceState(state) {
			return model.Normalized{}, fmt.Errorf("invoice canonical_state %q not recognized", r.CanonicalState)
		}
		key := CanonicalKey(r.EntityKey)
		if key == "" {
			return model.Normalized{}, fmt.Errorf("invoice missing entity_key")
		}
		out.CanonicalState = state
		out.EntityKey = key
		out.AmountMinor, out.Currency = sanitizeMoney(r.AmountMinor, r.Currency)
		return out, nil

	default:
		return model.Normalized{}, fmt.Errorf("unknown classification %q", r.Classification)
	}
}

// CanonicalKey makes entity correlation deterministic across the app: case and
// whitespace are normalized in code (not at the DB collation layer), so the same
// function is used on write here and on read at the entity-lookup endpoint.
func CanonicalKey(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// sanitizeMoney keeps amount and currency only as a coherent pair — a valid 3-letter
// alphabetic currency and a non-negative amount. Anything else drops both, so the DB
// never holds an amount without a currency (or vice versa).
func sanitizeMoney(amount *int64, currency string) (*int64, string) {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if amount == nil || *amount < 0 || len(cur) != 3 || !isAlpha(cur) {
		return nil, ""
	}
	return amount, cur
}

func isAlpha(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// eventTimeLayouts covers RFC3339 plus the common vendor variants (space separator,
// date only). A named-zone string like "WIB" is intentionally not handled — Go can't
// resolve it unambiguously, so it falls through to nil.
var eventTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05-07:00",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// parseEventTime best-effort parses a timestamp into UTC. event_time is a secondary
// ordering signal (canonical rank is primary), so an unparseable value yields nil —
// never an error that would fail the whole record.
func parseEventTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range eventTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
