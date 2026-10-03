# Deposit Crediting

The deposit-crediting context of a crypto-asset platform: it becomes aware of user deposits on multiple chains (by scanning itself or via a custodian) and credits the internal user ledger correctly.

## Language

### Modes and routing

**Crediting mode**:
How a deposit is detected and where its assets are held. Exactly two modes exist: self-built (platform-generated addresses, platform scans the chain) and custodian (custodian-generated addresses, custodian notifies via webhook). Selected per asset, not per deposit.
_Avoid_: custody type, integration mode

### Chain concepts

**Blockchain transaction**:
A signed protocol operation identified by a transaction hash. One transaction may cause several creditable asset movements.
_Avoid_: tx (in prose), transfer

**Transfer**:
One creditable asset movement. A single blockchain transaction can contain several transfers (e.g., multiple token logs, or a native movement plus token movements).
_Avoid_: transaction, deposit (for this meaning)

**Chain observation**:
What the scanner learns from a block, receipt, trace, or log, including block height/hash and canonicality. An observation is immutable once persisted; later observations may supersede its canonicality, never edit it.

**Reorg**:
A replacement of previously canonical blocks by a competing branch. Can invalidate already-observed transfers, including after credit.
_Avoid_: rollback, fork

**Confirmation depth**:
The number of canonical blocks on top of a transfer's block required before crediting. A risk policy, not a proof of finality.
_Avoid_: finality, confirmations (as a count alone)

**Finality horizon**:
A second, much deeper threshold after confirmation depth. Until it passes, a credited deposit can still be reversed by a deep reorg; once it passes, the deposit is treated as irreversible and the system stops watching it. This encodes the explicit assumption that reorg protection is best-effort, never a guarantee.
_Avoid_: finality (no deterministic finality is assumed on either chain)

**Chain cursor**:
The durable per-chain scan position (at least height and block hash, ideally parent hash) used for restart and reorg detection. Not a finality marker.

### Provider concepts

**Provider event**:
A custodian webhook delivery or query-API result. An external observation envelope, not necessarily one-to-one with a blockchain transaction.
_Avoid_: webhook (as a synonym for the event itself), notification

**Crediting event**:
A custodian event meaning "a deposit was detected and attributed to a platform account." It opens a deposit in the platform's pipeline; it is never by itself a reason to credit. The platform verifies the underlying chain facts and applies its own confirmation policy.
_Avoid_: deposit notification (ambiguous about who detected)

**Vault balance-change event**:
A custodian event meaning the custodian's own books moved. It is aggregate and unattributed — it says the vault balance changed, not which user caused it — so it must never mutate a user's ledger entry directly. Its only role is solvency reconciliation: the platform's per-asset ledger total for custodied assets should match the custodian-reported vault balance, with discrepancies raising alerts.
_Avoid_: settlement event

### Identity

**Source event ID**:
Identifies one provider delivery, e.g. `(provider, provider_event_id)`. Deduplicates retries and supports the audit trail. Never identifies the deposit itself.

**Logical transfer ID**:
Identifies one creditable asset movement from stable chain facts, e.g. `(chain, tx_hash, token_contract, log_index)` for tokens; native internal transfers additionally need a trace index. Excludes block hash, because a transaction can be re-included after a reorg.
_Avoid_: deposit ID (ambiguous), idempotency key (that is a client-API concept)

### Ledger

**Ledger entry**:
An append-only credit, debit, or reversal in integer base units, with a unique source reference. History is never edited or deleted.

**Reversal**:
A compensating ledger entry linked to an original credit, recorded when a credited transfer leaves the canonical chain. Not an on-chain refund.
_Avoid_: refund, rollback, deletion

### Deposit lifecycle (one shared machine for both modes — ADR 0005)

**Deposit**:
A logical transfer plus its crediting lifecycle state. One row per logical transfer ID, regardless of which mode observed it.

**PENDING**:
Observed on a candidate chain, below confirmation depth. No ledger effect yet.

**CREDITED**:
Confirmation depth reached; ledger credit and deposit state committed atomically. Non-terminal: a deeper reorg can still reverse it until the finality horizon passes.

**FINALIZED**:
Finality horizon passed. Terminal. The system stops watching the deposit; residual deep-reorg risk is accepted by policy and bounded by the exposure cap.

**REORGED**:
The transfer's inclusion is no longer canonical. An observation state, not a money movement by itself: re-inclusion returns the deposit to PENDING.

**DROPPED**:
The transfer was never credited and did not re-appear within the reorg window. Terminal.

**BELOW_MINIMUM**:
A supported-asset transfer under the configured minimum. Observed and persisted, but never credited and no ledger effect. Terminal. Exists because the UI minimum is not a real enforcement boundary — users can always send directly on-chain — so support needs a record of what arrived.
_Avoid_: dust (describes the amount, not the state)

**REVERSED**:
The transfer was credited, then reorged out; a compensating ledger entry undid the credit. Terminal for that credit cycle; if the same logical transfer is later re-included, it re-enters at PENDING as a new credit cycle.

**Exposure cap**:
The policy bound on total unfinalized, spendable credited value: `sum <= min(C_attack / k, business risk budget)`. The only attack-economics quantity fully under the platform's control.

**Available balance**:
The portion of an account's ledger balance that debits may spend. Distinct from the deposit state machine: a deposit may be credited (ledger credit exists) while a hold policy keeps part of it non-spendable. Confirmation/canonicality and spendability are separate axes and must not be merged into one state.
_Avoid_: spendable (as a deposit state), settled (ambiguous)

**Reconciliation**:
The periodic comparison of an external source of truth against internal records: custodian query-API results and vault balance-change events against deposits and the ledger, and targeted on-chain queries against claimed transactions. It is the completeness backstop for missed webhooks and the validity check for custodian claims — not a credit trigger.
_Avoid_: sync, audit (audit is the trail; reconciliation is the comparison)
