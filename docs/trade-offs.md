# Design Trade-offs

The written-explanation deliverable: what was traded against what, and why. Each section condenses an ADR; follow the links for the full reasoning. Capacity math: [../technical-decisions.md](../technical-decisions.md) §Capacity Estimation.

## 1. Verification over trust in custodian mode (ADR 0004)

Custodian webhooks are treated as low-latency hints, never as a basis for crediting: they may be duplicated, reordered, delayed by minutes, or never arrive. The credit decision rests on chain facts the platform verifies itself, with attribution through our own address table.

- **Cost:** crediting latency includes verification plus confirmation depth; we run targeted on-chain queries for custodied assets.
- **Benefit:** the custodian's notification pipeline cannot cause missed or phantom credits; a single correctness core covers both modes.

## 2. Risk policy as layers, not guarantees (ADRs 0002, 0006)

No deterministic finality is assumed on either chain. Protection is layered: **confirmation depth** (amount-tiered) handles natural reorgs; a **finality horizon** bounds how long credited deposits are watched before becoming terminal; an **exposure cap** bounds total unfinalized spendable value economically.

- Typical deposits credit with **no artificial delay** — depth only. Large deposits (one tier boundary, illustrative $50k) additionally wait for spendability until finalized.
- **Rejected:** an attack-cost equation as the credit trigger. An attacker's payoff aggregates across all victims, and the parameters (staked value, token price, rental markets) are unmeasurable in real time — so economics bounds exposure, it does not gate individual credits.
- **Accepted honestly:** past the horizon, residual deep-reorg risk is a business risk, bounded by the cap. If a reversal hits funds already spent, balances can go negative; the account is flagged and debits blocked. Detection and recording are the system's job; insurance/collection are business concerns.

## 3. One state machine, two ingest sources (ADR 0005)

Both modes feed one deposit lifecycle (`PENDING → CREDITED → FINALIZED`, plus `REORGED` / `DROPPED` / `REVERSED` / `BELOW_MINIMUM`). Reorg semantics, idempotent crediting, and reversal handling are properties of chain facts and the ledger, not of who detected the deposit. Mode-specific messiness — provider delays, missed webhooks, vault discrepancies — is pushed to reconciliation jobs at the edges. A per-mode machine would implement, test, and monitor every invariant twice.

## 4. Identity discipline makes replay safe (ADR 0001)

Two identity layers: a **source event ID** dedups deliveries; a **logical transfer ID** (stable chain facts, excluding block hash) identifies the creditable movement. The ledger credit references the latter uniquely, so reprocessing any event or rescanning any block is a no-op. This is what makes reorg rewind/replay and restart-replay cheap and boring instead of dangerous.

## 5. Ledger integrity under concurrency (ADR 0003)

The system is the sole writer of the ledger; deposit state, ledger entry, and balance projection commit in **one PostgreSQL transaction**, serialized per `(account, asset)`. Different accounts run fully in parallel; user accounts are not hot rows. For platform hot accounts (hot wallet, omnibus/fee) we consciously accept the optimistic pattern — conditional `UPDATE ... WHERE balance >= amount`, retryable insufficient-balance errors, low balances by policy — over a lock convoy on the hottest row.

- **Rejected:** application-level locks (useless across replicas), a Saga inside one database (a local transaction is the simpler correct mechanism), eventually-consistent balances (breaks synchronous spendability checks).

## 6. Boring infrastructure: no broker (ADR 0007)

The pipeline is PostgreSQL end-to-end: scanner and webhook handlers write directly; recovery flows from sources of truth (chain rescan, custodian query API), which a broker could never provide anyway — it can only replay what was already accepted. At ~70–700 matched writes/s peak, a broker adds operational weight and exactly-once fiction without buying correctness, decoupling we need, or throughput. Kafka + Avro + registry is the documented upgrade path, with explicit trigger conditions and an outbox-ready `source_events` table.

## 7. PostgreSQL + ent + mockery, deliberately

One well-tuned relational store over specialized components: the workload is relational (identity constraints, per-account serialization, projections), and the tables, indexes, and retention plan are sized in the capacity estimate. ent gives schema-as-code with generated types; its gaps for this design (conditional balance updates, `FOR UPDATE`) are handled through raw-SQL escape hatches inside the same transaction — never app-level check-then-act. mockery mocks the interfaces we own (node client, custodian client, repositories), which keeps the fault-injection harness cheap to build.

---

## What I am least certain about

- **The capacity assumptions.** Match rate (0.1% base / 1% stress), payload sizes, and peak multipliers are illustrative. The design mitigates by sizing headroom and stating assumptions explicitly rather than trusting point numbers.
- **Fast-chain observability.** Assumed account-based with per-transfer observability equivalent to EVM logs/traces. If internal native transfers are not observable there, the logical transfer ID scheme needs a chain-specific variant behind the adapter.
- **Exposure-cap inputs.** `C_attack` parameters are unmeasurable in real time; in practice the cap resolves to the business risk budget. The monitor-and-hold mechanism is sound; the number is a policy input.
- **Reorg-depth parameters** (`p1`, `r`, deepest reorg seen) until measured against real chain data — hence the empirical floor on top of the model.

## What would have to change if the conditions change

| Condition change | Design impact |
|---|---|
| Chain with a real BFT finality signal | Credit at finality; attack-cost models collapse to monitoring (ADR 0006). State machine unchanged — `FINALIZED` arrives earlier |
| 10–100x deposit volume | Broker + outbox becomes worth it (ADR 0007 triggers); partition `source_events`/`deposits`; benchmark the credit path beyond ~700 writes/s |
| Custodian sends signed, attributed reorg notifications | Re-checker gets lighter but stays — trust, then verify, still applies |
| Address count well beyond 5M | Bloom pre-filter + address-table partitioning; lookup path already batched |
| More chains | Per-chain adapter isolates specifics; risk parameters are per-chain config; no core changes |
| Deterministic finality removed entirely | Nothing structural — that is already the assumption |

## How I would verify correctness

- **Fault-injection harness** (primary vehicle): a mock chain with controllable reorg depth/frequency and a mock custodian with duplicate/reorder/delay/drop knobs. Drives both the correctness evidence and the reorg-parameter measurements.
- **Invariants as assertions and runtime monitors:** no duplicate credit (unique constraints, tested by replay), no missed credit (reconciliation diff = empty), no credit surviving a reorg (re-checker + reversal path exercised by deep-reorg scenarios).
- **Benchmarks:** the credit write path at the estimated peak (~700 writes/s) with realistic lock contention; scanner throughput at 2x chain average plus reorg replay.
