// Package model holds the core domain types and the canonical state machines.
// It has no dependencies, so every other package can import it without cycles.
package model

import (
	"slices"
	"time"
)

// Classification is the event type the LLM assigns to a payload.
type Classification string

const (
	ClassShipment     Classification = "SHIPMENT"
	ClassInvoice      Classification = "INVOICE"
	ClassUnclassified Classification = "UNCLASSIFIED"
)

// Shipment lifecycle is a total linear order, so a single rank comparison is the
// out-of-order / concurrency guard: only advance when new rank > current rank.
const (
	StatePickedUp       = "PICKED_UP"
	StateInTransit      = "IN_TRANSIT"
	StateOutForDelivery = "OUT_FOR_DELIVERY"
	StateDelivered      = "DELIVERED"
)

// shipmentRank maps each shipment state to its lifecycle position.
var shipmentRank = map[string]int{
	StatePickedUp:       1,
	StateInTransit:      2,
	StateOutForDelivery: 3,
	StateDelivered:      4,
}

// ShipmentRank returns the lifecycle rank of a shipment state (0 if unknown).
func ShipmentRank(state string) int { return shipmentRank[state] }

// Invoice lifecycle branches into two terminals from different predecessors
// (VOIDED only from ISSUED, REFUNDED only from PAID), so rank alone can't express
// legality — it needs an explicit predecessor-aware transition table.
const (
	StateIssued   = "ISSUED"
	StatePaid     = "PAID"
	StateVoided   = "VOIDED"
	StateRefunded = "REFUNDED"
)

var invoiceTransitions = map[string][]string{
	StateIssued:   {StatePaid, StateVoided},
	StatePaid:     {StateRefunded},
	StateVoided:   {}, // terminal
	StateRefunded: {}, // terminal
}

// InvoiceTransitionAllowed reports whether from -> to is a legal invoice move.
func InvoiceTransitionAllowed(from, to string) bool {
	return slices.Contains(invoiceTransitions[from], to)
}

// IsShipmentState reports whether s is a known canonical shipment state.
func IsShipmentState(s string) bool { _, ok := shipmentRank[s]; return ok }

// IsInvoiceState reports whether s is a known canonical invoice state.
func IsInvoiceState(s string) bool { _, ok := invoiceTransitions[s]; return ok }

// RawStatus is the processing status of a raw ingestion record.
type RawStatus string

const (
	StatusPending    RawStatus = "PENDING"
	StatusProcessing RawStatus = "PROCESSING"
	StatusProcessed  RawStatus = "PROCESSED"
	StatusFailed     RawStatus = "FAILED"
)

// RawEvent is a claimed unit of work handed to a worker.
type RawEvent struct {
	ID       string
	Payload  []byte // verbatim vendor JSON
	Attempts int    // incremented at claim time
}

// Normalized is the LLM's structured output for one raw event — one immutable fact.
// Nullable fields use pointers / empty strings so UNCLASSIFIED rows carry no entity.
type Normalized struct {
	Classification  Classification `json:"classification"`
	EntityKey       string         `json:"entity_key"`
	CanonicalState  string         `json:"canonical_state"`
	EventTime       *time.Time     `json:"event_time"`
	AmountMinor     *int64         `json:"amount_minor"`
	Currency        string         `json:"currency"`
	VendorStateText string         `json:"vendor_state_text"`
	Confidence      float64        `json:"confidence"`
}

// Entity is the current-state projection of a shipment or invoice.
type Entity struct {
	EntityKey     string     `json:"entity_key"`
	Type          string     `json:"type"`
	CurrentState  string     `json:"current_state"`
	CurrentRank   int        `json:"current_rank"`
	LastEventTime *time.Time `json:"last_event_time"`
	AmountMinor   *int64     `json:"amount_minor"`
	Currency      *string    `json:"currency"`
	UpdatedAt     time.Time  `json:"updated_at"`
}
