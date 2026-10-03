# Single-writer ledger with per-account serialization

**Decision.** This system is the sole owner and writer of the ledger; other systems (withdrawals, trades) request debits through its transactional interface. The deposit-state transition, ledger entry, and balance projection commit in one PostgreSQL transaction, serialized per `(account, asset)` via conditional updates or short row locks.

This is not a global lock: different accounts proceed fully in parallel, and user accounts are not hot rows (each is written only by its owner's flows), so per-account serialization costs nothing in the normal case.

Platform-level accounts are the exception: a hot wallet or omnibus/fee account is written by many flows at once, and a row lock would serialize all of them. There we deliberately prefer the optimistic pattern — conditional `UPDATE ... WHERE balance >= amount`, with insufficient balance as a retryable error resolved by top-up — combined with the safety policy of keeping hot-wallet balances low. We accept brief races and retryable failures in exchange for no lock convoy on the hottest row (the same trade-off Chaos made, accepted consciously). Per-account batching remains the fallback if contention is ever measured.

**Rejected alternatives.**

- Application-level locks — useless across replicas.
- A Saga for deposit-update + ledger-write — they share one database; a local transaction is the simpler correct mechanism.
- Append-only entries with an eventually-consistent balance projection — breaks synchronous spendability checks for debits.
