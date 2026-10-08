# Design Notes

Working notes for the multi-chain deposit-crediting assignment. Ratified decisions live in `docs/adr/`; vocabulary in `CONTEXT.md`. This file holds reasoning that doesn't belong in either.

## Language and Implementation Stack

**Go.** A reasonable choice given my experience and the IO-heavy workload. Goroutines support bounded parallel work across chain/provider requests, but concurrency primitives do not provide cross-replica correctness — shared state and ledger correctness are enforced at the durable storage boundary (ADR 0003).

- **ent** (ORM): schema-as-code with generated types fits the normalized PostgreSQL design. Two constraints from ADR 0003 to respect: the conditional balance update (`UPDATE ... WHERE balance >= amount`) and `SELECT ... FOR UPDATE` are not expressible in ent's fluent API — use ent's raw-SQL escape hatches (the `sql/lock` feature modifier, or `ExecContext` on the underlying driver) inside the same `*ent.Tx`, never an app-level check-then-act. Keep ent behind small repository interfaces so the service layer stays mockable.
- **mockery**: generates test mocks from the interfaces we own — node client, custodian client, repositories, ledger writer. Mock those interfaces, never ent's generated client.
- **Message format: no broker, no schema registry (ADR 0007).** Payloads we own are versioned Go structs persisted as `jsonb`, evolved by expand-and-contract. **Kafka + Avro are deferred, not rejected**: when a trigger in ADR 0007 fires (fan-out to multiple consumers, orders-of-magnitude higher event rates, cross-service integration), Kafka with Avro + registry is the natural upgrade, and the append-only `source_events` table is outbox-ready for CDC. Go library for that day: `hamba/avro`. Inbound custodian webhooks are provider-defined JSON regardless, normalized at the ingest boundary.

## Chains and Assets

Treat the block times, transaction volume, and reorg behavior in the prompt as the design inputs. Base or Polygon could be illustrative examples, but their real behavior should not silently replace the hypothetical conditions. Support assets through configuration, keyed by chain and asset identity; native coins and tokens have different transfer observations and metadata.

Credit mode is selected once per canonical asset, while confirmation and risk
thresholds remain chain-specific. Both modes operate at the same time across
the asset set. Persist the selected mode and relevant asset
configuration/version on each deposit, so a later configuration change does
not reinterpret an in-flight deposit.

The prompt's one-address-per-chain rule is enforced literally: one row per
`(account, chain)`, plus a separate unique constraint on `(chain, address)` so
that a physical address cannot be assigned twice. The address is mode-neutral;
the asset configuration owns routing, so a user cannot silently split one
chain across two mode-specific addresses.

## Transactions, Transfers, and Events

Canonical definitions live in `CONTEXT.md` (blockchain transaction vs transfer vs chain observation vs provider event). Design consequences recorded here:

- A single transaction can contain several creditable transfers; logical transfer identity must therefore include the log/trace index, never just the transaction hash (ADR 0001).
- Fireblocks often made these concepts appear one-to-one because its transaction object was the provider's aggregate. The assignment deliberately separates them: a webhook may describe a transaction, a vault balance event describes a later provider observation; neither is the ledger credit itself.
- Persist webhook/provider observations and deduplicate them. Do not discard a delayed or out-of-order observation just because an earlier one moved the deposit to a later business state: the later observation may reveal a reorg or correct earlier data.

## Deposit Observation by Mode

- **Self-built mode:** the platform scans chain data through the supplied node interface. It does not operate a node, but the design explains how scanning resumes after failure and detects changed canonical blocks (see Block Streams and Cursors below).
- **Custodian mode:** the custodian detects deposits and sends webhooks; its query API is a second provider-side source. Webhooks are a hint channel only — chain facts verified by the platform are the source of truth (ADR 0004).
- **Both modes:** the platform may query on-chain data. For custodian deposits this validates transaction/block facts and reconciles provider notifications; it does not make webhook and chain scan interchangeable.

## Address Lookup

Store address-to-owner mappings in PostgreSQL with an index; asset policy is
looked up separately. Five million 20-byte EVM addresses are ~100 MB of raw
bytes; indexes, row metadata, and associated fields add overhead (see Capacity
Estimation). An in-memory LRU cache can cut repeated lookups but needs bounded
sizing and cross-replica invalidation behavior.

A Bloom filter is an optional membership pre-filter: "definitely absent" avoids a database lookup; "possibly present" still requires one. It is an optimization, not the source of truth — measure lookup cost before adding it.

Private-key derivation and seed storage are out of scope per the prompt. Address lookup at this scale is in scope.

## PostgreSQL Shape

PostgreSQL is the transactional source of truth. Normalized fields used for lookup and constraints go in ordinary columns; `jsonb` holds raw provider payloads and diagnostic metadata, never hot-path identity or amount fields. Addresses are normalized fixed-length bytes or canonical text, with a unique index on `(chain, address)`. Amounts are integer base units, never floating point.

First decomposition:

| Table                           | Contents                                                                                     |
|---------------------------------|----------------------------------------------------------------------------------------------|
| `asset_configs`                 | chain, asset/contract, decimals, custody mode, confirmation policy                           |
| `deposit_addresses`             | user/account, chain, address, active/version metadata; mode comes from asset policy |
| `chain_cursors`, `chain_blocks` | last processed height/hash + recent parent/hash window for restart and reorg detection       |
| `source_events`                 | append-only webhook/provider envelopes with source event IDs and raw payloads                |
| `deposits`                      | one logical creditable transfer, its lifecycle state, canonical block facts                  |
| `ledger_entries`                | append-only credits, debits, reversals, with a unique source reference                       |
| `account_balances`              | optional materialized balances for fast reads, maintained in the same transaction as entries |

Keep the concepts separate — immutable observations, the current deposit projection, the ledger — even if the table count changes. A join is not inherently slow: foreign-key and composite indexes, selective predicates, reasonable row sizes, and `EXPLAIN (ANALYZE, BUFFERS)` matter more than avoiding normalization. Tune PostgreSQL after measuring (pool size, autovacuum, statistics, memory, partitioning); query shape and indexes come first. Do not put large raw payloads on every hot-path row without a retention plan.

## Event Identity, Ordering, and Delivery

Settled in ADR 0001 (two-layer identity). Additional notes:

- An idempotency key for a client API request is not the identity of an on-chain deposit. Timestamps are never identity or ordering mechanisms — only block height/hash and log/trace indexes.
- Use durable unique constraints and idempotent state/ledger writes so repeated observations do not create repeated credits.
- No broker is used — PostgreSQL is the durable pipeline (ADR 0007). Even with one, a broker can only replay events already accepted: it cannot discover a transfer the scanner missed or a webhook the provider never delivered. Recovery always needs a source to reconcile against — rescanning from the durable chain cursor, or polling the custodian API.

## Ledger Concurrency and Reorgs

Settled in ADR 0003 (single writer, per-`(account, asset)` serialization, one transaction for state + entry + balance) and ADR 0002 (reversal path, finality horizon). Additional notes:

- A Go mutex coordinates goroutines in one process only; it cannot protect the ledger across replicas or other services.
- The sender does not receive tokens back from the platform on a reorg: the canonical chain rolls back the transfer (the sender may again control those tokens), and the platform reverses its internal credit. A reversal is not an on-chain refund.
- Spending holds (playthrough policy) can reduce exposure, but they are a product/ledger policy shared with debit flows, not a substitute for reorg detection.
- The EVM account model does not change the platform ledger design: the chain maintains an address balance; the platform maintains an append-only per-`(account, chain, asset)` ledger with an optional balance projection. Not a UTXO list.

## Zero-Downtime Releases

Replicas and load balancing give availability, but safe releases also require old and new versions to coexist against compatible schemas and event formats. Use expand-and-contract: add compatible schema, deploy code that tolerates both representations, migrate/switch usage, remove the old schema only after old workers are gone. Endpoint versioning helps clients migrate but does not solve worker or database compatibility. Payloads we own follow the same discipline — versioned Go structs + `jsonb`, no separate schema registry to keep compatible (ADR 0007).

## Block Streams and Cursors

The scanner processes a stream of blocks incrementally rather than loading the chain into memory. Per chain, persist a durable cursor with at least the last processed height and block hash (ideally parent hash). Read the next block, extract candidate native transfers and token logs, resolve recipient addresses in batches against the indexed address table, persist observations and transfer facts, and advance the cursor only after durable processing succeeds.

At the stated averages, the 12-second chain has ~250 tx/s and the 2-second chain ~1,500 tx/s; the stream keeps memory bounded while processing ~3,000 transactions per block. Do not issue a database lookup per transaction: collect candidate addresses from a block and use a batched indexed lookup (`ANY` query or staging table). A Bloom filter can reject most addresses first, but positives still require PostgreSQL confirmation.

The cursor is not a finality marker. On restart, replaying the last few blocks is expected and must be idempotent. On a parent/hash mismatch, rewind into the retained reorg window, mark affected observations non-canonical, and rescan the replacement branch. Custodian webhooks enter the same durable observation pipeline; provider-API reconciliation covers notifications that never arrive.

## Capacity Estimation

Moved to [docs/capacity-estimation.md](docs/capacity-estimation.md) as a standalone deliverable. Summary:
- **Chain observation**: 1,750 tx/s average, ~3,500 tx/s at 2x peak
- **Deposit write throughput**: ~70–700 writes/s (depends on 0.1%–1% match rate)
- **Scanner memory**: ~100–500 MiB per process
- **Storage**: ~0.6–6 GB/day for 30-day hot retention
- **Measured benchmarks**: 783 credits/s (parallel), 228k tx/s scanner filter, 58ms deep reorg replay

## Assumptions

Explicit assumptions the design rests on:

1. **Node interface** provides blocks with hash + parent hash, transaction receipts with logs, and execution traces (needed for internal native transfers). Per-chain adapter hides the differences; the fast chain is assumed account-based with equivalent per-transfer observability and probabilistic finality (the prompt's reorg behavior rules out relying on a finality gadget).
2. **Address uniqueness**: each account has one deposit address per chain, and each physical address belongs to one account; enforced by unique constraints on `(account, chain)` and `(chain, address)`. Mode is not an address property; the asset policy owns it.
3. **Timestamps are never used** for identity or ordering — only block height/hash and log/trace indexes.
4. **Amounts** are integer base units everywhere; decimals live only in `asset_configs` for display.
5. **Webhook delivery** may duplicate, reorder, delay, or silently drop events (prompt-given); the custodian query API is eventually consistent with the custodian's own books.
6. **Custodian does not reliably notify reorgs.** The platform independently re-checks credited-but-unfinalized custodian deposits via targeted on-chain queries until the finality horizon.
7. **Hot wallet / pool balance** (withdrawal-side) uses optimistic conditional updates with a retryable insufficient-funds error; kept low as a security policy. Atomic guard is the DB conditional update, never app-level check-then-act.

## Out of scope

Per the prompt:
- Withdrawal flow itself, fund sweeping/consolidation, key management and signing, node selection/operations.

By design choice (noted for walkthrough):
- **Webhook signature verification / provider authentication**: assumed in production, excluded from the design discussion to keep focus on crediting correctness.
- **Below-minimum (dust) deposits**: the minimum is enforced at the UI; the system **skips the credit** but persists the observation in terminal state BELOW_MINIMUM for support visibility (UI enforcement is not a boundary — direct on-chain sends bypass it). No ledger effect, so no correctness risk. Unsupported assets remain silently dropped.
- Debit flow internals (withdrawal/trade execution); only the ledger's concurrency boundary with them is in scope.
