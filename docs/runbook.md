# Operations Runbook

Alert sources: the risk monitor (`internal/risk`), invariant checker, solvency checker, and scanner/re-checker/rechecker error logs. Every entry below names the log signal, its meaning, and the action.

## Alerts

| Signal                                        | Meaning                                                                                                                                        | Action                                                                                                                                                                                                                                          |
|-----------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `exposure alert` level=critical               | Unfinalized spendable exposure ≥ E_max for (chain, asset); spendability holds active — new credits post but are not spendable (risk-policy §4) | Expected self-protection. Confirm finalization is progressing (exposure drains). If it stays at cap, check scanner/re-checker health and consider raising N_finalize or the cap via config change. No ledger action needed — credits still post |
| `exposure alert` level=warn                   | Exposure ≥ 80% of cap                                                                                                                          | Watch finalization rate vs deposit rate; prepare for holds                                                                                                                                                                                      |
| `vault solvency discrepancy`                  | Custodian ledger total exceeds vault total for an asset (ADR 0004)                                                                             | Page immediately. Freeze new address issuance for that asset, reconcile custodian statements, treat as potential custodian insolvency                                                                                                           |
| `credited_without_entry`                      | A deposit is CREDITED/FINALIZED/REVERSED with no credit ledger entry — a missed credit (primary correctness invariant)                         | Page. Inspect the deposit row and engine logs at credit time; the ledger must be repaired manually after root cause                                                                                                                             |
| `ledger_balance_mismatch`                     | Ledger replay ≠ balance projection for an (account, asset)                                                                                     | Page. The projection is rebuildable: recompute from `ledger_entries` and correct `account_balances` in one transaction; investigate the write path that diverged                                                                                |
| `reorg_resolution_lag`                        | Deposit still REORGED long past its reorg window                                                                                               | Scanner/re-checker stuck. Check its error log; on `ErrChainInconsistent` see cursor repair below                                                                                                                                                |
| `scanner tick failed` repeating               | Node unreachable, DB error, or (rarely) chain inconsistency                                                                                    | Transient node errors self-heal. Persistent: check node RPC health, then DB connectivity                                                                                                                                                        |
| `custodian claim contradicts chain, rejected` | Webhook/API claim does not match chain facts                                                                                                   | Normal in small numbers (provider quirks). A sustained rate means custodian data corruption — escalate to the provider                                                                                                                          |
| account flagged (balance negative)            | A reversal exceeded the remaining balance (ADR 0002)                                                                                           | Expected after reorg-after-spend. Debits are blocked automatically. Ops: contact the user for top-up; the account unflags when the balance is non-negative again                                                                                |

## Scanner cursor repair (ErrChainInconsistent)

The scanner stops (does not retry) when chain data contradicts the recorded cursor — e.g. a reorg deeper than the retained block log, or a cursor edited by hand.

1. Confirm the chain is healthy and the scanner's node is on the canonical fork.
2. Find a safe height: any height whose hash both sides agree on — e.g. `head - 2 * reorg_window`.
3. Update `chain_cursors` to that height/hash and delete `canonical_blocks` above it.
4. Restart the scanner. Deposits in the re-scanned range are deduplicated by transfer ID; REORGED deposits re-include or expire through the normal machine.

## Restart procedures

- **Scanner**: safe to restart any time; it replays from the durable cursor idempotently.
- **Re-checker / reconciler / risk monitor**: stateless except the reconciler's in-memory poll cursor — a restart triggers one full (idempotent) backfill poll.
- **Engine/API**: stateless; in-flight transactions roll back and are redelivered idempotently by the ingest layer.

## Zero-downtime releases (expand-and-contract)

1. **Expand**: additive migration only (new nullable columns/tables, new enum values appended). Never rename or drop in this step.
2. Deploy new code tolerant of old and new shapes.
3. **Migrate**: backfill existing rows (batched, throttled).
4. **Contract**: drop old columns/enum values only after all old replicas are gone.

The rehearsal test `TestExpandContractMigrationUnderTraffic` (`internal/store/migration_integration_test.go`) runs this sequence against live traffic; run it before any non-trivial migration.

## Configuration changes

The prototype reads `asset_configs` per tick, so changing `n_credit`,
`n_finalize`, `reorg_window`, `exposure_cap`, or `tier_amount` takes effect
without a process restart. This is not the production policy model: production
must create an immutable policy revision, pin that revision on each new deposit,
and activate it with an expand-and-contract rollout. Lowering a threshold or cap
then affects only deposits pinned to the new revision; existing deposits retain
the rules under which they were opened.
