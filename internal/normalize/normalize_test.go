package normalize

import (
	"testing"
	"time"

	"webhook-ingestor/internal/llm"
	"webhook-ingestor/internal/model"
)

func TestApply_ShipmentCanonicalizesKeyAndTime(t *testing.T) {
	n := New(0.6)
	got, err := n.Apply(llm.Response{
		Classification: "SHIPMENT",
		EntityKey:      "  maeu240498712 ",
		CanonicalState: "in_transit",
		EventTime:      "2026-04-21T22:47:00+08:00",
		Confidence:     0.95,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.EntityKey != "MAEU240498712" {
		t.Errorf("entity_key = %q, want MAEU240498712", got.EntityKey)
	}
	if got.CanonicalState != model.StateInTransit {
		t.Errorf("state = %q, want IN_TRANSIT", got.CanonicalState)
	}
	// +08:00 22:47 -> 14:47Z
	want := time.Date(2026, 4, 21, 14, 47, 0, 0, time.UTC)
	if got.EventTime == nil || !got.EventTime.Equal(want) {
		t.Errorf("event_time = %v, want %v (UTC)", got.EventTime, want)
	}
}

func TestApply_LowConfidenceDowngradesToUnclassified(t *testing.T) {
	n := New(0.6)
	got, err := n.Apply(llm.Response{
		Classification: "SHIPMENT",
		EntityKey:      "MAEU240498712",
		CanonicalState: "IN_TRANSIT",
		Confidence:     0.4,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.Classification != model.ClassUnclassified {
		t.Errorf("classification = %q, want UNCLASSIFIED", got.Classification)
	}
	if got.EntityKey != "" || got.CanonicalState != "" {
		t.Errorf("downgraded record should carry no entity/state, got key=%q state=%q", got.EntityKey, got.CanonicalState)
	}
}

func TestApply_StateClassMismatchIsError(t *testing.T) {
	n := New(0.6)
	// schema-valid state, but DELIVERED is not an invoice state -> incoherent -> error
	_, err := n.Apply(llm.Response{
		Classification: "INVOICE",
		EntityKey:      "GFP-INV-1",
		CanonicalState: "DELIVERED",
		Confidence:     0.9,
	})
	if err == nil {
		t.Fatal("expected error for invoice with shipment state, got nil")
	}
}

func TestApply_MissingEntityKeyIsError(t *testing.T) {
	n := New(0.6)
	_, err := n.Apply(llm.Response{
		Classification: "SHIPMENT",
		CanonicalState: "PICKED_UP",
		Confidence:     0.9,
	})
	if err == nil {
		t.Fatal("expected error for shipment without entity_key, got nil")
	}
}

func TestApply_InvoiceMoney(t *testing.T) {
	amt := int64(2435075)
	n := New(0.6)

	got, err := n.Apply(llm.Response{
		Classification: "INVOICE", EntityKey: "GFP-INV-1", CanonicalState: "PAID",
		AmountMinor: &amt, Currency: "eur", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.AmountMinor == nil || *got.AmountMinor != 2435075 || got.Currency != "EUR" {
		t.Errorf("money = (%v, %q), want (2435075, EUR)", got.AmountMinor, got.Currency)
	}

	// invalid currency -> both dropped
	bad, _ := n.Apply(llm.Response{
		Classification: "INVOICE", EntityKey: "GFP-INV-1", CanonicalState: "PAID",
		AmountMinor: &amt, Currency: "EURO", Confidence: 0.9,
	})
	if bad.AmountMinor != nil || bad.Currency != "" {
		t.Errorf("bad currency should drop money, got (%v, %q)", bad.AmountMinor, bad.Currency)
	}
}

func TestApply_UnparseableTimeIsNilNotError(t *testing.T) {
	n := New(0.6)
	got, err := n.Apply(llm.Response{
		Classification: "SHIPMENT", EntityKey: "ONEYMBLHKG260499", CanonicalState: "DELIVERED",
		EventTime: "28/04/2026 09:42 WIB", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.EventTime != nil {
		t.Errorf("named-zone time should be nil, got %v", got.EventTime)
	}
}
