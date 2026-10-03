//go:build integration

// Zero-downtime release rehearsal (P5): a full expand-and-contract
// migration (add column → dual-tolerant traffic → backfill → drop) while
// an "old replica" — the current ent code, oblivious to the rehearsal
// columns — serves writes without interruption.
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/domain/deposit"
	"deposit-crediting/internal/errs"
	"deposit-crediting/internal/store"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
	"deposit-crediting/internal/store/storetest"
)

const rehearsalDSN = "postgres://postgres:postgres@localhost:5432/deposit_crediting?sslmode=disable&connect_timeout=2"

func TestExpandContractMigrationUnderTraffic(t *testing.T) {
	client := storetest.OpenDB(t)
	ctx := context.Background()
	engine := credit.NewEngine(store.New(client))

	state, _, err := deposit.Transition(deposit.StateNone, deposit.EventObserved)
	if err != nil {
		t.Fatalf("Transition(OBSERVED): %v", err)
	}
	if _, err := client.Deposit.Create().
		SetTransferID("evm:0xmig:native").
		SetChain("evm").SetAsset("ETH").SetAccount("alice").SetAddress("0xdeposit").
		SetAmount("1000000").SetMode(entdeposit.ModeSelfBuilt).SetState(entdeposit.State(state)).
		SetBlockHeight(1).SetBlockHash("0xb1").
		Save(ctx); err != nil {
		t.Fatalf("open deposit: %v", err)
	}
	if err := engine.Apply(ctx, "evm:0xmig:native", deposit.EventDepthReached); err != nil {
		t.Fatalf("credit: %v", err)
	}

	raw, err := sql.Open("postgres", rehearsalDSN)
	if err != nil {
		t.Fatalf("raw connection: %v", err)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := raw.ExecContext(ctx, query); err != nil {
			t.Fatalf("migration step %q: %v", query, err)
		}
	}
	t.Cleanup(func() {
		defer raw.Close()
		if _, err := raw.Exec("ALTER TABLE account_balances DROP COLUMN IF EXISTS memo_old"); err != nil {
			t.Errorf("cleanup memo_old: %v", err)
		}
		if _, err := raw.Exec("ALTER TABLE account_balances DROP COLUMN IF EXISTS memo_new"); err != nil {
			t.Errorf("cleanup memo_new: %v", err)
		}
	})

	// Current production shape: the legacy column exists.
	exec("ALTER TABLE account_balances ADD COLUMN memo_old text")

	// Old-replica traffic: debits keep flowing through every migration
	// step. Any non-funds error fails the rehearsal.
	stop := make(chan struct{})
	trafficErr := make(chan error, 1)
	go func() {
		defer close(trafficErr)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			err := engine.Debit(ctx, "alice", "ETH", big.NewInt(1), fmt.Sprintf("mig-w:%d", i))
			if err != nil && !errors.Is(err, errs.ErrInsufficientFunds) {
				trafficErr <- fmt.Errorf("traffic debit %d: %w", i, err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Step 1, expand: add the new column. Nullable with a constant
	// default is a metadata-only change in Postgres 11+.
	exec("ALTER TABLE account_balances ADD COLUMN memo_new text DEFAULT 'unset'")

	// Step 2, dual-tolerant traffic: v2 replicas write both columns.
	exec("UPDATE account_balances SET memo_old = 'legacy', memo_new = 'v2'")

	// Step 3, backfill rows written before v2 deployed.
	exec("UPDATE account_balances SET memo_new = memo_old WHERE memo_new = 'unset'")

	var unfilled int
	if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_balances WHERE memo_new IS NULL OR memo_new = 'unset'").Scan(&unfilled); err != nil {
		t.Fatalf("backfill check: %v", err)
	}
	if unfilled != 0 {
		t.Fatalf("backfill incomplete: %d rows unset", unfilled)
	}

	// Step 4, contract: the legacy column goes away; old columns are only
	// dropped after no replica reads them.
	exec("ALTER TABLE account_balances DROP COLUMN memo_old")

	close(stop)
	if err := <-trafficErr; err != nil {
		t.Fatalf("traffic interrupted during migration: %v", err)
	}

	// The system still serves after the contract step.
	if err := engine.Debit(ctx, "alice", "ETH", big.NewInt(1), "mig-final"); err != nil {
		t.Fatalf("post-contract debit: %v", err)
	}
	exec("ALTER TABLE account_balances DROP COLUMN memo_new")
}
