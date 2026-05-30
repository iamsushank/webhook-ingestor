-- 0001_init.sql — webhook ingestion schema.
-- Postgres 13+ : gen_random_uuid() is built-in, no extension required.

-- 1) raw_events — append-only ingestion log.
--    Idempotency (payload_hash) and crash recovery (status, lease_until, attempts) live here.
CREATE TABLE raw_events (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    payload_hash TEXT        NOT NULL UNIQUE,                       -- sha256(raw body) -> exact-duplicate dedup
    raw_payload  JSONB       NOT NULL,                              -- verbatim vendor payload, replayable
    source       TEXT,                                             -- vendor hint (header/path), optional
    status       TEXT        NOT NULL DEFAULT 'PENDING'
                 CHECK (status IN ('PENDING', 'PROCESSING', 'PROCESSED', 'FAILED')),
    attempts     INT         NOT NULL DEFAULT 0,                   -- retry counter; caps at MAX_ATTEMPTS -> DLQ
    error        TEXT,                                             -- last failure reason
    lease_until  TIMESTAMPTZ,                                      -- visibility timeout; reaper reclaims if past
    received_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP    -- our clock (ops/SLA), NOT lifecycle ordering
);

-- queue scan: claimable pending rows, oldest first. Partial index = small and hot.
CREATE INDEX idx_raw_events_pending ON raw_events (received_at) WHERE status = 'PENDING';
-- reaper scan: in-flight rows whose lease expired (worker crash).
CREATE INDEX idx_raw_events_lease   ON raw_events (lease_until) WHERE status = 'PROCESSING';

-- 2) normalized_events — immutable LLM facts, one per processed raw event. Audit + history.
CREATE TABLE normalized_events (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    raw_event_id      UUID        NOT NULL UNIQUE REFERENCES raw_events (id),  -- worker idempotency: 1 fact / raw
    classification    TEXT        NOT NULL
                      CHECK (classification IN ('SHIPMENT', 'INVOICE', 'UNCLASSIFIED')),
    entity_key        TEXT,                                        -- correlation key; NULL when UNCLASSIFIED
    canonical_state   TEXT,                                        -- PICKED_UP.. / ISSUED..; NULL when UNCLASSIFIED
    event_time        TIMESTAMPTZ,                                 -- vendor event time -> UTC; ordering tiebreak
    amount_minor      BIGINT,                                      -- money as integer minor units (never float)
    currency          CHAR(3),                                     -- ISO-4217
    vendor_state_text TEXT,                                        -- raw milestone words, for audit
    confidence        REAL,                                        -- LLM self-reported; < threshold -> UNCLASSIFIED
    llm_model         TEXT,                                        -- which model produced this fact
    prompt_version    TEXT,                                        -- which prompt produced this fact
    created_at        TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_normalized_entity ON normalized_events (entity_key);

-- 3) entities — current-state projection, one row per shipment/invoice. The guard lives here.
CREATE TABLE entities (
    entity_key      TEXT        PRIMARY KEY,                       -- business id (BL number / invoice doc ref)
    type            TEXT        NOT NULL CHECK (type IN ('SHIPMENT', 'INVOICE')),
    current_state   TEXT        NOT NULL,
    current_rank    INT         NOT NULL,                          -- monotonic guard for linear (shipment) lifecycle
    last_event_time TIMESTAMPTZ,                                   -- event_time of the winning state
    amount_minor    BIGINT,                                        -- latest known amount (invoice)
    currency        CHAR(3),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
