// Package llm is the boundary to the language model. The rest of the app depends
// only on the Client interface, so OpenAI can be swapped for any provider (or the
// deterministic Mock) without touching the worker — that's the no-lock-in promise.
package llm

import (
	"context"
	"errors"
)

// Response is the raw structured output of the model — one normalized record.
// Parsing/validation (event_time -> UTC, enum + range checks, entity_key casing)
// happens in the normalize layer, not here: this package only obtains the JSON.
type Response struct {
	Classification  string  `json:"classification"`    // SHIPMENT | INVOICE | UNCLASSIFIED
	EntityKey       string  `json:"entity_key"`        // correlation id; "" when UNCLASSIFIED
	CanonicalState  string  `json:"canonical_state"`   // canonical state; "" when UNCLASSIFIED
	EventTime       string  `json:"event_time"`        // RFC3339 UTC as produced by the model; "" if absent
	AmountMinor     *int64  `json:"amount_minor"`      // integer minor units; nil if none
	Currency        string  `json:"currency"`          // ISO-4217; "" if none
	VendorStateText string  `json:"vendor_state_text"` // original vendor words, verbatim
	Confidence      float64 `json:"confidence"`        // 0.0–1.0
}

// Result wraps a Response with the audit metadata persisted alongside the fact.
type Result struct {
	Response
	Model     string // model id that produced this
	PromptVer string // prompt version that produced this
}

// Client normalizes a raw vendor payload into a structured Response.
type Client interface {
	Normalize(ctx context.Context, raw []byte) (Result, error)
}

// APIError carries whether a provider failure is worth retrying. Transient classes
// (timeout, 429, 5xx) are retryable; a 4xx is not.
type APIError struct {
	StatusCode int
	Retryable  bool
	Message    string
}

func (e *APIError) Error() string { return e.Message }

// Retryable reports whether an error from Normalize should be retried. Unknown
// errors default to retryable — the worker's attempt cap bounds any loop.
func Retryable(err error) bool {
	if ae, ok := errors.AsType[*APIError](err); ok {
		return ae.Retryable
	}
	return true
}
