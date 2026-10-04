# Implementation Status

The repository is a working prototype of the correctness core and its two
ingest paths. The detailed design belongs in [architecture.md](architecture.md),
[state-machine.md](state-machine.md), [risk-policy.md](risk-policy.md), and the
[ADRs](adr/); this page tracks what's completed vs what remains for production.

## Repository boundaries

- `cmd/` contains the scanner, webhook server, and worker entry points.
- `internal/domain/` contains pure deposit, identity, and ledger rules.
- `internal/scanner/` and `internal/custodian/` implement the two ingest paths.
- `internal/credit/` coordinates state, ledger, and balance writes.
- `internal/store/` adapts PostgreSQL/ent persistence to those interfaces.
- `internal/risk/` implements exposure holds and runtime invariant checks.
- `internal/adapters/` contains chain and provider seams, including local stubs.
- `docs/` contains architecture, policies, ADRs, verification, and operations.

## ✅ Completed Prototype Work

- **Shared state machine**: Logical transfer identity, append-only ledger, and transactional balance projection (ADR 0001, 0003, 0005)
- **Self-built scanner**: Durable cursors, reorg rewind/replay, depth handling, and idempotent restart behavior
- **Custodian path**: Claim verification, source-event deduplication, reconciliation, re-checking, re-inclusion, and solvency checks (ADR 0004)
- **Risk enforcement**: Exposure-cap spendability holds and runtime checks for missed credits, balance drift, and unresolved reorgs (ADR 0002, 0006)
- **Local verification**: One Compose topology for PostgreSQL, provider stubs, server, scanner, and worker; services remain independently restartable and scalable
- **Schema setup**: Isolated in a one-shot migration container instead of being run by every long-lived process
- **Test coverage**: Unit, PostgreSQL integration, fault-injection, and benchmark coverage for the prototype scenarios

Run `make check` for hermetic checks and `make itest` for PostgreSQL-backed tests. Local walkthrough scenarios are in [manual-verification.md](manual-verification.md).

## ⏳ Remaining Production Work

### Critical for production correctness:
1. **Immutable policy revisions pinned to deposits** (medium-large change)
   - New revision tables/columns, activation and audit metadata
   - Policy lookups read from deposit's pinned revision, not live config
   - Backfill rules for existing deposits
   - Regression coverage for old/new workers running together during rollout
   - Prevents in-flight deposits from being reinterpreted by config changes

2. **Explicit versioned migrations** (replaces auto-migration)
   - Numbered SQL migrations with review process
   - Replace prototype Ent auto-migration and config upserts
   - Add policy rollout process separate from code deployment

3. **Real chain and custodian adapters**
   - Implement behind existing `chain.Client` and `custodian.Provider` interfaces
   - Production node selection, failover, rate limiting
   - Webhook signature verification and authentication

### Data integrity and observability:
4. **Persist raw provider envelopes** (already structured, needs completion)
   - Full webhook payloads for audit trail
   - Model vault balance-change events separately from crediting events
   - Retention policy for large payloads

5. **Address uniqueness migration**
   - Explicit migration for `(chain, address)` uniqueness constraint
   - Remove legacy address-mode column after migrating conflicting rows
   - Document the constraint enforcement strategy

6. **Metrics, dashboards, and alerting**
   - Prometheus exporters for exposure, solvency, reorg lag
   - Grafana dashboards for operational monitoring
   - Alert thresholds and runbook integration

### Deployment and operations:
7. **Production manifests and node operations**
   - Kubernetes/deployment configs with resource limits
   - Health checks, graceful shutdown, circuit breakers
   - Node connection pooling and retry strategies

### Out of scope (deliberately excluded):
- Withdrawals, sweeping, key management, signing
- These are separate systems with their own complexity

The prototype does not claim production readiness; these gaps are documented so the walkthrough discussion can focus on the correctness design and capacity planning that *are* complete.
