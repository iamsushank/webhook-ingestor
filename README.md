# Webhook Ingestion Service

Ingests arbitrary vendor webhooks (logistics & finance), uses an LLM to classify and normalize each payload into a strict internal schema, and persists it — built for a production reality where vendors expect **sub-second acks**, **resend identical payloads**, and **deliver events out of order**.

---

## Quickstart

```bash
# 1. (optional) configure — copy the env file. Leave it as-is to run
#    with the deterministic mock client (no network, no cost).
cp .env.example .env

# 2. run — Postgres + API + worker (migrations run on boot), one command
docker compose up --build              # API on http://localhost:8080

# 3. send a webhook
curl -sX POST localhost:8080/webhooks -H 'Content-Type: application/json' -d '{
  "carrier_scac":"MAEU","event_msg_id":"MAEU-EVT-2026-04-22-0001",
  "transport_doc":{"type":"MBL","number":"MAEU240498712"},
  "container":"MSKU7748112","milestone":"Loaded onboard and sailed",
  "milestone_at":"2026-04-21T22:47:00+08:00"}'
# → 202 Accepted  {"id":"<uuid>","status":"accepted","duplicate":false}

# 4. read the normalized projection (after async processing)
curl -s localhost:8080/entities/MAEU240498712
# → {"entity_key":"MAEU240498712","type":"SHIPMENT","current_state":"IN_TRANSIT", ...}
```

| Endpoint | Purpose |
|----------|---------|
| `POST /webhooks` | Ingest any JSON. Returns `202` immediately. |
| `GET /events/{id}` | Raw event + processing status (ops/debug). |
| `GET /entities/{key}` | Current normalized state of a shipment/invoice. |
| `GET /healthz` | Liveness + DB reachability. |

> **Providers**: by default it runs the **mock** (no key; money fields left `null`). For a real model set `LLM_PROVIDER` — `openai` (needs `LLM_API_KEY`) or `ollama` (local, free: `LLM_PROVIDER=ollama LLM_MODEL=llama3.2`). Both populate `amount_minor`/`currency`.

### Config

All values are environment-driven (12-factor), sourced from `.env` via docker-compose, with in-code defaults as fallback. See `.env.example` for the full list.

| Env | Default | Meaning |
|-----|---------|---------|
| `MODE` | `all` | process role: `server` (HTTP only) \| `worker` (consumer only) \| `all` |
| `LLM_PROVIDER` | _(auto)_ | `openai` \| `ollama` \| `mock`; auto = OpenAI if `LLM_API_KEY` set, else mock |
| `LLM_MODEL` | provider default | model id — `gpt-4o-mini`, `llama3.2`, `gemma4:latest`, … |
| `LLM_API_KEY` | — | required for `openai`/Groq; unused for `ollama`/`mock` |
| `LLM_BASE_URL` | provider default | endpoint — an OpenAI-compatible URL, or the Ollama host |
| `DATABASE_URL` | set by compose | Postgres DSN |
| `LOG_LEVEL` | `info` | `debug` also logs each request + response (method, path, status, bodies) |
| _tuning_ | — | `CONFIDENCE_THRESHOLD`, `WORKER_CONCURRENCY`, `MAX_ATTEMPTS`, `LEASE_TIMEOUT`, `BASE/MAX_BACKOFF`, `POLL_INTERVAL` — see `.env.example` |

---

## Architecture

The central tension: vendors want a **sub-second ack**, but an LLM call takes 1–5s — so ingestion is split from processing, and the rest follows from that plus the three stated realities (duplicates, out-of-order, failure).

**Key decisions:**

- **The database is the queue** — `raw_events` claimed with `FOR UPDATE SKIP LOCKED` under a lease, so claim + retry + crash-recovery are one mechanism with no broker to operate. Behind a `Queue` interface for a later SQS/Kafka swap.
- **Three storage layers** — append-only raw log (idempotency) → immutable LLM facts (audit) → a guarded current-state projection (the entity).
- **Two keys, two jobs** — a content hash dedupes *messages*; an LLM-extracted business key correlates *entities* across webhooks.
- **State is a convergent projection** — out-of-order and concurrent updates both collapse to the max-rank state; a late event is recorded but never regresses it.
- **Provider-agnostic LLM** — OpenAI / Ollama / Mock behind one `Client` interface; the prompt + JSON schema are a single embedded contract.

```
            FAST PATH  (sub-second, no LLM)
 vendor ─POST /webhooks─▶ ingest
                           │ 1. sha256(body) → dedup on raw_events.payload_hash (UNIQUE)
                           │ 2. INSERT raw_events (status=PENDING)   ← the row IS the queue
                           │ 3. signal a worker
                           └─▶ 202 Accepted

            SLOW PATH  (async worker pool)
 worker ─Claim w/ lease─▶ raw_events (PENDING or expired-lease)   [FOR UPDATE SKIP LOCKED]
        │ 1. LLM: classify + normalize + extract entity_key  (structured JSON)
        │ 2. validate against strict schema  (reject bad enums / missing keys)
        │ 3. INSERT normalized_events  (raw_event_id UNIQUE → idempotent)
        │ 4. apply to entities projection  (rank guard / transition table)
        └─▶ Ack: raw_events → PROCESSED   (or Retry w/ backoff → DLQ after MAX_ATTEMPTS)
```

**Boundaries (the swappable seams).** The consumer and pipeline depend on interfaces, not implementations:

- `queue.Queue` — the work source (`Claim`/`Ack`/`Retry`/`Fail`). Backed by `queue.Postgres` today; an SQS/Kafka impl drops in without touching the consumer or pipeline.
- `llm.Client` — model access (`Normalize(ctx, raw) (Result, error)`). Three impls — **OpenAI**, **Ollama** (local), **Mock** — chosen by `LLM_PROVIDER`. No lock-in.
- `worker.Pool` is a generic consumer; `processor.Processor` is the domain pipeline. Neither knows about the other's concerns.

**Process roles.** The same binary runs as `server` (HTTP ingest), `worker` (queue consumer), or `all` (both — default), selected by `MODE`. Because the roles share only the database, compose runs them as **separate containers** (`api` + `worker`) that scale independently: `docker compose up -d --scale worker=3`. Migrations are guarded by a Postgres advisory lock, so any number of containers can boot together safely.

**Three storage layers, three jobs:**

| Table | Job |
|-------|-----|
| `raw_events` | append-only ingestion log → idempotency (`payload_hash`) + crash recovery (`status`, `lease_until`, `attempts`) |
| `normalized_events` | immutable LLM facts → full history + audit trail |
| `entities` | current-state projection → the out-of-order & concurrency guard lives here |

**Layout**

```
main.go                  thin entrypoint: load config, build app, run the role
config.go, init.go       env config + dependency wiring (newApp) + run modes (server/worker/all)
internal/ingest/         HTTP fast path (hash, dedup, 202) + status/projection reads
internal/queue/          Queue interface + Postgres impl (FOR UPDATE SKIP LOCKED, lease, backoff)
internal/processor/      domain pipeline: LLM → normalize → persist fact → project
internal/worker/         generic consumer pool (Claim → Handle → Ack/Retry/Fail)
internal/llm/            Client interface + OpenAI, Ollama, and Mock impls
internal/llm/spec/       shared LLM contract: system prompt + JSON schema (embedded files)
internal/samples/        appendix vendor payloads as JSON fixtures (tests)
internal/normalize/      validation gate, state machines, entity-key canonicalization
internal/store/          Postgres repositories
migrations/              SQL schema (embedded, run on boot)
docker-compose.yml, Dockerfile
```

---

## How it handles the realities

One mechanism per distributed-systems problem.

### 1. Sub-second ack
The fast path does no LLM work: hash → store raw → signal → `202 Accepted`. `202` (not `200`) is honest: *received, not yet processed*. Processing outcome is queryable separately via `GET /events/{id}` — it never leaks into the webhook ack. If the DB write fails we return `5xx` so the vendor retries (correct backpressure). The **only** `4xx` here is a non-JSON body; a downstream LLM failure is *our* problem and never returns 4xx to the vendor.

### 2. Duplicate payloads (vendors resend the exact same body)
Idempotency key = `sha256(raw body)`, enforced by a `UNIQUE` constraint on `raw_events.payload_hash`. A duplicate insert conflicts → we skip enqueue, skip the LLM, and return the **same `202`**. Identical request → identical response, every time.

> Side benefit: because an exact duplicate never reaches the LLM twice, **message-level idempotency bounds LLM non-determinism to one shot per unique payload**.

### 3. Out-of-order events (a Day-1 `PICKED_UP` can arrive after `DELIVERED`)
Every webhook is stored as an immutable fact in `normalized_events`. `entities.current_state` is a **guarded projection** over those facts — it only ever moves forward, so a late event is recorded but never regresses state.

- **Ordering authority = canonical-state rank, not the clock.** `PICKED_UP(1) < IN_TRANSIT(2) < OUT_FOR_DELIVERY(3) < DELIVERED(4)`. Vendor clocks are unreliable; lifecycle position is not.
- **`event_time`** (the vendor's event timestamp, normalized to UTC) is secondary — it orders history and breaks ties. Distinct from **`received_at`** (our clock, ops only). A webhook stuck in a retry queue for days has a late `received_at` but its real `event_time` is Day 1.
- Shipment guard is a single atomic statement:
  ```sql
  INSERT INTO entities (...) VALUES (...)
  ON CONFLICT (entity_key) DO UPDATE SET ...
  WHERE entities.current_rank < EXCLUDED.current_rank;   -- 0 rows = a later state already won
  ```

**Entity correlation** — knowing two different webhooks belong to one shipment — uses a separate **correlation key**, not the hash (different payloads have different hashes). The business identifier sits at no fixed path across undocumented vendors, so the LLM extracts it (shipment → transport/BL doc number; invoice → invoice doc ref) and the app canonicalizes the casing.

### 4. Concurrency (N workers, two events for the same entity at once)
The projection update is **convergent**: with the rank guard, any interleaving of workers collapses to "keep the max-rank state" — final state is identical regardless of processing order. The read-check-write is atomic via the conditional `UPDATE` above (compare-and-set), or `SELECT FOR UPDATE` for the non-linear invoice path. The LLM call runs **before** the transaction — we never hold a DB lock across a multi-second LLM call.

### 5. Failure & recovery
- **Transient errors** (LLM timeout, 429, 5xx, DB hiccup) → retry with **exponential backoff + jitter**, capped at `MAX_ATTEMPTS`.
- **Permanent errors** → **dead-lettered immediately**, no wasted retries: a deterministic validation failure (temp 0), or a provider error that won't recover on retry. The OpenAI client reclassifies `insufficient_quota` (a 429) and `invalid_api_key` (a 401) from "transient" to permanent, so an unfunded account or bad key DLQs on the first attempt instead of burning the retry budget. Low confidence is downgraded to `UNCLASSIFIED` (a valid outcome, not a failure).
- **Worker crash** → the job holds a **lease** (`lease_until`); claims re-pick rows where `status='PROCESSING' AND lease_until < now()`, incrementing `attempts` (so a payload that *crashes* the worker also eventually caps out, not just LLM-error poisons).
- **Retries exhausted** → `status='FAILED'` (DLQ) + a logged warning (the alert/metric hook). Raw payload is retained → fully replayable.

> Formally: **at-least-once processing** (retry + lease recovery) made safe by **idempotent writes** (dedup hash + `raw_event_id UNIQUE` + the convergent projection guard) = **effectively-once**.

---

## State machines

```
Shipment (linear):  PICKED_UP → IN_TRANSIT → OUT_FOR_DELIVERY → DELIVERED
Invoice  (branching):
        ISSUED ── PAID ── REFUNDED        (refund reverses a settlement)
           └──── VOIDED                   (cancel before payment)
```

Shipment is a total order → a single `rank <` comparison is the guard. Invoice branches into two terminals from **different** predecessors (`VOIDED` only from `ISSUED`, `REFUNDED` only from `PAID`), so rank alone can't tell legal from illegal — it uses an explicit predecessor-aware transition table under a row lock.

Vendor language is collapsed to these canonical states by the LLM (`"Loaded onboard and sailed"` → `IN_TRANSIT`, `"settled in full"` → `PAID`, `"freight invoice raised"` → `ISSUED`).

---

## LLM strategy

- **Three providers, one interface**: `llm.Client` has three implementations — **OpenAI** (and any OpenAI-compatible endpoint via `LLM_BASE_URL`, e.g. Groq), **Ollama** (local, free, offline), and **Mock** (deterministic, no network). `LLM_PROVIDER` selects one; nothing downstream changes.
- **One shared contract**: the system prompt and the response JSON schema live as embedded files in `internal/llm/spec` (`system_prompt.txt`, `normalize.json`) — a single source of truth every provider reads, so adding a provider never duplicates them.
- **Schema-constrained output**: OpenAI uses `response_format: json_schema, strict: true`; Ollama feeds the same schema to its `format` for constrained decoding; both at `temperature: 0` — the model can't emit prose or an unknown enum. (Non-strict OpenAI-compatible endpoints fall back to `json_object`.)
- **Validation gate**: the response is validated in Go (enum ∈ canonical set, `entity_key` present for SHIPMENT/INVOICE, `currency` ISO-4217, `event_time` parseable) **before** any DB write. A structural failure is **permanent** (deterministic at temp 0) → dead-lettered immediately, never retried; low confidence → downgraded to `UNCLASSIFIED`. Either way the LLM can't poison the store.
- **Fallback**: classification is `SHIPMENT | INVOICE | UNCLASSIFIED`; anything not clearly a parcel-movement or financial document (e.g. a port-congestion advisory) → `UNCLASSIFIED`, stored, no entity created.
- **Auditability**: every fact keeps `vendor_state_text` (raw words), `confidence`, `llm_model`, and `prompt_version`, so a bad mapping ("vendor said X → LLM chose Y") is traceable and the prompt is versioned.

### Internal schema (LLM output contract)

```json
{
  "classification":   "SHIPMENT | INVOICE | UNCLASSIFIED",
  "entity_key":       "stable business id, or null",
  "canonical_state":  "PICKED_UP|IN_TRANSIT|OUT_FOR_DELIVERY|DELIVERED|ISSUED|PAID|VOIDED|REFUNDED|null",
  "event_time":       "RFC3339 UTC, or null",
  "amount_minor":     "integer minor units (e.g. 2435075 = €24,350.75), or null",
  "currency":         "ISO-4217, or null",
  "vendor_state_text":"original milestone text",
  "confidence":       0.0
}
```

Money is stored as **integer minor units + currency code** (never float, never FX-converted at ingestion — `"EUR 24.350,75"` → `amount_minor=2435075, currency=EUR`).

---

## Database schema

```sql
CREATE TABLE raw_events (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  payload_hash TEXT NOT NULL UNIQUE,                            -- idempotency
  raw_payload  JSONB NOT NULL,
  source       TEXT,
  status       TEXT NOT NULL DEFAULT 'PENDING'
               CHECK (status IN ('PENDING','PROCESSING','PROCESSED','FAILED')),
  attempts     INT  NOT NULL DEFAULT 0,
  error        TEXT,
  lease_until  TIMESTAMPTZ,                                     -- crash recovery
  received_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- partial indexes keep the queue/reaper scans small even with millions of PROCESSED rows
CREATE INDEX idx_raw_events_pending ON raw_events (received_at) WHERE status = 'PENDING';
CREATE INDEX idx_raw_events_lease   ON raw_events (lease_until) WHERE status = 'PROCESSING';

CREATE TABLE normalized_events (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  raw_event_id    UUID NOT NULL UNIQUE REFERENCES raw_events(id),  -- worker idempotency
  classification  TEXT NOT NULL CHECK (classification IN ('SHIPMENT','INVOICE','UNCLASSIFIED')),
  entity_key      TEXT,
  canonical_state TEXT,
  event_time      TIMESTAMPTZ,
  amount_minor    BIGINT,
  currency        CHAR(3),
  vendor_state_text TEXT,
  confidence      REAL,
  llm_model       TEXT,
  prompt_version  TEXT,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_normalized_entity ON normalized_events (entity_key);

CREATE TABLE entities (
  entity_key      TEXT PRIMARY KEY,                               -- natural business key
  type            TEXT NOT NULL CHECK (type IN ('SHIPMENT','INVOICE')),
  current_state   TEXT NOT NULL,
  current_rank    INT  NOT NULL,                                  -- shipment monotonic guard
  last_event_time TIMESTAMPTZ,
  amount_minor    BIGINT,
  currency        CHAR(3),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

---

## Trade-offs (made for the 3-hour budget)

| Chose | Instead of | Why / mitigation |
|-------|-----------|------------------|
| DB-backed queue — `raw_events` consumed via `FOR UPDATE SKIP LOCKED` under a lease, behind a `Queue` interface | a dedicated broker (Kafka/SQS) | No extra infra; the table is already the durable source of truth, so claim + crash-recovery + backoff are one mechanism. Swapping is a new `Queue` impl — consumer/pipeline untouched. SQS maps cleanly (visibility timeout = lease, native DLQ); Kafka needs retry-topic + DLQ-topic adapter (offset model has no per-message ack). |
| Content-hash idempotency (byte-exact) | semantic / vendor-event-id dedup | Matches the stated "exact same payload" reality. Misses re-serialized duplicates → roadmap. |
| Single LLM call (classify + normalize) | two-stage (cheap classifier → normalizer) | Fewer moving parts; two-stage is a cost/accuracy optimization → roadmap. |
| Model produces `amount_minor`/`event_time` (UTC) directly | deterministic Go locale/FX parser | Prompted with examples; Go validates ranges/format. Deterministic parser → roadmap. Currency is stored as-is, never FX-converted (lossy, time-sensitive). |
| DLQ = `FAILED` status + logged warning | pager / managed DLQ | Enough to prove the pattern; alerting integration → roadmap. |
| No signature/auth on the endpoint | per-vendor HMAC verification | Out of scope for the exercise → roadmap (first item). |
| Focused tests + DB integration tests | full coverage | Time-boxed; cover the highest-risk invariants. |

---

## Production roadmap

1. **Security**: per-vendor webhook signature (HMAC) verification + authn before ingest.
2. **Durable broker**: the `Queue` interface is already the seam — add an SQS or Kafka impl for multi-instance, cross-host at-least-once. SQS maps directly (visibility timeout = lease); Kafka partitions by `entity_key` for per-entity ordering, with retry/DLQ topics bridging the offset model.
3. **DLQ tooling**: alerting on DLQ depth, a replay endpoint, poison-message inspection.
4. **Entity resolution**: deterministic identifier rules (MBL vs HBL vs container multiplicity) to harden correlation beyond the LLM's best guess.
5. **LLM quality**: golden-dataset eval harness + regression on known payloads, prompt versioning/A-B, two-stage model routing for cost.
6. **Observability**: structured logs + opt-in request/response debug logging (done), metrics (ingest latency, queue depth, LLM latency/cost, classification mix), distributed tracing.
7. **Scale**: independent `server`/`worker` containers (done — `--scale worker=N`, advisory-locked migrations); next, autoscale workers on queue depth and add read replicas.
8. **Data**: semantic dedup, multi-currency FX + amount-due, deterministic money parsing, schema migrations in CI/CD.
9. **Credential refresh**: a `401` is permanent today (assumes an invalid key). When the provider uses **expiring tokens / OAuth**, detect the expired-token `401`, refresh or rotate the credential, then retry once before dead-lettering — so a merely-expired token never DLQs otherwise-valid events.

---

## Testing

```bash
go test ./...                          # unit tests (no DB needed — store integration tests skip)

# include the store integration tests (idempotency, out-of-order, concurrency, transitions):
TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5643/webhooks?sslmode=disable go test ./...
```

Covered: mock classification of all sample payloads, the validation gate (enum/key/money/timezone/confidence), retry backoff bounds, consumer ack/retry/DLQ routing, HTTP handlers (202/duplicate/400/404/key-canonicalization), and DB-level data integrity — idempotent insert, out-of-order no-regression, concurrent convergence, invoice transition legality, and fact idempotency.
