# Design trade-offs

This page records the high-level decisions that required an explicit trade-off. The linked ADR or policy document is the canonical explanation; this page serves as a quick index.

| Choice                                               | Why                                                                                                                      | Canonical source                                                                                     |
|------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------|
| Verify custodian claims against chain facts          | Provider notifications are hints, not credit authority.                                                                  | [ADR 0004](adr/0004-chain-facts-are-source-of-truth-in-custodian-mode.md)                            |
| One state machine for both modes                     | Reorg, idempotency, and reversal invariants should be implemented once.                                                  | [state-machine.md](state-machine.md), [ADR 0005](adr/0005-shared-core-deposit-state-machine.md)      |
| Confirmation depth plus an observation horizon       | Depth handles ordinary reorgs; the horizon bounds monitoring cost and leaves explicit residual risk.                     | [risk-policy.md](risk-policy.md), [ADR 0002](adr/0002-reversal-after-credit-and-finality-horizon.md) |
| Exposure holds spendability, not ledger credit       | Chain facts remain reflected in the ledger while aggregate reorg exposure is controlled.                                 | [risk-policy.md](risk-policy.md), [ADR 0006](adr/0006-risk-policy-layering.md)                       |
| PostgreSQL as the durable pipeline; no broker for v1 | Constraints, transactions, and source-of-truth recovery are more valuable than broker complexity at the estimated scale. | [ADR 0007](adr/0007-no-message-broker-postgres-is-the-durable-pipeline.md)                           |
| Per-account ledger serialization                     | A single transaction keeps the entry and balance projection consistent under concurrent credits and debits.              | [ADR 0003](adr/0003-single-writer-ledger-per-account-serialization.md)                               |

## Remaining uncertainty

Capacity inputs, observed reorg distributions, fast-chain transfer
observability, and exposure-cap values are illustrative until production data is
available. The implementation therefore keeps chain-specific adapters,
measures the critical paths, and applies an empirical floor to reorg policy.

## If conditions change

- A chain with deterministic finality can finalize at the chain signal.
- A sustained order-of-magnitude volume increase justifies an outbox and
  broker, plus partitioning of high-volume tables.
- Provider-signed reorg notifications can reduce polling, but do not replace
  canonical verification.
- More addresses or chains require adapter/index scaling, not a second state
  machine.

## Verification approach

Use the fault-injection harness to vary reorg depth and provider delivery
ordering, duplication, delay, and loss. Assert unique credits, reconciliation
completeness, and reversal behavior. Run PostgreSQL integration tests for
constraints and locking, then benchmark the credit and replay paths against the
capacity estimates in [technical-decisions.md](../technical-decisions.md).
