# Shared core deposit state machine for both crediting modes

**Decision.** Both modes use one deposit lifecycle state machine — `PENDING → CREDITED → FINALIZED`, with `REORGED` (recoverable), `DROPPED` (terminal, never credited), `REVERSED` (terminal for that credit cycle; re-inclusion starts a new cycle at `PENDING`), and `BELOW_MINIMUM` (terminal, under the configured minimum, no ledger effect) — fed by two observation sources behind a common ingest interface: the self-built scanner and custodian webhooks/query API.

Reorg semantics, idempotent crediting, and reversal handling are properties of chain facts and the ledger, not of who detected the deposit; duplicating the machine per mode would duplicate the correctness core. Mode-specific concerns live outside the machine: provider delays, missed webhooks, and book discrepancies are handled by reconciliation jobs and hold flags, and scanner cursor management is an ingest detail.

**Rejected alternative.** One state machine per mode — superficially cleaner separation, but every invariant (no duplicate credit, no missed credit, no credit surviving a reorg) would need to be implemented, tested, and monitored twice.
