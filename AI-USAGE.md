# AI Usage

## Approach

I worked **bottom-up**: used AI to explore the requirements fully before locking in any design, wrote down the decisions as ADRs, then built from that confirmed design.

## What AI Helped With

### Exploring the problem
- Looked at options for each requirement (identity, state machine, ledger, pipeline, risk policy)
- Worked through edge cases (reorgs after credit, duplicate webhooks, hot-account contention, deep reorgs)
- Did the capacity and risk math (throughput formulas, reorg probability, attack economics)
- **Result:** ADRs recording the decisions I evaluated and picked

### Writing docs
- Organized the docs with one consistent vocabulary
- Wrote the required deliverables (architecture, state machine, capacity estimation, trade-offs)

### Building
- Generated scaffolding (scanner loop, credit engine, state machine table, store adapters)
- Implemented the core logic (state machine, ledger, identity)
- Wired up infrastructure (PostgreSQL adapters, chain/custodian integration)
- **Result:** a working prototype (tests pass, benchmarks match the estimates)

## What I Did Myself

**Design calls:**
- One state machine instead of two (reorg behavior is a chain property, not a mode property)
- Append-only ledger with reversals, for auditability
- Exposure cap holds spendability instead of blocking the ledger entry (ledger still reflects chain facts)
- PostgreSQL as the durable pipeline (ACID + constraints + recovery from source, no broker needed yet)

**Review:**
- Read every line of generated code for correctness
- Refactored for clarity (naming, extracted constants, simpler flow)
- Wrote tests for edge cases (idempotency, locking, reorg replay)
- Built fault-injection harnesses (stub chain/custodian)

**Judgment calls:**
- Flagged assumptions that need real data (match rates, peak multipliers, retention periods)
- Picked manual test scenarios based on realistic failure modes
- Decided what's prototype-scope vs. what's production scaffolding

## Challenges

While the app was running since day one, but the code quality suffered from huge duplication and some bad structural calls I didn't catch until the full app existed.
Examples: 
- Near-identical functions that should have been one function with a parameter
- Logic that belonged in one package leaking into another
- Setup code repeated per-chain instead of extracted

These issues were briefly noticed while building piece by piece (In isolation the code looks fine and tests pass), but only surfaced when the system was assembled as a whole during full review.
This is where I spent more time with AI, as a result of addressing the issues and limitations of building from bottom-up: locally correct code, but keeping a good design pattern takes more time to refactor.
