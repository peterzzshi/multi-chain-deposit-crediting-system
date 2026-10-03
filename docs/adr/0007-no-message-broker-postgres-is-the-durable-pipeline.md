# No message broker: PostgreSQL is the durable pipeline (Kafka/Avro deferred)

**Decision.** The system runs without a message broker or schema registry. The scanner is pull-based — it reads blocks at chain pace through the node interface and persists only matched observations; the chain cursor is both the resume position and the backpressure signal. Webhooks are validated and inserted into the append-only `source_events` table, which together with `deposits` forms the durable work queue. Payloads we own are versioned Go structs persisted as `jsonb`, evolved by the same expand-and-contract discipline as the rest of the schema. Implementation stack: Go, ent (ORM; conditional updates and row locks via raw-SQL escape hatches inside `*ent.Tx`, never app-level check-then-act), mockery for mocks of the interfaces we own (node client, custodian client, repositories, ledger writer).

**Why — the requirements argue against a broker:**

- **Volumes don't justify one.** Matched deposit events run at ~2–18/s average (~70–700 writes/s peak, Capacity Estimation) — trivial for PostgreSQL. The 1,750–3,500 tx/s scanning volume is RPC pull processed in bounded in-memory batches; it never becomes bus traffic.
- **Recovery never flows through a broker.** The real recovery sources are the chain (cursor rewind + rescan) and the custodian query API. A broker cannot replay what was never delivered to it, so it would replace neither — it would be redundant infrastructure.
- **The core invariant is simpler without a consumer in between.** Deposit state + ledger entry + balance projection commit in one local transaction (ADR 0003). A broker would force idempotent-consumer/outbox machinery to approximate what the local transaction gives exactly.
- **Ordering is unnecessary by design.** Observations may arrive out of order (webhooks duplicate, delay, reorder); identity and dedup come from chain facts and database constraints (ADR 0001), not delivery order.
- **Fan-out is a single consumer** (the deposit pipeline); reconciliation is a separate poller by design.

**Deferred, not rejected.** Kafka + Avro + a schema registry becomes the natural upgrade when an explicit trigger fires: multiple independent consumers of the observation stream, matched-event rates orders of magnitude higher, multi-stage stream processing, or cross-service event integration. `source_events` is append-only and outbox-ready — CDC can publish it to a broker later without redesigning the core. On adoption, the outbox row is written in the same transaction as the state change to preserve the atomicity invariant. (Go library for that day: `hamba/avro`.)

**Rejected alternative.** Adopting Kafka + Avro now: operational cost (cluster, registry, partitioning, consumer-group management) with no correctness benefit at these volumes, plus a second compatibility surface (message schemas on top of the DB schema) for zero-downtime releases to manage.
