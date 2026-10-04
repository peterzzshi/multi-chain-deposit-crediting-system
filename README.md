# Multi-Chain Deposit Crediting System

Design and prototype for crediting deposits from two chains through two modes: **self-built** (platform scans chain) and
**custodian** (custodian detects, platform verifies).

**Stack:** Go, PostgreSQL (ent ORM), no message broker  
**Assignment:** [requirements.md](requirements.md) | **Solution:** [SOLUTION.md](SOLUTION.md)

---

## Overview

Both modes feed one shared state machine: `PENDING → CREDITED → FINALIZED`, with reorg paths to
`REORGED → REVERSED/DROPPED`.

PostgreSQL is the source of truth. Deposit state, ledger entry, and balance are committed in one transaction. Exposure
monitoring holds spendability (not ledger credits) when aggregate unfinalized value exceeds cap.

**Key decisions:**

- One state machine (reorg invariants are chain properties, not mode properties)
- Exposure cap holds spendability (ledger reflects chain facts)
- PostgreSQL as durable pipeline (ACID + constraints + recovery from source)
- Confirmation depth + finality horizon (bounded monitoring cost vs unbounded protection)

See [docs/architecture.md](docs/architecture.md) for component diagram and data flows.

## Documentation

**Start here:**

- [`SOLUTION.md`](SOLUTION.md) — Direct answers to requirements (architecture, state machine, capacity, trade-offs)
- [`requirements.md`](requirements.md) — Original assignment

**Design details:**

- [`docs/architecture.md`](docs/architecture.md) — Components and data flows
- [`docs/state-machine.md`](docs/state-machine.md) — Lifecycle transitions with rationale
- [`docs/capacity-estimation.md`](docs/capacity-estimation.md) — Throughput/memory/storage math + benchmarks
- [`docs/risk-policy.md`](docs/risk-policy.md) — Confirmation depths, exposure cap, finality horizon
- [`technical-decisions.md`](technical-decisions.md) — Trade-offs and assumptions
- [`docs/adr/`](docs/adr/) — 7 architectural decision records

**Verification:**

- [`docs/e2e-test-scenarios.md`](docs/e2e-test-scenarios.md) — 30 comprehensive end-to-end scenarios
- [`docs/runbook.md`](docs/runbook.md) — Alerts and recovery procedures

**Implementation:**

- [`docs/implementation-plan.md`](docs/implementation-plan.md) — Completed prototype vs remaining production work

**Reference:**

- [`CONTEXT.md`](CONTEXT.md) — Vocabulary (transfer, provider event, logical ID, finality horizon)

## Run locally

```bash
docker compose up --build
```

The single Compose file runs PostgreSQL, a one-shot schema job, the two local
provider stubs, and the server, scanner, and worker as separate services. For
faster code iteration, the binaries can still be run directly after running
the migration job once.

Seed the sample addresses with [`scripts/seed.sql`](scripts/seed.sql). Asset
policy comes from `configs/assets.json`; applications validate and apply it at
startup for this prototype. Production should use a separate expand-and-
contract migration step rather than application-startup DDL.

## Verification

```bash
make check
make itest
```

The unit suite is hermetic. Integration tests require the PostgreSQL service.
The HTTP adapter test may need a host that permits the loopback listener used
by `httptest`.

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
