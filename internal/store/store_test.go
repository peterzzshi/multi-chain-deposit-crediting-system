//go:build integration

// Integration tests against dockerized Postgres: they verify SQL,
// constraints, and locking, which mocks cannot. Run with `make itest`.
package store_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"

	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"
	"deposit-crediting/internal/store"
	"deposit-crediting/internal/store/ent"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
)

// openDeposit simulates the ingest layer: insert the row with the initial
// state computed by the machine.
func openDeposit(t *testing.T, client *ent.Client, transferID string, amount string) {
	t.Helper()
	state, _, err := domain.Transition(domain.StateNone, domain.EventObserved)
	if err != nil {
		t.Fatalf("Transition(OBSERVED) unexpected error: %v", err)
	}
	_, err = client.Deposit.Create().
		SetTransferID(transferID).
		SetChain("evm").
		SetAsset("ETH").
		SetAccount("alice").
		SetAddress("0xdeposit").
		SetAmount(amount).
		SetMode(entdomain.ModeSelfBuilt).
		SetState(entdomain.State(state)).
		Save(context.Background())
	if err != nil {
		t.Fatalf("open deposit: %v", err)
	}
}

func TestEngineAgainstPostgres(t *testing.T) {
	client := store.OpenDB(t)
	ctx := context.Background()
	engine := credit.NewEngine(store.New(client))

	const transferID = "evm:0xabc:native"
	openDeposit(t, client, transferID, "100")

	if err := engine.Apply(ctx, transferID, domain.EventDepthReached); err != nil {
		t.Fatalf("Apply(DEPTH_REACHED) unexpected error: %v", err)
	}
	row, err := client.Deposit.Query().Where(entdomain.TransferID(transferID)).Only(ctx)
	if err != nil {
		t.Fatalf("query deposit: %v", err)
	}
	if got, want := domain.State(row.State), domain.StateCredited; got != want {
		t.Errorf("state = %s; want %s", got, want)
	}
	n, err := client.LedgerEntry.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if got, want := n, 1; got != want {
		t.Errorf("ledger entries = %d; want %d", got, want)
	}

	// Redelivery is a no-op.
	if err := engine.Apply(ctx, transferID, domain.EventDepthReached); err != nil {
		t.Fatalf("Apply(DEPTH_REACHED) redelivery unexpected error: %v", err)
	}
	if n, err := client.LedgerEntry.Query().Count(ctx); err != nil || n != 1 {
		t.Errorf("ledger entries after redelivery = %d, %v; want 1, nil", n, err)
	}

	// ADR 0002: debit 70, then a deep reorg reverses the credit.
	if err := engine.Debit(ctx, "alice", "ETH", big.NewInt(70), "withdrawal:1"); err != nil {
		t.Fatalf("Debit() unexpected error: %v", err)
	}
	if err := engine.Apply(ctx, transferID, domain.EventReorgedOut); err != nil {
		t.Fatalf("Apply(REORGED_OUT) unexpected error: %v", err)
	}
	if err := engine.Apply(ctx, transferID, domain.EventWindowExpiredCredited); err != nil {
		t.Fatalf("Apply(WINDOW_EXPIRED_CREDITED) unexpected error: %v", err)
	}

	bal, err := client.AccountBalance.Query().Only(ctx)
	if err != nil {
		t.Fatalf("query balance: %v", err)
	}
	if got, want := bal.Balance, "-70"; got != want {
		t.Errorf("balance = %s; want %s", got, want)
	}
	if !bal.Flagged {
		t.Error("flagged = false; want true after negative reversal")
	}

	types := make(map[domain.TransactionType]int)
	entries, err := client.LedgerEntry.Query().All(ctx)
	if err != nil {
		t.Fatalf("query entries: %v", err)
	}
	for _, e := range entries {
		types[domain.TransactionType(e.Type)]++
	}
	if types[domain.Credit] != 1 || types[domain.Debit] != 1 || types[domain.Reversal] != 1 {
		t.Errorf("entry types = %v; want one credit, one debit, one reversal", types)
	}

	if err := engine.Debit(ctx, "alice", "ETH", big.NewInt(1), "withdrawal:2"); err == nil {
		t.Error("Debit() on flagged account = nil error; want ErrAccountFlagged")
	}
}

// TestConcurrentCreditsAndDebits races duplicate credits against a burst
// of debits on one account. Invariants: exactly one credit entry, ledger
// and balance agree, and the balance never goes negative through debits
// (ADR 0003). Run with -race.
func TestConcurrentCreditsAndDebits(t *testing.T) {
	client := store.OpenDB(t)
	ctx := context.Background()
	engine := credit.NewEngine(store.New(client))

	const transferID = "evm:0xrace:native"
	openDeposit(t, client, transferID, "100")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := engine.Apply(ctx, transferID, domain.EventDepthReached); err != nil {
				t.Errorf("concurrent Apply(DEPTH_REACHED): %v", err)
			}
		}()
	}
	wg.Wait()

	const debits = 20
	results := make([]error, debits)
	for i := 0; i < debits; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = engine.Debit(ctx, "alice", "ETH", big.NewInt(10), fmt.Sprintf("withdrawal:%d", i))
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for i, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, errs.ErrInsufficientFunds):
		default:
			t.Errorf("Debit(%d) error = %v; want nil or ErrInsufficientFunds", i, err)
		}
	}

	entries, err := client.LedgerEntry.Query().All(ctx)
	if err != nil {
		t.Fatalf("query entries: %v", err)
	}
	if got, want := len(entries), 1+succeeded; got != want {
		t.Errorf("entries = %d; want %d (1 credit + %d debits)", got, want, succeeded)
	}
	bal, err := client.AccountBalance.Query().Only(ctx)
	if err != nil {
		t.Fatalf("query balance: %v", err)
	}
	want := new(big.Int).Sub(big.NewInt(100), big.NewInt(int64(10*succeeded)))
	if bal.Balance != want.String() {
		t.Errorf("balance = %s; want %s (ledger says %d successful debits)", bal.Balance, want, succeeded)
	}
	if bal.Balance[0] == '-' {
		t.Errorf("balance = %s; debits must never drive it negative", bal.Balance)
	}
}
