package llm

import (
	"context"
	"testing"

	"webhook-ingestor/internal/samples"
)

// The appendix payloads (loaded from the samples fixture package) pinned to their
// expected classification/state/key, so a change to the mock heuristics can't drift.
func TestMockClassifiesSamplePayloads(t *testing.T) {
	cases := []struct {
		file  string
		class string
		state string
		key   string
	}{
		{"maersk_in_transit.json", "SHIPMENT", "IN_TRANSIT", "MAEU240498712"},
		{"maersk_picked_up.json", "SHIPMENT", "PICKED_UP", "MAEU240498712"},
		{"gfp_paid.json", "INVOICE", "PAID", "GFP-INV-2026-Q2-08821"},
		{"gfp_issued.json", "INVOICE", "ISSUED", "GFP-INV-2026-Q2-08821"},
		{"one_delivered.json", "SHIPMENT", "DELIVERED", "ONEYMBLHKG260499"},
		{"advisory_unclassified.json", "UNCLASSIFIED", "", ""},
	}

	m := NewMock()
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			payload, err := samples.Read(c.file)
			if err != nil {
				t.Fatalf("read sample: %v", err)
			}
			got, err := m.Normalize(context.Background(), payload)
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if got.Classification != c.class {
				t.Errorf("classification = %q, want %q", got.Classification, c.class)
			}
			if got.CanonicalState != c.state {
				t.Errorf("canonical_state = %q, want %q", got.CanonicalState, c.state)
			}
			if got.EntityKey != c.key {
				t.Errorf("entity_key = %q, want %q", got.EntityKey, c.key)
			}
		})
	}
}
