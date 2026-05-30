package llm

import (
	"context"
	"encoding/json"
	"strings"

	"webhook-ingestor/internal/llm/spec"
)

// Mock is a deterministic, rule-based Client for offline runs and hermetic tests:
// no network, no API key. It is not as smart as the model, but it classifies and
// maps the sample payloads well enough to exercise the full pipeline end to end.
type Mock struct{}

func NewMock() *Mock { return &Mock{} }

func (m *Mock) Normalize(_ context.Context, raw []byte) (Result, error) {
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	flat := strings.ToLower(string(raw))

	r := Response{Confidence: 0.9}
	switch {
	case looksInvoice(p, flat):
		r.Classification = "INVOICE"
		r.CanonicalState = invoiceState(flat)
		r.EntityKey = strings.ToUpper(firstString(p, "doc_ref", "invoice_id", "invoice_no", "invoice_ref"))
		r.VendorStateText = txKind(p)
		r.EventTime = firstTimeString(p)
	case looksShipment(p):
		r.Classification = "SHIPMENT"
		r.CanonicalState = shipmentState(flat)
		r.EntityKey = strings.ToUpper(shipmentKey(p))
		r.VendorStateText = firstString(p, "milestone", "milestone_text", "milestone_local_time")
		r.EventTime = firstTimeString(p)
	default:
		r.Classification = "UNCLASSIFIED"
		r.Confidence = 0.3
	}
	if r.Classification != "UNCLASSIFIED" && r.CanonicalState == "" {
		r.Confidence = 0.4 // classified but state unclear
	}
	return Result{Response: r, Model: "mock", PromptVer: spec.PromptVersion}, nil
}

func looksInvoice(p map[string]any, flat string) bool {
	if _, ok := p["transaction"]; ok {
		return true
	}
	if _, ok := p["doc_ref"]; ok {
		return true
	}
	return strings.Contains(flat, "invoice") || strings.Contains(flat, "remitter")
}

func looksShipment(p map[string]any) bool {
	for _, k := range []string{"container", "container_no", "transport_doc", "carrier_scac", "milestone", "milestone_text", "house_bl", "master_bl"} {
		if _, ok := p[k]; ok {
			return true
		}
	}
	return false
}

func shipmentState(flat string) string {
	switch {
	case strings.Contains(flat, "out for delivery"):
		return "OUT_FOR_DELIVERY"
	case strings.Contains(flat, "released to consignee"), strings.Contains(flat, "delivered"), strings.Contains(flat, "consignee facility"):
		return "DELIVERED"
	case strings.Contains(flat, "received at origin"), strings.Contains(flat, "gate-in"), strings.Contains(flat, "gate in"), strings.Contains(flat, "empty container released"), strings.Contains(flat, "picked up"), strings.Contains(flat, "pickup"):
		return "PICKED_UP"
	case strings.Contains(flat, "sailed"), strings.Contains(flat, "departed"), strings.Contains(flat, "loaded onboard"), strings.Contains(flat, "in transit"), strings.Contains(flat, "vessel"):
		return "IN_TRANSIT"
	default:
		return ""
	}
}

func invoiceState(flat string) string {
	switch {
	case strings.Contains(flat, "refund"):
		return "REFUNDED"
	case strings.Contains(flat, "void"), strings.Contains(flat, "cancel"):
		return "VOIDED"
	case strings.Contains(flat, "settled"), strings.Contains(flat, "paid"), strings.Contains(flat, "payment received"):
		return "PAID"
	case strings.Contains(flat, "raised"), strings.Contains(flat, "issued"):
		return "ISSUED"
	default:
		return ""
	}
}

// shipmentKey prefers the master transport doc, then house BL, then container.
func shipmentKey(p map[string]any) string {
	if td, ok := p["transport_doc"].(map[string]any); ok {
		if n, ok := td["number"].(string); ok && n != "" {
			return n
		}
	}
	return firstString(p, "master_bl", "house_bl", "container_no", "container")
}

func firstString(p map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := p[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// firstTimeString returns the first timestamp-ish field found (top level or inside
// "transaction"), as the vendor wrote it. The normalize layer parses it to UTC.
func firstTimeString(p map[string]any) string {
	keys := []string{"milestone_at", "milestone_local_time", "issued_at", "settled_at", "occurred_at", "event_time", "timestamp"}
	if v := firstString(p, keys...); v != "" {
		return v
	}
	if tx, ok := p["transaction"].(map[string]any); ok {
		return firstString(tx, keys...)
	}
	return ""
}

func txKind(p map[string]any) string {
	if tx, ok := p["transaction"].(map[string]any); ok {
		if k, ok := tx["kind"].(string); ok {
			return k
		}
	}
	return ""
}
