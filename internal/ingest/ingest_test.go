package ingest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webhook-ingestor/internal/model"
	"webhook-ingestor/internal/store"
)

type fakeStore struct {
	insertNew bool
	gotHash   string
	gotKey    string
	event     *store.EventView
	entity    *model.Entity
}

func (f *fakeStore) InsertRaw(_ context.Context, hash string, _ []byte, _ string) (string, bool, error) {
	f.gotHash = hash
	return "raw-1", f.insertNew, nil
}
func (f *fakeStore) GetEvent(context.Context, string) (*store.EventView, error) { return f.event, nil }
func (f *fakeStore) GetEntity(_ context.Context, key string) (*model.Entity, error) {
	f.gotKey = key
	return f.entity, nil
}
func (f *fakeStore) Ping(context.Context) error { return nil }

type fakeNotifier struct{ calls int }

func (f *fakeNotifier) Notify() { f.calls++ }

func newAPI(st Store, n Notifier) http.Handler {
	return New(st, n, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhooks", strings.NewReader(body)))
	return rec
}

func TestIngest_NewPayloadAcceptedAndNotifies(t *testing.T) {
	st := &fakeStore{insertNew: true}
	n := &fakeNotifier{}
	rec := post(newAPI(st, n), `{"carrier_scac":"MAEU"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if n.calls != 1 {
		t.Errorf("Notify calls = %d, want 1", n.calls)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["duplicate"] != false {
		t.Errorf("duplicate = %v, want false", resp["duplicate"])
	}
	if st.gotHash == "" {
		t.Error("expected a content hash to be computed")
	}
}

func TestIngest_DuplicateAcceptedButNotNotified(t *testing.T) {
	st := &fakeStore{insertNew: false} // store reports an existing row
	n := &fakeNotifier{}
	rec := post(newAPI(st, n), `{"carrier_scac":"MAEU"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (idempotent ack)", rec.Code)
	}
	if n.calls != 0 {
		t.Errorf("Notify calls = %d, want 0 (no re-enqueue for a duplicate)", n.calls)
	}
}

func TestIngest_InvalidJSONRejected(t *testing.T) {
	st := &fakeStore{insertNew: true}
	n := &fakeNotifier{}
	rec := post(newAPI(st, n), `not json at all`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if n.calls != 0 || st.gotHash != "" {
		t.Error("invalid JSON must not be hashed, persisted, or enqueued")
	}
}

func TestGetEntity_CanonicalizesKey(t *testing.T) {
	st := &fakeStore{entity: &model.Entity{EntityKey: "MAEU240498712", Type: "SHIPMENT", CurrentState: "IN_TRANSIT"}}
	h := newAPI(st, &fakeNotifier{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/entities/maeu240498712", nil))

	if st.gotKey != "MAEU240498712" {
		t.Errorf("store queried with %q, want canonicalized MAEU240498712", st.gotKey)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestGetEntity_NotFound(t *testing.T) {
	st := &fakeStore{entity: nil}
	h := newAPI(st, &fakeNotifier{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/entities/UNKNOWN", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestGetEvent_InvalidIDIsBadRequest(t *testing.T) {
	h := newAPI(&fakeStore{}, &fakeNotifier{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events/not-a-uuid", nil))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (malformed id, not a 500)", rec.Code)
	}
}

func TestGetEvent_ValidButMissingIsNotFound(t *testing.T) {
	h := newAPI(&fakeStore{event: nil}, &fakeNotifier{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events/123e4567-e89b-12d3-a456-426614174000", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
