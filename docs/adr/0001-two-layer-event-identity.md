# Two-layer event identity

**Decision.** Deduplication uses two distinct identities, never conflated:

- **Source event ID** `(provider, provider_event_id)` — identifies one delivery. Dedups webhook retries and supports the audit trail.
- **Logical transfer ID** `(chain, tx_hash, contract, log_index)`, plus trace index for internal native transfers — identifies the creditable asset movement itself.

The logical transfer ID is derived from stable chain facts and deliberately excludes block hash, so a transaction re-included after a reorg keeps its identity. The ledger credit carries a unique reference to the logical transfer ID: reprocessing a source event or rescanning a block is a no-op for the credit, while canonical block facts can still update.

**Rejected alternatives.**

- Provider transaction ID as deposit identity — breaks in self-built mode and on re-inclusion.
- Client-style idempotency keys — they identify API requests, not chain movements.
