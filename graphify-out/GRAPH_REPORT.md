# Graph Report - webhook-ingestor  (2026-05-30)

## Corpus Check
- 30 files · ~14,964 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 208 nodes · 288 edges · 19 communities (18 shown, 1 thin omitted)
- Extraction: 88% EXTRACTED · 12% INFERRED · 0% AMBIGUOUS · INFERRED: 35 edges (avg confidence: 0.8)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `09f5cb37`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- [[_COMMUNITY_Community 0|Community 0]]
- [[_COMMUNITY_Community 1|Community 1]]
- [[_COMMUNITY_Community 2|Community 2]]
- [[_COMMUNITY_Community 3|Community 3]]
- [[_COMMUNITY_Community 4|Community 4]]
- [[_COMMUNITY_Community 5|Community 5]]
- [[_COMMUNITY_Community 6|Community 6]]
- [[_COMMUNITY_Community 7|Community 7]]
- [[_COMMUNITY_Community 8|Community 8]]
- [[_COMMUNITY_Community 9|Community 9]]
- [[_COMMUNITY_Community 10|Community 10]]
- [[_COMMUNITY_Community 11|Community 11]]
- [[_COMMUNITY_Community 12|Community 12]]
- [[_COMMUNITY_Community 15|Community 15]]

## God Nodes (most connected - your core abstractions)
1. `Store` - 14 edges
2. `newAPI()` - 8 edges
3. `newApp()` - 7 edges
4. `API` - 7 edges
5. `writeJSON()` - 7 edges
6. `Postgres` - 7 edges
7. `testStore()` - 7 edges
8. `loadConfig()` - 6 edges
9. `getenv()` - 6 edges
10. `fakeQueue` - 6 edges

## Surprising Connections (you probably didn't know these)
- `newApp()` --calls--> `NewPostgres()`  [INFERRED]
  init.go → internal/queue/postgres.go
- `buildLLM()` --calls--> `NewOpenAI()`  [INFERRED]
  init.go → internal/llm/openai.go
- `main()` --calls--> `loadConfig()`  [INFERRED]
  main.go → config.go
- `testStore()` --calls--> `getenv()`  [INFERRED]
  internal/store/integration_test.go → config.go
- `newApp()` --calls--> `NewPool()`  [INFERRED]
  init.go → internal/worker/worker.go

## Communities (19 total, 1 thin omitted)

### Community 0 - "Community 0"
Cohesion: 0.12
Nodes (18): chatMessage, chatRequest, jsonSchema, Ollama, OpenAI, NewOpenAI(), openAIStrictSchema(), retryableStatus() (+10 more)

### Community 1 - "Community 1"
Cohesion: 0.09
Nodes (10): APIError, Client, Retryable(), Response, Result, Processor, Permanentf(), fakeHandler (+2 more)

### Community 2 - "Community 2"
Cohesion: 0.19
Nodes (12): Mock, firstString(), firstTimeString(), invoiceState(), looksInvoice(), looksShipment(), NewMock(), shipmentKey() (+4 more)

### Community 3 - "Community 3"
Cohesion: 0.19
Nodes (11): fakeNotifier, fakeStore, newAPI(), post(), TestGetEntity_CanonicalizesKey(), TestGetEntity_NotFound(), TestGetEvent_InvalidIDIsBadRequest(), TestGetEvent_ValidButMissingIsNotFound() (+3 more)

### Community 4 - "Community 4"
Cohesion: 0.13
Nodes (3): InvoiceTransitionAllowed(), Store, New()

### Community 5 - "Community 5"
Cohesion: 0.21
Nodes (6): API, writeError(), writeJSON(), Notifier, responseRecorder, Store

### Community 6 - "Community 6"
Cohesion: 0.17
Nodes (12): Classification, Entity, IsInvoiceState(), IsShipmentState(), Normalized, RawEvent, RawStatus, CanonicalKey() (+4 more)

### Community 7 - "Community 7"
Cohesion: 0.16
Nodes (7): Job, Permanent, Queue, IsPermanent(), Handler, Pool, NewPool()

### Community 8 - "Community 8"
Cohesion: 0.37
Nodes (12): ShipmentRank(), assertInvoiceState(), mustApplyInvoice(), mustApplyShipment(), mustEntity(), TestApplyInvoice_TransitionTable(), TestApplyShipment_ConcurrentConverges(), TestApplyShipment_NoRegressionThenForward() (+4 more)

### Community 9 - "Community 9"
Cohesion: 0.27
Nodes (6): App, buildLLM(), connectWithRetry(), newApp(), newLogger(), main()

### Community 10 - "Community 10"
Cohesion: 0.22
Nodes (3): Config, Postgres, NewPostgres()

### Community 11 - "Community 11"
Cohesion: 0.62
Nodes (6): Config, getenv(), getenvDuration(), getenvFloat(), getenvInt(), loadConfig()

### Community 12 - "Community 12"
Cohesion: 0.29
Nodes (5): NewOllama(), TestOllama_NormalizeParsesResponse(), ollamaOptions, ollamaRequest, ollamaResponse

## Knowledge Gaps
- **22 isolated node(s):** `Config`, `Response`, `Result`, `Client`, `chatRequest` (+17 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **1 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `newApp()` connect `Community 9` to `Community 10`, `Community 7`?**
  _High betweenness centrality (0.382) - this node is a cross-community bridge._
- **Why does `buildLLM()` connect `Community 9` to `Community 0`, `Community 2`, `Community 12`?**
  _High betweenness centrality (0.320) - this node is a cross-community bridge._
- **Why does `ShipmentRank()` connect `Community 8` to `Community 1`, `Community 6`?**
  _High betweenness centrality (0.314) - this node is a cross-community bridge._
- **Are the 3 inferred relationships involving `newApp()` (e.g. with `NewPostgres()` and `NewPool()`) actually correct?**
  _`newApp()` has 3 INFERRED edges - model-reasoned connections that need verification._
- **What connects `Config`, `Response`, `Result` to the rest of the system?**
  _22 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `Community 0` be split into smaller, more focused modules?**
  _Cohesion score 0.12 - nodes in this community are weakly interconnected._
- **Should `Community 1` be split into smaller, more focused modules?**
  _Cohesion score 0.09 - nodes in this community are weakly interconnected._