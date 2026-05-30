// Package worker is the generic consumer: it pulls jobs from a queue.Queue and runs
// them through a Handler, applying ack / retry / dead-letter based on the result. It
// has no knowledge of the LLM, the database, or which broker backs the queue — those
// live behind the Queue and Handler interfaces.
package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"webhook-ingestor/internal/queue"
)

// Handler processes one job. Returning a queue.Permanent error dead-letters the job;
// any other error schedules a retry; nil acks it.
type Handler interface {
	Handle(ctx context.Context, job *queue.Job) error
}

// Pool runs Concurrency consumer goroutines against one queue + handler.
type Pool struct {
	queue       queue.Queue
	handler     Handler
	concurrency int
	log         *slog.Logger
}

func NewPool(q queue.Queue, h Handler, concurrency int, log *slog.Logger) *Pool {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Pool{queue: q, handler: h, concurrency: concurrency, log: log}
}

// Run starts the pool and blocks until ctx is cancelled and all workers exit. A job
// interrupted by shutdown is not lost: its lease expires and a future worker reclaims
// it (at-least-once delivery + idempotent handling = effectively-once).
func (p *Pool) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < p.concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			p.worker(ctx, id)
		}(i)
	}
	p.log.Info("worker pool started", "concurrency", p.concurrency)
	wg.Wait()
	p.log.Info("worker pool stopped")
}

func (p *Pool) worker(ctx context.Context, id int) {
	for {
		job, err := p.queue.Claim(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.log.Error("claim failed", "worker", id, "err", err)
			// Avoid a hot loop if the broker is briefly unavailable.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if job == nil {
			continue // idle timeout from a long-poll Claim
		}
		p.dispatch(ctx, job)
	}
}

// dispatch runs the handler and routes the outcome to the queue lifecycle.
func (p *Pool) dispatch(ctx context.Context, job *queue.Job) {
	switch err := p.handler.Handle(ctx, job); {
	case err == nil:
		if ackErr := p.queue.Ack(ctx, job.ID); ackErr != nil {
			p.log.Error("ack failed", "id", job.ID, "err", ackErr)
		}
	case queue.IsPermanent(err):
		if failErr := p.queue.Fail(ctx, job.ID, err.Error()); failErr != nil {
			p.log.Error("fail failed", "id", job.ID, "err", failErr)
		}
	default:
		if retryErr := p.queue.Retry(ctx, job.ID, job.Attempts, err.Error()); retryErr != nil {
			p.log.Error("retry failed", "id", job.ID, "err", retryErr)
		}
	}
}
