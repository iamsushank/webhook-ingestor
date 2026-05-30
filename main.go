// Command webhook-ingestor ingests vendor webhooks. The fast path (ingest HTTP)
// accepts payloads; the slow path (queue -> worker pool -> processor) normalizes them
// with an LLM. MODE selects which role this process runs: server, worker, or all.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	cfg := loadConfig()
	logger := newLogger(cfg.LogLevel)

	// One signal context drives graceful shutdown of whichever role is running.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := newApp(ctx, cfg, logger)
	if err != nil {
		logger.Error("startup failed", "err", err)
		os.Exit(1)
	}
	defer app.Close()

	if err := app.Run(ctx); err != nil {
		logger.Error("exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
