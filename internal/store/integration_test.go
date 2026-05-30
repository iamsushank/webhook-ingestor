package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"webhook-ingestor/internal/model"
)

// These exercise the data-integrity guards against a real Postgres. They are skipped
// unless TEST_DATABASE_URL is set, e.g.:
//
//	TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5643/webhooks?sslmode=disable go test ./internal/store/
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run store integration tests")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func uniq(t *testing.T) string {
	return fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())
}

func TestInsertRaw_Idempotent(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	hash := uniq(t)
	t.Cleanup(func() { st.Pool.Exec(ctx, `DELETE FROM raw_events WHERE payload_hash=$1`, hash) })

	id1, new1, err := st.InsertRaw(ctx, hash, []byte(`{"a":1}`), "")
	if err != nil || !new1 {
		t.Fatalf("first insert: id=%s new=%v err=%v (want new=true)", id1, new1, err)
	}
	id2, new2, err := st.InsertRaw(ctx, hash, []byte(`{"different":"body"}`), "")
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if new2 {
		t.Error("second insert with same hash reported new=true (want false)")
	}
	if id1 != id2 {
		t.Errorf("duplicate returned id %s, want original %s", id2, id1)
	}
}

func TestApplyShipment_NoRegressionThenForward(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	key := uniq(t)
	t.Cleanup(func() { st.Pool.Exec(ctx, `DELETE FROM entities WHERE entity_key=$1`, key) })

	now := time.Now().UTC()
	// DELIVERED first, then a late earlier-lifecycle PICKED_UP must NOT regress it.
	mustApplyShipment(t, st, key, model.StateDelivered, new(now))
	mustApplyShipment(t, st, key, model.StatePickedUp, new(now.Add(-72*time.Hour)))

	if got := mustEntity(t, st, key); got.CurrentState != model.StateDelivered {
		t.Fatalf("state = %s, want DELIVERED (late PICKED_UP regressed it)", got.CurrentState)
	}

	// And a forward move on a fresh key advances.
	key2 := uniq(t)
	t.Cleanup(func() { st.Pool.Exec(ctx, `DELETE FROM entities WHERE entity_key=$1`, key2) })
	mustApplyShipment(t, st, key2, model.StatePickedUp, new(now))
	mustApplyShipment(t, st, key2, model.StateInTransit, new(now.Add(time.Hour)))
	if got := mustEntity(t, st, key2); got.CurrentState != model.StateInTransit {
		t.Fatalf("state = %s, want IN_TRANSIT", got.CurrentState)
	}
}

func TestApplyShipment_ConcurrentConverges(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	key := uniq(t)
	t.Cleanup(func() { st.Pool.Exec(ctx, `DELETE FROM entities WHERE entity_key=$1`, key) })

	states := []string{model.StatePickedUp, model.StateInTransit, model.StateOutForDelivery, model.StateDelivered}
	now := time.Now().UTC()

	var wg sync.WaitGroup
	for i := range 40 { // many workers race to update the same entity
		s := states[i%len(states)]
		wg.Go(func() {
			_ = st.ApplyShipment(ctx, key, s, model.ShipmentRank(s), new(now))
		})
	}
	wg.Wait()

	got := mustEntity(t, st, key)
	if got.CurrentState != model.StateDelivered || got.CurrentRank != 4 {
		t.Fatalf("converged to %s/rank %d, want DELIVERED/4 (max-rank guard)", got.CurrentState, got.CurrentRank)
	}
}

func TestApplyInvoice_TransitionTable(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	key := uniq(t)
	t.Cleanup(func() { st.Pool.Exec(ctx, `DELETE FROM entities WHERE entity_key=$1`, key) })
	now := time.Now().UTC()

	mustApplyInvoice(t, st, key, model.StateIssued, new(now))
	assertInvoiceState(t, st, key, model.StateIssued)

	mustApplyInvoice(t, st, key, model.StatePaid, new(now.Add(time.Hour)))
	assertInvoiceState(t, st, key, model.StatePaid)

	// Illegal PAID -> VOIDED must be ignored (VOIDED is only reachable from ISSUED).
	mustApplyInvoice(t, st, key, model.StateVoided, new(now.Add(2*time.Hour)))
	assertInvoiceState(t, st, key, model.StatePaid)

	// Legal PAID -> REFUNDED.
	mustApplyInvoice(t, st, key, model.StateRefunded, new(now.Add(3*time.Hour)))
	assertInvoiceState(t, st, key, model.StateRefunded)
}

func TestInsertNormalized_IdempotentOnRawEvent(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	hash := uniq(t)
	rawID, _, err := st.InsertRaw(ctx, hash, []byte(`{"x":1}`), "")
	if err != nil {
		t.Fatalf("insert raw: %v", err)
	}
	t.Cleanup(func() {
		st.Pool.Exec(ctx, `DELETE FROM normalized_events WHERE raw_event_id=$1`, rawID)
		st.Pool.Exec(ctx, `DELETE FROM raw_events WHERE id=$1`, rawID)
	})

	fact := model.Normalized{Classification: model.ClassShipment, EntityKey: "K", CanonicalState: model.StatePickedUp}
	for range 2 { // a retried worker re-runs the LLM and re-writes
		if err := st.InsertNormalized(ctx, rawID, fact, "mock", "v1"); err != nil {
			t.Fatalf("insert normalized: %v", err)
		}
	}

	var n int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM normalized_events WHERE raw_event_id=$1`, rawID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("normalized rows = %d, want 1 (raw_event_id UNIQUE)", n)
	}
}

// helpers

func mustApplyShipment(t *testing.T, st *Store, key, state string, et *time.Time) {
	t.Helper()
	if err := st.ApplyShipment(context.Background(), key, state, model.ShipmentRank(state), et); err != nil {
		t.Fatalf("ApplyShipment(%s): %v", state, err)
	}
}

func mustApplyInvoice(t *testing.T, st *Store, key, state string, et *time.Time) {
	t.Helper()
	if err := st.ApplyInvoice(context.Background(), key, state, et, nil, ""); err != nil {
		t.Fatalf("ApplyInvoice(%s): %v", state, err)
	}
}

func mustEntity(t *testing.T, st *Store, key string) *model.Entity {
	t.Helper()
	e, err := st.GetEntity(context.Background(), key)
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if e == nil {
		t.Fatalf("entity %s not found", key)
	}
	return e
}

func assertInvoiceState(t *testing.T, st *Store, key, want string) {
	t.Helper()
	if got := mustEntity(t, st, key); got.CurrentState != want {
		t.Fatalf("invoice state = %s, want %s", got.CurrentState, want)
	}
}
