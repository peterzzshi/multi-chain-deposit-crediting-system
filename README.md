# Multi-Chain Deposit Crediting System

Design and prototype for crediting deposits from two chains supporting two modes: **self-built** and **custodian**
---

## Overview

Both modes feed one shared state machine: `PENDING → CREDITED → FINALIZED`, with reorg paths to
`REORGED → REVERSED/DROPPED`.

PostgreSQL is the source of truth. Deposit state, ledger entry, and balance are committed in one transaction. Exposure
monitoring holds spendability (not ledger credits) when aggregate unfinalized value exceeds cap.

## Documentation

**Design details:**

- [`docs/architecture.md`](docs/architecture.md) — Components and data flows
- [`docs/state-machine.md`](docs/state-machine.md) — Lifecycle transitions with rationale
- [`docs/capacity-estimation.md`](docs/capacity-estimation.md) — Throughput/memory/storage math and benchmarks
- [`docs/trade-offs.md`](docs/trade-offs.md) — Trade-off index with canonical sources
- [`technical-decisions.md`](technical-decisions.md) — Supporting reasoning and assumptions

**Reference:**

- [`CONTEXT.md`](CONTEXT.md) — Vocabulary (transfer, provider event, logical ID, finality horizon)

## Run locally

```bash
docker compose up --build
```

This starts
- PostgreSQL
- a one-shot schema job
- two chain-node stubs (one per network: `stubchain` on :9100, `fastchain` on :9101)
- a custodian-api stub (:9300),
- `server` (:9200, HTTP)
- `scanner` (self-built ingest)
- `worker` (background loops). 
Once the stack is up, seed the sample deposit addresses:

```bash
docker compose exec -T postgres psql -U postgres -d deposit_crediting < scripts/seed.sql
```

Asset policy (min amount, confirmation depths, exposure caps) comes from
`configs/assets.json`; each binary validates and applies it on boot.
Production should use a separate expand-and-contract migration step instead
of application-startup DDL.

## Trigger a deposit and observe it

The chain-node stub only advances when you tell it to, so every scenario is
deterministic. Credit a native transfer to `alice` (seeded on `stubchain`,
asset `ETH`, `n_credit` 2 blocks):

```bash
# 1. Put a transfer in a block (deposit is now visible but PENDING)
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xabc","to":"0xaaa","asset":"ETH","amount":"1000"}]}'

# 2. Mine blocks until it reaches n_credit depth
curl -s -X POST localhost:9100/admin/mine -d '{"count":2}'

# 3. Read it back
curl -s localhost:9200/v1/deposits/stubchain:0xabc:native
curl -s localhost:9200/v1/balances/alice
```

The scanner picks up the mined block within its poll interval; the deposit
moves `PENDING → CREDITED` once depth is reached, and alice's ETH balance
reflects the credit. Mine further to `n_finalize` depth and the deposit
becomes `FINALIZED`.

To see a reorg reverse a credit, a custodian-mode webhook flow, or any of the
other request/response pairs (dust deposits, duplicate webhooks, exposure
caps, concurrent debits, restarts mid-reorg), see
[`docs/e2e-test-scenarios.md`](docs/e2e-test-scenarios.md) — it has the exact
`curl` commands and expected output for 29 scenarios.

## Release safety

Versioning alone does not provide zero downtime. Policy and payload revisions
prevent semantic drift for in-flight deposits, but a release still needs an
expand-and-contract migration, code that reads both old and new shapes during a
rolling deployment, health-gated traffic shifting, and a rollback-compatible
contract step. The prototype isolates schema setup in the Compose migration
job; production should replace that job's Ent auto-migration with reviewed,
numbered migrations and pin an immutable policy revision on each deposit.

## Current boundaries

The prototype deliberately leaves real chain/provider adapters, webhook
authentication, production migrations, metrics dashboards, withdrawals,
sweeping, key management, and node operations outside the implementation.
The interfaces and local stubs are intended to make those boundaries explicit.
