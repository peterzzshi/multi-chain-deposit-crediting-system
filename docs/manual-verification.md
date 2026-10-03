# Manual Verification Scenarios

End-to-end checks you can run locally against stub APIs. Each scenario
derives from a requirement in `requirements.md` — not from the
implementation — and states the exact steps and the expected outcome.

All commands below were executed against this repository and produced the
documented results.

## Local topology

```
 curl (you)                curl (you = webhook channel)
    │                            │
    ▼                            ▼
 chainstub :9100 ──► scanner ──►│
 (stub chain node)   (self-built│   server :9200 ──► PostgreSQL :5432
    ▲                ingest)    │   (webhooks, debits, reads)
    │                            │        ▲
    └──────── worker ◄───────────┘        │
 (re-checker, risk,      custodianstub :9300
  invariants,            (stub custodian:
  reconciliation) ◄────── query API + vaults)
```

- **chainstub** is a stand-in chain node. Blocks are mined only when you
  say so (`POST /admin/blocks`, `/admin/mine`), and reorgs happen only
  when you trigger them (`POST /admin/reorg`) — every scenario is
  deterministic.
- **custodianstub** is a stand-in custodian. Its query API always tells
  the truth; the webhook channel is *you*: `GET /admin/webhooks` drains
  pending deliveries, and you POST them to the app's webhook endpoint.
  Faults (duplicate/delayed/dropped) are injected at observation time.
- The app (scanner, server, worker) treats both stubs exactly like real
  integrations — over HTTP, behind the same interfaces a real adapter
  would implement.

## Setup

```bash
# 1. PostgreSQL (only service in docker-compose)
docker compose up -d postgres

# 2. Start the five processes (one terminal each, or append &)
go run ./cmd/chainstub                                        # :9100 stub chain node
go run ./cmd/custodianstub                                    # :9300 stub custodian
go run ./cmd/scanner                                          # self-built ingest
go run ./cmd/server                                           # :9200 webhooks/debits/reads
CUSTODIAN_API_URL=http://localhost:9300 go run ./cmd/worker   # background loops

# 3. Seed config + addresses (schema is created by any app binary on boot)
docker compose exec -T postgres psql -U postgres -d deposit_crediting < scripts/seed.sql

# 4. Optional: clean slate between runs
docker compose exec -T postgres psql -U postgres -d deposit_crediting -c \
  "TRUNCATE ledger_entries, account_balances, deposits, source_events, canonical_blocks, chain_cursors, exposure_states;"
```

Seeded world (chain `stubchain`):

| Account | Address | Mode | Asset | Min | n_credit | n_finalize | Reorg window |
|---------|---------|------|-------|----:|---------:|-----------:|-------------:|
| alice | `0xaaa` | self-built | ETH | 100 | 2 | 4 | 3 |
| carol | `0xccc` | self-built | ETH | 100 | 2 | 4 | 3 |
| bob | `0xbbb` | custodian | USDT | 50 | 2 | 4 | 3 |

ETH exposure cap: 5000, high-value tier: 2000. USDT: uncapped.
Poll cadences: scanner 500 ms, worker 2 s, reconciliation 10 s.
"Wait a moment" below means ~2 s unless stated otherwise.

Useful reads:

```bash
curl -s localhost:9200/v1/deposits/<transferID>     # one deposit
curl -s "localhost:9200/v1/deposits?account=alice"  # all of an account's
curl -s localhost:9200/v1/balances/alice            # balance, held, flagged
docker compose exec -T postgres psql -U postgres -d deposit_crediting \
  -c "SELECT type, amount, ref FROM ledger_entries ORDER BY id;"  # the ledger
```

A deposit's transfer ID is `<chain>:<txHash>:native` for native transfers.

---

## Scenario 1 — Self-built deposit is credited, then finalized

*Requirement: the platform credits deposits it observes on-chain; funds
become final after sufficient depth.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs1","to":"0xaaa","asset":"ETH","amount":"1000"}]}'
sleep 2
curl -s localhost:9200/v1/deposits/stubchain:0xs1:native
```

**Expected:** deposit exists, `state: "PENDING"`, amount 1000, no ledger
entry yet, balance 0. Then:

```bash
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'   # depth 2 = n_credit
sleep 2
curl -s localhost:9200/v1/deposits/stubchain:0xs1:native
curl -s localhost:9200/v1/balances/alice
```

**Expected:** `state: "CREDITED"`, `held: false`, alice's ETH balance is
1000, exactly one `credit` ledger entry with ref `stubchain:0xs1:native`.

```bash
curl -s -X POST localhost:9100/admin/mine -d '{"count":2}'   # depth 4 = n_finalize
sleep 2
```

**Expected:** `state: "FINALIZED"`.

## Scenario 2 — Re-scanning never double-credits

*Requirement: no duplicate credits, even if the same transfer is
processed more than once.*

Continuing from Scenario 1, restart the scanner (Ctrl-C, start it again)
and wait a moment.

**Expected:** alice's balance is unchanged, and the ledger still shows
exactly one credit for `0xs1`. The scanner resumes from its durable
cursor; replays are absorbed by the unique transfer ID.

## Scenario 3 — Dust deposits are never credited

*Requirement: deposits below the crediting minimum are recorded but not
credited (frontend/UI is not the enforcement boundary).*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs3","to":"0xccc","asset":"ETH","amount":"5"}]}'
sleep 2
curl -s localhost:9200/v1/deposits/stubchain:0xs3:native
```

**Expected:** `state: "BELOW_MINIMUM"` (terminal), carol's balance stays
0, no ledger entry — even after mining more blocks.

## Scenario 4 — Unsupported assets are ignored

*Requirement: only configured (chain, asset) pairs are credited.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs4","to":"0xaaa","asset":"DOGE","amount":"9999"}]}'
sleep 2
curl -s "localhost:9200/v1/deposits?account=alice"
```

**Expected:** no deposit for `0xs4` exists at all; balances unchanged.

## Scenario 5 — A reorged-out deposit is never credited

*Requirement: no credit survives a reorg.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs5","to":"0xaaa","asset":"ETH","amount":"300"}]}'
sleep 2                                                   # PENDING, depth 1
curl -s -X POST localhost:9100/admin/reorg -d '{"depth":1}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":5}' # new branch, no 0xs5
sleep 3
curl -s localhost:9200/v1/deposits/stubchain:0xs5:native
```

**Expected:** state goes `REORGED` → `DROPPED` once the reorg window (3
blocks) passes. No ledger entry, alice's balance untouched.

## Scenario 6 — Reorg after credit reverses the funds

*Requirement: a credited deposit that is reorged out is reversed.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs6","to":"0xaaa","asset":"ETH","amount":"400"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'   # credited at depth 2
sleep 2
curl -s -X POST localhost:9100/admin/reorg -d '{"depth":2}'  # orphan it
curl -s -X POST localhost:9100/admin/mine -d '{"count":5}'
sleep 3
curl -s localhost:9200/v1/deposits/stubchain:0xs6:native
curl -s localhost:9200/v1/balances/alice
```

**Expected:** `REORGED` → `REVERSED`; alice's ETH balance returns to
what it was before this deposit (the credit is backed out by a
`reversal` ledger entry).

## Scenario 7 — Reversal after the user spent the funds

*Requirement: reversal still happens when the balance was already
spent — the account goes negative and is flagged.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs7","to":"0xccc","asset":"ETH","amount":"1000"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'
sleep 2                                                     # carol credited 1000
curl -s -X POST localhost:9200/v1/debits \
  -d '{"account":"carol","asset":"ETH","amount":"700","ref":"withdrawal:s7"}'
curl -s -X POST localhost:9100/admin/reorg -d '{"depth":2}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":5}'
sleep 3
curl -s localhost:9200/v1/balances/carol
curl -s -X POST localhost:9200/v1/debits \
  -d '{"account":"carol","asset":"ETH","amount":"1","ref":"withdrawal:s7b"}'
```

**Expected:** deposit ends `REVERSED`; carol's ETH balance is **−700**
and `flagged: true`; the follow-up debit is rejected
(`409 account flagged`). The ledger shows credit, debit, reversal.

## Scenario 8 — Re-inclusion after a reorg keeps exactly one credit

*Requirement: a transfer orphaned and then re-included on the new branch
is credited once, not twice.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs8","to":"0xaaa","asset":"ETH","amount":"500"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'
sleep 2                                                     # credited
curl -s -X POST localhost:9100/admin/reorg -d '{"depth":2}'
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs8","to":"0xaaa","asset":"ETH","amount":"500"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":3}'
sleep 3
curl -s localhost:9200/v1/deposits/stubchain:0xs8:native
curl -s localhost:9200/v1/balances/alice
```

**Expected:** the deposit passes through `REORGED` back to
`PENDING`/`CREDITED`; alice's balance reflects **one** 500 credit; the
ledger has exactly one credit for `0xs8`.

## Scenario 9 — Custodian deposit: webhook is a hint, the chain decides

*Requirement: custodian-mode deposits are credited only after on-chain
verification (trust but verify).*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs9","to":"0xbbb","asset":"USDT","amount":"500"}]}'
curl -s -X POST localhost:9300/admin/deposits -d '{"providerEventId":"evt-s9",
  "chain":"stubchain","txHash":"0xs9","to":"0xbbb","asset":"USDT","amount":"500"}'
curl -s localhost:9300/admin/webhooks        # drain the delivery
curl -s -X POST localhost:9200/v1/webhooks/custodian -d '{"providerEventId":"evt-s9",
  "chain":"stubchain","txHash":"0xs9","to":"0xbbb","asset":"USDT","amount":"500"}'
sleep 2
curl -s localhost:9200/v1/deposits/stubchain:0xs9:native
```

**Expected:** webhook acknowledged; deposit exists in `PENDING`
(mode `custodian`). After `POST /admin/mine {"count":2}` and a moment:
`CREDITED`, bob's USDT balance 500 — driven by the re-checker, not the
webhook.

## Scenario 10 — Duplicated webhooks credit once

*Requirement: custodian webhooks may be duplicated; exactly one credit.*

Repeat Scenario 9 with `"faults":["duplicate"]` on
`POST /admin/deposits`; `GET /admin/webhooks` returns **two**
deliveries. POST both to the webhook endpoint.

**Expected:** both deliveries acknowledged, one deposit row, one credit
ledger entry, bob's balance increases by 500 — not 1000.

## Scenario 11 — A webhook for a transaction that does not exist

*Requirement: claims are never credited on the custodian's word alone.*

```bash
curl -s -X POST localhost:9200/v1/webhooks/custodian -d '{"providerEventId":"evt-s11",
  "chain":"stubchain","txHash":"0xphantom","to":"0xbbb","asset":"USDT","amount":"1000000"}'
sleep 12    # past a reconciliation poll
curl -s "localhost:9200/v1/deposits?account=bob"
```

**Expected:** response `202 not_on_chain`; no deposit for `0xphantom` is
ever created; bob's balance unchanged. (Acknowledgement codes describe
delivery, not crediting — the deposit read is the verdict.)

## Scenario 12 — A webhook that contradicts the chain

*Requirement: mismatched claims are rejected.*

Mine a block with a real 500-USDT transfer (`0xs12` to `0xbbb`), then
POST a webhook claiming the same tx but amount **5000**.

**Expected:** no deposit is created; bob's balance unchanged. The claim
disagrees with chain facts and is dropped (see the server log warning).

## Scenario 13 — A dropped webhook is recovered by reconciliation

*Requirement: missed webhooks are recovered by polling the custodian
query API.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs13","to":"0xbbb","asset":"USDT","amount":"250"}]}'
curl -s -X POST localhost:9300/admin/deposits -d '{"providerEventId":"evt-s13",
  "chain":"stubchain","txHash":"0xs13","to":"0xbbb","asset":"USDT","amount":"250",
  "faults":["dropped"]}'
curl -s localhost:9300/admin/webhooks   # 0 deliveries — it never arrives
sleep 12                                 # one reconciliation poll
curl -s localhost:9200/v1/deposits/stubchain:0xs13:native
```

**Expected:** despite zero webhook deliveries, the deposit appears
(`PENDING`) within one poll interval and credits normally after depth.

## Scenario 14 — Vault discrepancy is surfaced

*Requirement: the custodian's vault totals are reconciled against our
ledger; discrepancies alert.*

```bash
curl -s -X POST localhost:9300/admin/vaults \
  -d '{"chain":"stubchain","asset":"USDT","total":"1"}'
sleep 12
```

**Expected:** the worker log prints a solvency alert (vault 1 vs a much
larger ledger total from earlier scenarios). Alerting only — crediting
is unaffected.

## Scenario 15 — Exposure cap holds spendability, not crediting

*Requirement: aggregate unfinalized exposure is capped; on breach new
high-value credits post to the ledger but are not spendable.*

ETH cap is 5000 with a 2000 tier. Create exposure with two large
unfinalized credits:

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs15a","to":"0xaaa","asset":"ETH","amount":"3000"},
  {"kind":"native","txHash":"0xs15b","to":"0xccc","asset":"ETH","amount":"3000"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'
sleep 3
curl -s localhost:9200/v1/balances/carol
```

**Expected:** both credits post (balances show 3000), but once exposure
exceeds the cap the second deposit shows `held: true` and carol's
`held` amount is 3000 — a debit against carol is rejected with 409
while the balance itself stands. Mining to finality drains exposure;
the monitor releases the hold (`held` returns to 0, debits work again).
Watch the worker log for the critical exposure alert.

## Scenario 16 — The system is quiet when nothing is wrong

*Requirement (verification strategy): runtime invariant monitors guard
production the way test assertions guard the harness.*

After running Scenarios 1–15, inspect the worker log.

**Expected:** no `INVARIANT VIOLATION` lines for
`credited_without_entry` or `ledger_balance_mismatch`, and no
`reorg_resolution_lag` older than the scenario you just ran (reorged
deposits resolve within 2× the reorg window). Exposure/solvency alerts
from Scenarios 14–15 are expected — those are risk signals, not
invariant violations.

---

## Cleanup

```bash
# stop the five processes (Ctrl-C in each terminal), then:
docker compose down          # keep the data volume
docker compose down -v       # or wipe everything
```
