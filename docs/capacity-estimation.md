# Capacity Estimation

Order-of-magnitude throughput, memory, and storage calculations for the multi-chain deposit crediting system. This is a formal deliverable for the design problem.

## Key Distinction

**Chain-observation throughput** vs **deposit throughput**: the scanner must keep up with every block, but database ledger writes are needed only for matched deposit transfers and provider events.

## Input Throughput

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

## Scanner Memory

```text
working_set ~= blocks_in_flight * tx_per_block * bytes_per_tx * expansion_factor
```

With 2 blocks in flight, a 1–4 KiB normalized transaction representation, and 3x parsing/indexing overhead:

```text
2 * 3,000 * (1–4 KiB) * 3 ~= 18–72 MiB per chain worker
```

Add RPC buffers, queues, metrics, and the address cache: a practical initial allocation is ~100–500 MiB per scanner process, validated by load tests. The exact value depends on node response sizes and whether traces are requested.

Optional Bloom filter sizing: `m = -n * ln(fpr) / ln(2)^2`. For `n = 5,000,000` addresses, a 1% false-positive filter is ~6 MiB; 0.1% is ~9 MiB. Positives still require an indexed PostgreSQL lookup.

## Address and Event Storage

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

## Database Write Throughput

Let `r` be the matched-transfer rate and `w` the durable writes per matched transfer (source observation, transfer projection, ledger entry, optional balance projection):

```text
write_rate ~= r * w
```

At 0.1% match, `r` ≈ 1.75 matched transfers/s; at 1%, ≈ 17.5/s. With four durable writes per match and a 10x peak factor: ~70 writes/s or ~700 writes/s respectively. This is the workload to benchmark PostgreSQL and the ledger-concurrency design against — it is not 1,750 ledger writes/s, because most chain transactions are irrelevant to the platform.

## Storage Detail (per table, first year)

- `source_events` (custodian webhooks): ~50% of matched records at 2–3x duplicate deliveries, ~1.5 KiB jsonb payload → ~0.3 GB/day raw. Dominant growth table: 90-day hot retention, archive beyond.
- `deposits` projection: ~0.4 KiB/row incl. indexes → ~60 MB/day at 0.1% match.
- `ledger_entries`: credits + debits + rare reversals at ~2x the deposit rate, ~250 B/row → ~75 MB/day.
- `chain_blocks` retained reorg window (EVM 500 + fast chain 5,000 hashes): < 5 MB — negligible.

## Reorg Replay

Replay is CPU/DB-bound, not arrival-bound, so catch-up runs faster than live ingest: re-filtering the full retained window (worst case 5,000 fast-chain blocks ≈ 15M tx) at ~10k tx/s takes ~25 minutes for an exceptional deep event; typical reorgs of 2–10 blocks replay in seconds. Cursor commits are ~35/min — trivial.

These are order-of-magnitude planning numbers. Replace the match rate, payload size, peak multiplier, retention period, and replay depth with measured or explicitly agreed assumptions before the walkthrough. The reorg-risk models in [risk-policy.md](risk-policy.md) tune confirmation and exposure policy; they do not substitute for this throughput, memory, and storage estimate.

## Measured Benchmarks (validation)

Environment: Apple M2, dockerized Postgres 16, single test client; integration benchmarks in `internal/store/bench_integration_test.go` and `internal/scanner/bench_integration_test.go` (`go test -tags=integration -bench`). Order-of-magnitude validation of the estimates above, not a production sizing.

| Path                                                            |                            Estimate |                                                 Measured | Verdict                                                             |
|-----------------------------------------------------------------|------------------------------------:|---------------------------------------------------------:|---------------------------------------------------------------------|
| Credit write path (sequential, one account)                     | ~70–700 writes/s peak (system-wide) | ~355 credits/s (~2.8 ms/credit, ~8 statements in one tx) | per-account serialization bound, as designed                        |
| Credit write path (parallel, 16 accounts)                       |                                   — |                                           ~783 credits/s | system-wide peak clears the 700/s stress case                       |
| External debits (sequential)                                    |                                   — |                                            ~936 debits/s | headroom                                                            |
| Scanner filter (10k-address table, 3,000-tx blocks, 0.1% match) |              ~3,500 tx/s at 2x peak |                                            ~228,000 tx/s | ~65x headroom; re-test at 5M addresses before Bloom filter decision |
| Deep-reorg rewind + replay (10 blocks)                          |                             seconds |                                                   ~58 ms | well inside one block interval                                      |

Caveats: benchmark Postgres runs unsynchronized (local docker); the 5M-row address table will slow `ResolveRecipients` vs the 10k-row test — the unique `(chain, address)` index keeps lookup O(log n), so degradation should be modest, but the Bloom pre-filter decision waits for that measurement.

## Assumptions Summary

For the walkthrough, the following assumptions are used:
- **Match rate**: 0.1% base case, 1% stress case
- **Peak multiplier**: 2x for scanner throughput, 10x for write bursts
- **Retention**: 30-day hot storage for deposits and source events
- **Address scale**: 1M current, 5M within one year

These are illustrative inputs; production sizing would use measured values from pilot traffic.
