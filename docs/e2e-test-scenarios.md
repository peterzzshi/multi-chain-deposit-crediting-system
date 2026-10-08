# End-to-End Test Scenarios

Manual checks you can run locally against stub APIs. Each scenario derives
from a requirement in `requirements.md` — not from the implementation — and
states the exact steps and the expected outcome.

## Local topology

```
 curl (you)                curl (you = webhook channel)
    │                            │
    ▼                            ▼
 chain-node :9100 ──► scanner ──►│
 chain-node-fast:9101 (self-built│   server :9200 ──► PostgreSQL :5432
 (stub chain nodes)   ingest)    │   (webhooks, debits, reads)
    ▲                            │        ▲
    └──────── worker ◄───────────┘        │
 (re-checker, risk,      custodian-api :9300
  invariants,            (stub custodian:
  reconciliation) ◄────── query API + vaults)
```

- **chain-node** is a stand-in chain node, one process per network
  (`stubchain` on :9100, `fastchain` on :9101 — each instance only knows its
  own chain). Blocks are mined only when you say so (`POST /admin/blocks`,
  `/admin/mine`), and reorgs happen only when you trigger them
  (`POST /admin/reorg`) — every scenario is deterministic.
- **custodian-api** is a stand-in custodian. Its query API always tells the
  truth; the webhook channel is *you*: `GET /admin/webhooks` drains pending
  deliveries, and you POST them to the app's webhook endpoint. Faults
  (duplicate/delayed/dropped) are injected at observation time.
- The app (scanner, server, worker) treats both stubs exactly like real
  integrations — over HTTP, behind the same interfaces a real adapter would
  implement.

## Setup

The quickest path is the full Compose stack — it already runs two chain-node
instances (one per network), the custodian stub, PostgreSQL, and the three
app services:

```bash
docker compose up --build
```

Then seed the sample addresses and jump to the scenarios below:

```bash
docker compose exec -T postgres psql -U postgres -d deposit_crediting < scripts/seed.sql
```

Optional clean slate between runs:

```bash
docker compose exec -T postgres psql -U postgres -d deposit_crediting -c \
  "TRUNCATE ledger_entries, account_balances, deposits, source_events, canonical_blocks, chain_cursors, exposure_states;"
```

**To iterate on code without rebuilding images**, run Postgres and the stubs
through Compose, then run the Go services directly against the stubs'
published ports via `configs/chains.local.json`:

```bash
docker compose up -d postgres chain-node chain-node-fast custodian-api migrate

DATABASE_URL=postgres://postgres:postgres@localhost:5432/deposit_crediting?sslmode=disable \
CHAINS_CONFIG=configs/chains.local.json ASSETS_CONFIG=configs/assets.json \
  START_HEIGHT=0 MAX_BATCH=100 go run ./cmd/scanner

DATABASE_URL=postgres://postgres:postgres@localhost:5432/deposit_crediting?sslmode=disable \
CHAINS_CONFIG=configs/chains.local.json ASSETS_CONFIG=configs/assets.json \
  LISTEN_ADDR=:9200 PROVIDER=custodianA go run ./cmd/server

DATABASE_URL=postgres://postgres:postgres@localhost:5432/deposit_crediting?sslmode=disable \
CHAINS_CONFIG=configs/chains.local.json ASSETS_CONFIG=configs/assets.json \
CUSTODIAN_API_URL=http://localhost:9300 PROVIDER=custodianA \
  RECONCILE_INTERVAL_MS=30000 RECONCILE_OVERLAP_MS=5000 go run ./cmd/worker
```

Supported assets live in `configs/assets.json` (validated and upserted by
every binary at startup; edit the file and restart a binary to change
policy). Seeded addresses come from `scripts/seed.sql`:

| Account | Chain     | Address | Assets     | Notes                       |
|---------|-----------|---------|------------|-----------------------------|
| alice   | stubchain | `0xaaa` | ETH        | Self-built mode             |
| carol   | stubchain | `0xccc` | ETH        | Self-built mode             |
| bob     | stubchain | `0xbbb` | USDT, USDC | Custodian mode              |
| dave    | stubchain | `0xddd` | WBTC       | Self-built mode, 8 decimals |
| eve     | fastchain | `0xeee` | SOL        | Self-built mode, fast chain |

**Asset configurations:**
- **ETH** (stubchain): min 100, n_credit 2, n_finalize 4, exposure cap 5000, tier 2000, reorg window 3
- **USDT** (stubchain): min 50, n_credit 2, n_finalize 4, custodian mode, reorg window 3
- **USDC** (stubchain): min 100, n_credit 2, n_finalize 4, custodian mode, cap 10000, tier 5000, reorg window 3
- **WBTC** (stubchain): min 10000 (8 decimals), n_credit 3, n_finalize 6, cap 50M, tier 20M, reorg window 5
- **SOL** (fastchain): min 1000 (9 decimals), n_credit 10, n_finalize 32, cap 100000, tier 50000, reorg window 15
- **USDC** (fastchain): min 100, n_credit 10, n_finalize 32, custodian mode, cap 20000, tier 10000, reorg window 15

**Chain characteristics:**
- **stubchain**: ~12s block time, typical reorg depth 1-3 blocks
- **fastchain**: ~2s block time, reorgs more frequently to greater depth (5-10 blocks typical)

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

## Scenario 17 — Finalization advances state after sufficient depth

*Requirement: credited deposits become finalized after N_finalize confirmations.*

Continuing from Scenario 1 (which left a deposit at `CREDITED` after depth 4):

```bash
curl -s localhost:9200/v1/deposits/stubchain:0xs1:native
```

**Expected:** `state: "FINALIZED"`, deposit is now immutable (no reorg
window applies).

## Scenario 18 — High-value deposit waits 2×N_credit before credit

*Requirement: deposits above tier_amount threshold require 2×N_credit
confirmations.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs18","to":"0xaaa","asset":"ETH","amount":"3000"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'   # depth 2 = normal n_credit
sleep 2
curl -s localhost:9200/v1/deposits/stubchain:0xs18:native
```

**Expected:** `state: "PENDING"` (not yet credited). Amount 3000 exceeds
tier_amount 2000, so requires depth 4 (2×2). After `POST /admin/mine
{"count":2}` and waiting: `CREDITED`.

## Scenario 19 — Token transfer (ERC20/SPL) is credited

*Requirement: token deposits follow the same crediting rules as native.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"erc20","txHash":"0xs19","to":"0xbbb","asset":"USDT","amount":"500","contractAddress":"0xusdt"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'
sleep 2
curl -s localhost:9200/v1/deposits/stubchain:0xs19:erc20:0xusdt
```

**Expected:** deposit exists, transfer ID includes contract address
(`stubchain:0xs19:erc20:0xusdt`), `CREDITED` after n_credit depth, bob's
USDT balance increases by 500.

## Scenario 20 — WBTC deposit with 8 decimals

*Requirement: higher-precision assets credit correctly.*

Add seed address for dave:

```bash
docker compose exec -T postgres psql -U postgres -d deposit_crediting -c \
  "INSERT INTO deposit_addresses (account, chain, address, active) VALUES ('dave', 'stubchain', '0xddd', true) ON CONFLICT DO NOTHING;"
```

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"erc20","txHash":"0xs20","to":"0xddd","asset":"WBTC","amount":"50000","contractAddress":"0xwbtc"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":2}'   # n_credit=3 for WBTC
sleep 2
curl -s localhost:9200/v1/deposits/stubchain:0xs20:erc20:0xwbtc
```

**Expected:** amount 50000 is above min (10000), `CREDITED` after depth 3,
dave's WBTC balance reflects 50000 (0.0005 BTC in human terms).

## Scenario 21 — Fastchain deposit with different thresholds

*Requirement: fast chains use different N_credit/N_finalize values.*

Ensure the fastchain chain-node (`chain-node-fast`) is running on port 9101.

```bash
curl -s -X POST localhost:9101/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs21","to":"0xeee","asset":"SOL","amount":"5000"}]}'
curl -s -X POST localhost:9101/admin/mine -d '{"count":9}'   # n_credit=10 for SOL
sleep 2
curl -s localhost:9200/v1/deposits/fastchain:0xs21:native
```

**Expected:** `state: "PENDING"` after 9 blocks (depth < 10). After one
more block: `CREDITED`. After mining to depth 32: `FINALIZED`.

## Scenario 21b — Fastchain reorgs more frequently and deeper

*Requirement: fast chains experience reorgs to greater depth.*

```bash
curl -s -X POST localhost:9101/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs21b","to":"0xeee","asset":"SOL","amount":"6000"}]}'
curl -s -X POST localhost:9101/admin/mine -d '{"count":10}'  # credited at depth 11
sleep 2
curl -s localhost:9200/v1/deposits/fastchain:0xs21b:native   # CREDITED
curl -s -X POST localhost:9101/admin/reorg -d '{"depth":8}'  # deeper reorg than EVM typical
curl -s -X POST localhost:9101/admin/mine -d '{"count":20}'
sleep 3
curl -s localhost:9200/v1/deposits/fastchain:0xs21b:native
```

**Expected:** deposit transitions `REORGED` → `REVERSED` correctly. The
reorg window for fastchain is 15 blocks (vs 3 for stubchain), reflecting
its higher reorg frequency. The system handles depth-8 reorgs (vs typical
depth-2 on stubchain) without issue.

## Scenario 22 — Exposure hold released after finalization

*Requirement: finalized credits no longer count toward exposure cap.*

Continuing from Scenario 15 (ETH exposure breach with held deposits):

```bash
curl -s -X POST localhost:9100/admin/mine -d '{"count":2}'   # finalize one deposit
sleep 3
curl -s localhost:9200/v1/balances/carol
```

**Expected:** as deposits finalize, aggregate exposure drops below cap;
worker releases holds (`held` amount decreases), debits become allowed
again.

## Scenario 23 — Out-of-order webhook delivery

*Requirement: webhook ordering does not affect correctness.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs23a","to":"0xbbb","asset":"USDT","amount":"100"},
  {"kind":"native","txHash":"0xs23b","to":"0xbbb","asset":"USDT","amount":"200"}]}'
curl -s -X POST localhost:9300/admin/deposits -d '{"providerEventId":"evt-s23b",
  "chain":"stubchain","txHash":"0xs23b","to":"0xbbb","asset":"USDT","amount":"200"}'
curl -s -X POST localhost:9300/admin/deposits -d '{"providerEventId":"evt-s23a",
  "chain":"stubchain","txHash":"0xs23a","to":"0xbbb","asset":"USDT","amount":"100"}'
curl -s localhost:9300/admin/webhooks   # drain both (second posted first)
```

POST webhooks in arrival order (s23b before s23a), then:

```bash
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'
sleep 2
curl -s "localhost:9200/v1/deposits?account=bob"
```

**Expected:** both deposits exist and credit correctly despite reversed
webhook order. Chain verifier is authoritative; webhook order is irrelevant.

## Scenario 24 — Delayed webhook after chain verifier

*Requirement: late webhooks are absorbed without duplication.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs24","to":"0xbbb","asset":"USDT","amount":"300"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'
sleep 12   # reconciliation detects it, credits happen
curl -s -X POST localhost:9300/admin/deposits -d '{"providerEventId":"evt-s24",
  "chain":"stubchain","txHash":"0xs24","to":"0xbbb","asset":"USDT","amount":"300"}'
curl -s localhost:9300/admin/webhooks
curl -s -X POST localhost:9200/v1/webhooks/custodian -d '{"providerEventId":"evt-s24",
  "chain":"stubchain","txHash":"0xs24","to":"0xbbb","asset":"USDT","amount":"300"}'
sleep 2
```

**Expected:** deposit already exists and is `CREDITED`; late webhook
acknowledged but produces no duplicate credit or state change.

## Scenario 25 — Multiple deposits same account same block

*Requirement: concurrent deposits to one account all credit.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs25a","to":"0xaaa","asset":"ETH","amount":"200"},
  {"kind":"native","txHash":"0xs25b","to":"0xaaa","asset":"ETH","amount":"300"},
  {"kind":"native","txHash":"0xs25c","to":"0xaaa","asset":"ETH","amount":"400"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'
sleep 2
curl -s "localhost:9200/v1/deposits?account=alice"
curl -s localhost:9200/v1/balances/alice
```

**Expected:** 3 separate deposits, all `CREDITED`, alice's balance
increases by 900 total. Ledger shows 3 distinct credit entries.

## Scenario 26 — Credit and debit racing on same account

*Requirement: optimistic concurrency prevents lost updates.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs26","to":"0xccc","asset":"ETH","amount":"1000"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'
sleep 2   # carol credited 1000
```

In rapid succession (two terminals or scripts):

```bash
curl -s -X POST localhost:9200/v1/debits -d '{"account":"carol","asset":"ETH","amount":"400","ref":"w1"}' &
curl -s -X POST localhost:9200/v1/debits -d '{"account":"carol","asset":"ETH","amount":"300","ref":"w2"}' &
wait
curl -s localhost:9200/v1/balances/carol
```

**Expected:** both debits succeed (balance 1000 - 400 - 300 = 300) or one
retries after version conflict. Ledger shows credit + 2 debits, balance
consistent. No lost update.

## Scenario 27 — Scanner restart mid-reorg

*Requirement: scanner resumes correctly even if stopped during reorg.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs27","to":"0xaaa","asset":"ETH","amount":"600"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":1}'
sleep 2   # credited
curl -s -X POST localhost:9100/admin/reorg -d '{"depth":2}'
```

Kill the scanner (Ctrl-C), wait 2 seconds, restart it. Then:

```bash
curl -s -X POST localhost:9100/admin/mine -d '{"count":5}'
sleep 3
curl -s localhost:9200/v1/deposits/stubchain:0xs27:native
```

**Expected:** deposit transitions `REORGED` → `REVERSED` correctly despite
scanner restart. Cursor recovery handles the gap; no duplicate or missed
state transitions.

## Scenario 28 — Worker restart with pending reconciliation

*Requirement: worker loops are idempotent; restart is safe.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs28","to":"0xbbb","asset":"USDT","amount":"250"}]}'
curl -s -X POST localhost:9300/admin/deposits -d '{"providerEventId":"evt-s28",
  "chain":"stubchain","txHash":"0xs28","to":"0xbbb","asset":"USDT","amount":"250",
  "faults":["dropped"]}'
sleep 5   # reconciliation poll in progress
```

Kill the worker (Ctrl-C), restart it, wait 12 seconds:

```bash
curl -s localhost:9200/v1/deposits/stubchain:0xs28:native
```

**Expected:** deposit appears and credits normally. Reconciliation resumes
from query API; no duplicate claims or missed deposits.

## Scenario 29 — Deep reorg beyond retained window

*Requirement: cursor repair handles reorgs deeper than typical retention.*

```bash
curl -s -X POST localhost:9100/admin/blocks -d '{"transfers":[
  {"kind":"native","txHash":"0xs29","to":"0xaaa","asset":"ETH","amount":"700"}]}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":10}'
sleep 2   # finalized
curl -s -X POST localhost:9100/admin/reorg -d '{"depth":12}'
curl -s -X POST localhost:9100/admin/mine -d '{"count":15}'
sleep 3
```

**Expected:** scanner log shows cursor repair (walking back to find common
ancestor), deposit transitions `REORGED` → `DROPPED` (beyond window), no
crash. If the new branch re-includes `0xs29`: `PENDING` → `CREDITED` again.

---

## Cleanup

```bash
docker compose down          # keep the data volume
docker compose down -v       # or wipe everything
```
