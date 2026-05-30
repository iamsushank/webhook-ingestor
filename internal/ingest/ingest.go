// Package ingest is the fast path: the HTTP boundary that accepts arbitrary vendor
// JSON, deduplicates it, persists the raw record, signals the worker, and returns a
// sub-second 202 — with no LLM call in the request path. It also serves read-only
// status/projection endpoints. It depends on narrow interfaces (Store, Notifier),
// not concrete types, so the handlers are unit-testable without a database.
package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"webhook-ingestor/internal/model"
	"webhook-ingestor/internal/normalize"
	"webhook-ingestor/internal/store"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// uuidPattern validates the {id} path param up front, so a malformed id returns 400
// instead of a 500 from Postgres rejecting an invalid uuid cast.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Store is the persistence surface the HTTP layer needs.
type Store interface {
	InsertRaw(ctx context.Context, hash string, payload []byte, source string) (id string, isNew bool, err error)
	GetEvent(ctx context.Context, id string) (*store.EventView, error)
	GetEntity(ctx context.Context, key string) (*model.Entity, error)
	Ping(ctx context.Context) error
}

// Notifier is the one-method slice of the queue the ingest path uses: a hint that
// new work is available. Depending on this (not the whole Queue) keeps ingest decoupled.
type Notifier interface {
	Notify()
}

type API struct {
	store    Store
	notifier Notifier
	log      *slog.Logger
}

func New(st Store, notifier Notifier, log *slog.Logger) *API {
	return &API{store: st, notifier: notifier, log: log}
}

// Routes returns the mux. Go 1.22 method+pattern routing — no framework needed.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks", a.ingest)
	mux.HandleFunc("GET /events/{id}", a.getEvent)
	mux.HandleFunc("GET /entities/{key}", a.getEntity)
	mux.HandleFunc("GET /healthz", a.health)
	return a.withLogging(mux)
}

// withLogging logs each request + response at DEBUG (method, path, status, duration,
// and both bodies). Body capture is skipped entirely unless debug is enabled, so it
// costs nothing at higher log levels. Toggle via LOG_LEVEL=debug.
func (a *API) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.log.Enabled(r.Context(), slog.LevelDebug) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		var reqBody []byte
		if r.Body != nil {
			reqBody, _ = io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
			r.Body = io.NopCloser(bytes.NewReader(reqBody)) // restore for the handler
		}
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		a.log.Debug("http request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration", time.Since(start).String(),
			"request", string(reqBody), "response", rec.body.String())
	})
}

// responseRecorder captures status + body for debug logging while writing through to
// the real ResponseWriter.
type responseRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// ingest validates the JSON, deduplicates by content hash, persists the raw event,
// wakes a worker, and acknowledges with 202. The ack means "durably received" — never
// "processed". Processing status is read separately via GET /events/{id}.
func (a *API) ingest(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large or unreadable")
		return
	}
	if !json.Valid(body) {
		// The only legitimate 400 on this endpoint: the client sent non-JSON. A later
		// LLM/normalization failure is OUR problem and never returns a 4xx to the vendor.
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	source := strings.TrimSpace(r.Header.Get("X-Webhook-Source"))

	id, isNew, err := a.store.InsertRaw(r.Context(), hash, body, source)
	if err != nil {
		a.log.Error("ingest: persist raw failed", "err", err)
		writeError(w, http.StatusInternalServerError, "could not accept webhook")
		return
	}
	if isNew {
		a.notifier.Notify() // a duplicate is already enqueued/processed — don't re-signal
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":        id,
		"status":    "accepted",
		"duplicate": !isNew,
	})
}

func (a *API) getEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid event id")
		return
	}
	ev, err := a.store.GetEvent(r.Context(), id)
	if err != nil {
		a.log.Error("get event failed", "err", err)
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if ev == nil {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

// getEntity returns the current-state projection. The key is canonicalized the same
// way it was on write, so callers don't have to match casing exactly.
func (a *API) getEntity(w http.ResponseWriter, r *http.Request) {
	ent, err := a.store.GetEntity(r.Context(), normalize.CanonicalKey(r.PathValue("key")))
	if err != nil {
		a.log.Error("get entity failed", "err", err)
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if ent == nil {
		writeError(w, http.StatusNotFound, "entity not found")
		return
	}
	writeJSON(w, http.StatusOK, ent)
}

// health reports liveness plus database reachability.
func (a *API) health(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
