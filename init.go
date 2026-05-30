package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"webhook-ingestor/internal/ingest"
	"webhook-ingestor/internal/llm"
	"webhook-ingestor/internal/normalize"
	"webhook-ingestor/internal/processor"
	"webhook-ingestor/internal/queue"
	"webhook-ingestor/internal/store"
	"webhook-ingestor/internal/worker"
)

// App holds the wired dependency graph and runs the role selected by Config.Mode.
type App struct {
	cfg   Config
	log   *slog.Logger
	store *store.Store
	pool  *worker.Pool
	api   *ingest.API
}

// newApp connects to Postgres (running migrations on boot), then wires the dependency
// graph: store -> queue + processor -> worker pool; store + queue -> ingest API.
// Swapping the LLM provider or the queue backend is a one-line change here.
func newApp(ctx context.Context, cfg Config, log *slog.Logger) (*App, error) {
	st, err := connectWithRetry(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}
	log.Info("migrations applied")

	q := queue.NewPostgres(st, cfg.Queue, log)
	proc := processor.New(buildLLM(cfg, log), normalize.New(cfg.ConfidenceThreshold), st, log)
	pool := worker.NewPool(q, proc, cfg.WorkerConcurrency, log)
	api := ingest.New(st, q, log)

	return &App{cfg: cfg, log: log, store: st, pool: pool, api: api}, nil
}

func (a *App) Close() { a.store.Close() }

// Run executes the configured process role and blocks until ctx is cancelled. The
// roles share only the database, so server and worker scale independently as separate
// containers (server inserts rows; workers claim them).
func (a *App) Run(ctx context.Context) error {
	switch a.cfg.Mode {
	case "server":
		a.log.Info("starting in server mode (HTTP only)")
		return a.runServer(ctx)
	case "worker":
		a.log.Info("starting in worker mode (queue consumer only)")
		a.pool.Run(ctx)
		return nil
	default: // "all" — both roles in one process (dev / single container)
		a.log.Info("starting in all mode (HTTP + worker)")
		return a.runAll(ctx)
	}
}

// runServer serves the ingest API until ctx is cancelled or the listener fails, then
// drains in-flight requests within a timeout.
func (a *App) runServer(ctx context.Context) error {
	srv := &http.Server{
		Addr:              a.cfg.HTTPAddr,
		Handler:           a.api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		a.log.Info("http server listening", "addr", a.cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		a.log.Error("http server error", "err", err)
	}

	a.log.Info("shutdown initiated")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// runAll runs the HTTP server and the worker pool together. If either returns, the
// other is cancelled so the process exits cleanly.
func (a *App) runAll(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var wg sync.WaitGroup
	wg.Go(func() {
		a.pool.Run(ctx)
	})

	err := a.runServer(ctx)
	cancel() // server returned -> stop the pool
	wg.Wait()
	return err
}

// connectWithRetry tolerates the database not being ready yet (common under
// docker-compose where Postgres and the app start together).
func connectWithRetry(ctx context.Context, dsn string, log *slog.Logger) (*store.Store, error) {
	const attempts = 30
	var lastErr error
	for i := range attempts {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		st, err := store.New(ctx, dsn)
		if err == nil {
			return st, nil
		}
		lastErr = err
		log.Warn("waiting for database", "attempt", i+1, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, lastErr
}

// buildLLM selects the provider. LLM_PROVIDER forces a choice (openai|ollama|mock);
// otherwise it auto-detects: a hosted provider when a key is present, else the
// deterministic mock so the service still runs end to end without any credentials.
func buildLLM(cfg Config, log *slog.Logger) llm.Client {
	switch strings.ToLower(cfg.LLMProvider) {
	case "ollama":
		log.Info("llm provider: ollama", "model", cfg.LLMModel, "baseURL", cfg.LLMBaseURL)
		return llm.NewOllama(cfg.LLMModel, cfg.LLMBaseURL)
	case "mock":
		log.Warn("llm provider: mock (forced)")
		return llm.NewMock()
	case "openai":
		log.Info("llm provider: openai", "model", cfg.LLMModel, "baseURL", cfg.LLMBaseURL)
		return llm.NewOpenAI(cfg.LLMAPIKey, cfg.LLMModel, cfg.LLMBaseURL)
	default:
		if cfg.LLMAPIKey != "" {
			log.Info("llm provider: openai (auto)", "model", cfg.LLMModel, "baseURL", cfg.LLMBaseURL)
			return llm.NewOpenAI(cfg.LLMAPIKey, cfg.LLMModel, cfg.LLMBaseURL)
		}
		log.Warn("no LLM_API_KEY set — using deterministic mock client")
		return llm.NewMock()
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
