# Assumptions and Scope

Explicit assumptions the design rests on. If the walkthrough challenges one, this is the list to revisit.

## Assumptions (stated, not verified against the prompt)

1. **Node interface** provides blocks with hash + parent hash, transaction receipts with logs, and execution traces (needed for internal native transfers). Per-chain adapter hides the differences; the fast chain is assumed account-based with equivalent per-transfer observability and probabilistic finality (the prompt's reorg behavior rules out relying on a finality gadget).
2. **Address uniqueness**: a deposit address belongs to exactly one `(user, chain, mode)`; enforced by a unique constraint on `(chain, address)`.
3. **Timestamps are never used** for identity or ordering — only block height/hash and log/trace indexes.
4. **Amounts** are integer base units everywhere; decimals live only in `asset_configs` for display.
5. **Webhook delivery** may duplicate, reorder, delay, or silently drop events (prompt-given); the custodian query API is eventually consistent with the custodian's own books.
6. **Custodian does not reliably notify reorgs.** The platform independently re-checks credited-but-unfinalized custodian deposits via targeted on-chain queries until the finality horizon (Q15).
7. **Hot wallet / pool balance** (withdrawal-side) uses optimistic conditional updates with a retryable insufficient-funds error; kept low as a security policy. Atomic guard is the DB conditional update, never app-level check-then-act.

## Out of scope (per prompt)

- Withdrawal flow itself, fund sweeping/consolidation, key management and signing, node selection/operations.

## Out of scope (our choice, noted for the walkthrough)

- **Webhook signature verification / provider authentication** (Q18): assumed in production, excluded from the design discussion to keep focus on crediting correctness.
- **Below-minimum (dust) deposits** (Q16, decided): the minimum is enforced at the UI; the system **skips the credit** but persists the observation in terminal state BELOW_MINIMUM for support visibility (UI enforcement is not a boundary — direct on-chain sends bypass it). No ledger effect, so no correctness risk. Unsupported assets remain silently dropped (Q6).
- Debit flow internals (withdrawal/trade execution); only the ledger's concurrency boundary with them is in scope.
