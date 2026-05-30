// Package processor is the domain pipeline run for each job: LLM -> normalize ->
// persist fact -> project onto the entity. It implements worker.Handler but knows
// nothing about queueing or which broker delivered the job — it just returns an
// error, using queue.Permanent to mark failures that must not be retried.
package processor

import (
	"context"
	"fmt"
	"log/slog"

	"webhook-ingestor/internal/llm"
	"webhook-ingestor/internal/model"
	"webhook-ingestor/internal/normalize"
	"webhook-ingestor/internal/queue"
	"webhook-ingestor/internal/store"
)

// Processor wires the LLM client, the validation gate, and the store.
type Processor struct {
	llm   llm.Client
	norm  *normalize.Normalizer
	store *store.Store
	log   *slog.Logger
}

func New(client llm.Client, norm *normalize.Normalizer, st *store.Store, log *slog.Logger) *Processor {
	return &Processor{llm: client, norm: norm, store: st, log: log}
}

// Handle runs one job through the pipeline. The whole method is re-entrant: on a
// crash/retry it re-runs safely because the fact insert is idempotent (raw_event_id
// UNIQUE) and the projection apply is a guarded compare-and-set. Ack is the queue's
// job, not the processor's.
func (p *Processor) Handle(ctx context.Context, job *queue.Job) error {
	res, err := p.llm.Normalize(ctx, job.Payload)
	if err != nil {
		if llm.Retryable(err) {
			return fmt.Errorf("llm: %w", err) // transient -> retry
		}
		return queue.Permanentf("llm: %v", err) // 4xx -> DLQ
	}

	norm, err := p.norm.Apply(res.Response)
	if err != nil {
		// Deterministic at temperature 0 — retrying yields the same error. DLQ it.
		return queue.Permanentf("normalize: %v", err)
	}

	if err := p.store.InsertNormalized(ctx, job.ID, norm, res.Model, res.PromptVer); err != nil {
		return fmt.Errorf("persist fact: %w", err) // infra -> retry
	}
	if err := p.project(ctx, norm); err != nil {
		return fmt.Errorf("project: %w", err) // infra -> retry
	}

	p.log.Debug("processed",
		"id", job.ID, "class", norm.Classification, "state", norm.CanonicalState, "key", norm.EntityKey)
	return nil
}

// project updates the current-state view. Shipments use the atomic rank guard;
// invoices use the row-locked transition table; UNCLASSIFIED has no entity.
func (p *Processor) project(ctx context.Context, n model.Normalized) error {
	switch n.Classification {
	case model.ClassShipment:
		return p.store.ApplyShipment(ctx, n.EntityKey, n.CanonicalState, model.ShipmentRank(n.CanonicalState), n.EventTime)
	case model.ClassInvoice:
		return p.store.ApplyInvoice(ctx, n.EntityKey, n.CanonicalState, n.EventTime, n.AmountMinor, n.Currency)
	default:
		return nil
	}
}
