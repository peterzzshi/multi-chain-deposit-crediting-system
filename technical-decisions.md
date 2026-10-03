# Design Notes

Working answers and trade-offs for the multi-chain deposit-crediting assignment. Ratified decisions live in `docs/adr/`; vocabulary in `CONTEXT.md`; risk models in `docs/risk-policy.md`. This file holds the reasoning that doesn't belong in any of those, plus the capacity estimation deliverable.

## Language and Implementation Stack

**Go.** A reasonable choice given my experience and the IO-heavy workload. Goroutines support bounded parallel work across chain/provider requests, but concurrency primitives do not provide cross-replica correctness — shared state and ledger correctness are enforced at the durable storage boundary (ADR 0003).

- **ent** (ORM): schema-as-code with generated types fits the normalized PostgreSQL design. Two constraints from ADR 0003 to respect: the conditional balance update (`UPDATE ... WHERE balance >= amount`) and `SELECT ... FOR UPDATE` are not expressible in ent's fluent API — use ent's raw-SQL escape hatches (the `sql/lock` feature modifier, or `ExecContext` on the underlying driver) inside the same `*ent.Tx`, never an app-level check-then-act. Keep ent behind small repository interfaces so the service layer stays mockable.
- **mockery**: generates test mocks from the interfaces we own — node client, custodian client, repositories, ledger writer. Mock those interfaces, never ent's generated client.
- **Message format: no broker, no schema registry (ADR 0007).** Payloads we own are versioned Go structs persisted as `jsonb`, evolved by expand-and-contract. **Kafka + Avro are deferred, not rejected**: when a trigger in ADR 0007 fires (fan-out to multiple consumers, orders-of-magnitude higher event rates, cross-service integration), Kafka with Avro + registry is the natural upgrade, and the append-only `source_events` table is outbox-ready for CDC. Go library for that day: `hamba/avro`. Inbound custodian webhooks are provider-defined JSON regardless, normalized at the ingest boundary.

## Chains and Assets

Treat the block times, transaction volume, and reorg behavior in the prompt as the design inputs. Base or Polygon could be illustrative examples, but their real behavior should not silently replace the hypothetical conditions. Support assets through configuration, keyed by chain and asset identity; native coins and tokens have different transfer observations and metadata.

Credit mode is selected per `(chain, asset)` pair, and both modes operate at the same time. Persist the selected mode and relevant asset configuration/version on each deposit, so a later configuration change does not reinterpret an in-flight deposit.

The prompt's one-address-per-chain rule is resolved by widening the mapping: an address belongs to exactly one `(user, chain, mode)` — a user may hold one self-built and one custodian address per chain — enforced by a unique constraint on `(chain, address)` (see `docs/assumptions-and-scope.md`). A self-generated platform address and a custodian-generated address cannot be the same physical address, so this is the minimal consistent interpretation.

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

Store address-to-owner/asset/mode mappings in PostgreSQL with an index. Five million 20-byte EVM addresses are ~100 MB of raw bytes; indexes, row metadata, and associated fields add overhead (see Capacity Estimation). An in-memory LRU cache can cut repeated lookups but needs bounded sizing and cross-replica invalidation behavior.

A Bloom filter is an optional membership pre-filter: "definitely absent" avoids a database lookup; "possibly present" still requires one. It is an optimization, not the source of truth — measure lookup cost before adding it.

Private-key derivation and seed storage are out of scope per the prompt. Address lookup at this scale is in scope.

## PostgreSQL Shape

PostgreSQL is the transactional source of truth. Normalized fields used for lookup and constraints go in ordinary columns; `jsonb` holds raw provider payloads and diagnostic metadata, never hot-path identity or amount fields. Addresses are normalized fixed-length bytes or canonical text, with a unique index on `(chain, address)`. Amounts are integer base units, never floating point.

First decomposition:

| Table                           | Contents                                                                                     |
|---------------------------------|----------------------------------------------------------------------------------------------|
| `asset_configs`                 | chain, asset/contract, decimals, custody mode, confirmation policy                           |
| `deposit_addresses`             | user/account, chain, address, mode, asset scope, active/version metadata                     |
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

The key distinction is **chain-observation throughput** vs **deposit throughput**: the scanner must keep up with every block, but database ledger writes are needed only for matched deposit transfers and provider events.

### Input throughput

```text
blocks_per_day = 86,400 / block_time
tx_per_second  = tx_per_block / block_time
tx_per_day     = blocks_per_day * tx_per_block
```

| Chain        | Block time | Blocks/day | Average tx/s |  Transactions/day |
|--------------|-----------:|-----------:|-------------:|------------------:|
| EVM-like     |       12 s |      7,200 |          250 |      21.6 million |
| Faster chain |        2 s |     43,200 |        1,500 |     129.6 million |
| **Total**    |            | **50,400** |    **1,750** | **151.2 million** |

Size production for a peak multiplier, not the average: at 2x peak the scanner and parsing pipeline sustain ~3,500 tx/s, with headroom for replaying a reorg window. A block is a bounded batch of ~3,000 transactions; the stream never loads a whole day into memory.

### Scanner memory

```text
working_set ~= blocks_in_flight * tx_per_block * bytes_per_tx * expansion_factor
```

With 2 blocks in flight, a 1–4 KiB normalized transaction representation, and 3x parsing/indexing overhead:

```text
2 * 3,000 * (1–4 KiB) * 3 ~= 18–72 MiB per chain worker
```

Add RPC buffers, queues, metrics, and the address cache: a practical initial allocation is ~100–500 MiB per scanner process, validated by load tests. The exact value depends on node response sizes and whether traces are requested.

Optional Bloom filter sizing: `m = -n * ln(fpr) / ln(2)^2`. For `n = 5,000,000` addresses, a 1% false-positive filter is ~6 MiB; 0.1% is ~9 MiB. Positives still require an indexed PostgreSQL lookup.

### Address and event storage

Five million 20-byte addresses are ~100 MB raw, but row metadata, ownership fields, and indexes make the footprint materially larger: budget ~0.5–2 GB for the address mapping until measured.

Do not persist every full transaction body indefinitely. Retain compact block headers/cursors and matched observations; large raw provider payloads fall under a retention policy.

```text
records_per_day  = transactions_per_day * deposit_match_rate
storage_per_day ~= records_per_day * bytes_per_record * storage_multiplier
```

Illustrative scenario, not a requirement — 0.1% match rate, 2 KiB per normalized record, 2x table/index overhead:

```text
151.2M * 0.001 = 151,200 records/day
151,200 * 2 KiB * 2 ~= 0.6 GB/day
30-day hot retention ~= 18 GB
```

At a 1% match rate the same assumptions give ~6 GB/day and ~180 GB for 30 days. State the assumed match rate and retention period rather than claiming a single storage number.

### Database write throughput

Let `r` be the matched-transfer rate and `w` the durable writes per matched transfer (source observation, transfer projection, ledger entry, optional balance projection):

```text
write_rate ~= r * w
```

At 0.1% match, `r` ≈ 1.75 matched transfers/s; at 1%, ≈ 17.5/s. With four durable writes per match and a 10x peak factor: ~70 writes/s or ~700 writes/s respectively. This is the workload to benchmark PostgreSQL and the ledger-concurrency design against — it is not 1,750 ledger writes/s, because most chain transactions are irrelevant to the platform.

### Storage detail (per table, first year)

- `source_events` (custodian webhooks): ~50% of matched records at 2–3x duplicate deliveries, ~1.5 KiB jsonb payload → ~0.3 GB/day raw. Dominant growth table: 90-day hot retention, archive beyond.
- `deposits` projection: ~0.4 KiB/row incl. indexes → ~60 MB/day at 0.1% match.
- `ledger_entries`: credits + debits + rare reversals at ~2x the deposit rate, ~250 B/row → ~75 MB/day.
- `chain_blocks` retained reorg window (EVM 500 + fast chain 5,000 hashes): < 5 MB — negligible.

### Reorg replay

Replay is CPU/DB-bound, not arrival-bound, so catch-up runs faster than live ingest: re-filtering the full retained window (worst case 5,000 fast-chain blocks ≈ 15M tx) at ~10k tx/s takes ~25 minutes for an exceptional deep event; typical reorgs of 2–10 blocks replay in seconds. Cursor commits are ~35/min — trivial.

These are order-of-magnitude planning numbers. Replace the match rate, payload size, peak multiplier, retention period, and replay depth with measured or explicitly agreed assumptions before the walkthrough. The reorg-risk models in [docs/risk-policy.md](docs/risk-policy.md) tune confirmation and exposure policy; they do not substitute for this throughput, memory, and storage estimate.

### Measured (P5 benchmarks, 2026-10-03)

Environment: Apple M2, dockerized Postgres 16, single test client; integration benchmarks in `internal/store/bench_integration_test.go` and `internal/scanner/bench_integration_test.go` (`go test -tags=integration -bench`). Order-of-magnitude validation of the estimates above, not a production sizing.

| Path                                                            |                            Estimate |                                                 Measured | Verdict                                                             |
|-----------------------------------------------------------------|------------------------------------:|---------------------------------------------------------:|---------------------------------------------------------------------|
| Credit write path (sequential, one account)                     | ~70–700 writes/s peak (system-wide) | ~355 credits/s (~2.8 ms/credit, ~8 statements in one tx) | per-account serialization bound, as designed                        |
| Credit write path (parallel, 16 accounts)                       |                                   — |                                           ~783 credits/s | system-wide peak clears the 700/s stress case                       |
| External debits (sequential)                                    |                                   — |                                            ~936 debits/s | headroom                                                            |
| Scanner filter (10k-address table, 3,000-tx blocks, 0.1% match) |              ~3,500 tx/s at 2x peak |                                            ~228,000 tx/s | ~65x headroom; re-test at 5M addresses before Bloom filter decision |
| Deep-reorg rewind + replay (10 blocks)                          |                             seconds |                                                   ~58 ms | well inside one block interval                                      |

Caveats: benchmark Postgres runs unsynchronized (local docker); the 5M-row address table will slow `ResolveRecipients` vs the 10k-row test — the unique `(chain, address)` index keeps lookup O(log n), so degradation should be modest, but the Bloom pre-filter decision waits for that measurement (non-goals).

## Open Design Work — status

- ~~Define deposit states~~ → Resolved: shared machine `PENDING → CREDITED → FINALIZED` with `REORGED` / `DROPPED` / `REVERSED` / `BELOW_MINIMUM` (ADR 0005, glossary in CONTEXT.md).
- ~~Confirmation thresholds and reorg behavior before/after credit~~ → Resolved: minimal parameter set in `docs/risk-policy.md` §5; reversal + finality horizon in ADR 0002.
- ~~Address ownership across modes~~ → Resolved: address belongs to exactly one `(user, chain, mode)`; mode per `(chain, asset)`; unique `(chain, address)`.
- ~~Below-minimum (dust) deposits (Q16)~~ → Resolved: skip the credit, persist the observation in terminal `BELOW_MINIMUM` for support visibility.
- ~~Ingest pipeline shape + message format (Q19/Q20)~~ → Resolved: broker-less pipeline over PostgreSQL; Kafka + Avro deferred with trigger conditions (ADR 0007). Stack recorded there: Go + ent + mockery.
- ~~Scanner-watching custodian addresses (Q22)~~ → Deferred: per-deposit targeted queries + 5-min reconciliation poll suffice (noted in ADR 0004).
- ~~Exposure-cap enforcement (Q21)~~ → Resolved: runtime monitor; at/over cap, new credits post but spendability is held (docs/risk-policy.md §4).
- Capacity assumptions → Committed for the deliverable (Q23): 0.1% match rate as base, 1% as stress; 2x scanner / 10x write peak; 30-day hot retention. Flagged as illustrative in the estimate above.
- Reconciliation and correctness tests → Approach agreed: fault-injection harness (mock chain with controllable reorgs + mock custodian with duplicate/reorder/delay/drop knobs) as the primary verification vehicle; invariant checks as test assertions plus documented runtime monitors; chain re-checker re-verifies custodian deposits on-chain until the finality horizon (Q15).
- ~~Deliverables~~ → Produced: [docs/architecture.md](docs/architecture.md), [docs/state-machine.md](docs/state-machine.md), [docs/trade-offs.md](docs/trade-offs.md); capacity estimation is the section above.
