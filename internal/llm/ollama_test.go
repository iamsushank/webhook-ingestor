package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllama_NormalizeParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %s, want /api/chat", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		// Ollama wraps the model's JSON string in message.content.
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"{\"classification\":\"INVOICE\",\"entity_key\":\"GFP-1\",\"canonical_state\":\"PAID\",\"event_time\":\"\",\"amount_minor\":2435075,\"currency\":\"EUR\",\"vendor_state_text\":\"settled in full\",\"confidence\":0.95}"},"done":true}`)
	}))
	defer srv.Close()

	res, err := NewOllama("llama3.2", srv.URL).Normalize(context.Background(), []byte(`{"x":1}`))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if res.Classification != "INVOICE" || res.EntityKey != "GFP-1" || res.CanonicalState != "PAID" {
		t.Errorf("parsed wrong: %+v", res.Response)
	}
	if res.AmountMinor == nil || *res.AmountMinor != 2435075 || res.Currency != "EUR" {
		t.Errorf("money parsed wrong: amount=%v currency=%q", res.AmountMinor, res.Currency)
	}
	if res.Model != "llama3.2" {
		t.Errorf("model = %q, want llama3.2", res.Model)
	}
}
