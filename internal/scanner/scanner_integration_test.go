//go:build integration

// Scanner integration tests: the full self-built ingest path against
// dockerized Postgres and a controllable mock chain — the P2 done-criteria
// (docs/implementation-plan.md).
package scanner_test

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"deposit-crediting/internal/adapters/chain"
	"deposit-crediting/internal/adapters/chain/chaintest"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/domain/deposit"
	"deposit-crediting/internal/errs"
	"deposit-crediting/internal/scanner"
	"deposit-crediting/internal/store"
	"deposit-crediting/internal/store/ent"
	entdeposit "deposit-crediting/internal/store/ent/deposit"

	_ "github.com/lib/pq"
)

const (
	chainID     = "evm"
	aliceAddr   = "0xalice"
	testTxHash  = "0xtx1"
	testTransID = "evm:" + testTxHash + ":native"
)

// Test policy: credit at depth 3, finalize at 5, reorg window 4, min 10.
type fixture struct {
	t      *testing.T
	ctx    context.Context
	client *ent.Client
	engine *credit.Engine
	chain  *chaintest.Chain
	sc     *scanner.Scanner
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), client: openTestDB(t), chain: chaintest.NewChain()}
	f.engine = credit.NewEngine(store.New(f.client))
	f.sc = scanner.New(
		scanner.Config{ChainID: chainID, StartHeight: 1, MaxBatch: 100},
		f.chain, store.NewScannerStore(f.client), f.engine,
	)
	if _, err := f.client.AssetConfig.Create().
		SetChain(chainID).SetAsset("ETH").SetDecimals(18).
		SetMode("self_built").SetMinAmount("10").
		SetNCredit(3).SetNFinalize(5).SetReorgWindow(4).
		Save(f.ctx); err != nil {
		t.Fatalf("seed asset config: %v", err)
	}
	if _, err := f.client.DepositAddress.Create().
		SetAccount("alice").SetChain(chainID).SetAddress(aliceAddr).SetMode("self_built").
		Save(f.ctx); err != nil {
		t.Fatalf("seed address: %v", err)
	}
	return f
}

func openTestDB(t *testing.T) *ent.Client {
	t.Helper()
	client, err := ent.Open("postgres", "postgres://postgres:postgres@localhost:5432/deposit_crediting?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Skipf("postgres unavailable, skipping integration test: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Schema.Create(ctx); err != nil {
		t.Skipf("postgres unreachable at localhost:5432, skipping integration test: %v", err)
	}
	t.Cleanup(func() {
		for _, del := range []func(context.Context) (int, error){
			client.LedgerEntry.Delete().Exec,
			client.AccountBalance.Delete().Exec,
			client.Deposit.Delete().Exec,
			client.DepositAddress.Delete().Exec,
			client.AssetConfig.Delete().Exec,
			client.CanonicalBlock.Delete().Exec,
			client.ChainCursor.Delete().Exec,
		} {
			if _, err := del(context.Background()); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
		if err := client.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
	})
	return client
}

func nativeTransfer(txHash, to string, amount int64) chain.Transfer {
	return chain.Transfer{Kind: chain.Native, TxHash: txHash, From: "0xexternal", To: to, Asset: "ETH", Amount: big.NewInt(amount)}
}

func (f *fixture) addBlocks(n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		f.chain.AddBlock()
	}
}

func (f *fixture) tick() {
	f.t.Helper()
	if err := f.sc.Tick(f.ctx); err != nil {
		f.t.Fatalf("Tick() unexpected error: %v", err)
	}
}

func (f *fixture) state(transferID string) deposit.State {
	f.t.Helper()
	row, err := f.client.Deposit.Query().Where(entdeposit.TransferID(transferID)).Only(f.ctx)
	if err != nil {
		f.t.Fatalf("query deposit %s: %v", transferID, err)
	}
	return deposit.State(row.State)
}

func (f *fixture) depositCount() int {
	f.t.Helper()
	n, err := f.client.Deposit.Query().Count(f.ctx)
	if err != nil {
		f.t.Fatalf("count deposits: %v", err)
	}
	return n
}

func (f *fixture) balance(account, asset string) (string, bool) {
	f.t.Helper()
	row, err := f.client.AccountBalance.Query().Only(f.ctx)
	if err != nil {
		f.t.Fatalf("query balance: %v", err)
	}
	return row.Balance, row.Flagged
}

func (f *fixture) entryRefs() []string {
	f.t.Helper()
	entries, err := f.client.LedgerEntry.Query().All(f.ctx)
	if err != nil {
		f.t.Fatalf("query entries: %v", err)
	}
	refs := make([]string, 0, len(entries))
	for _, e := range entries {
		refs = append(refs, e.Ref)
	}
	return refs
}

// A reorg landing between the reorg check and the block fetch makes the
// next block not extend the cursor. Tick reports ErrChainInconsistent
// without advancing the cursor, and Run stops instead of retrying forever.
func TestScannerChainInconsistentStops(t *testing.T) {
	f := newFixture(t)
	f.sc = scanner.New(
		scanner.Config{ChainID: chainID, StartHeight: 1, MaxBatch: 100, PollInterval: 10 * time.Millisecond},
		f.chain, store.NewScannerStore(f.client), f.engine,
	)
	f.addBlocks(3)
	f.tick()

	f.chain.AddBlock()
	var once sync.Once
	f.chain.BeforeBlock = func(height uint64) {
		once.Do(func() { f.chain.CorruptParent(height, "0xdead") })
	}
	if err := f.sc.Tick(f.ctx); !errors.Is(err, errs.ErrChainInconsistent) {
		t.Fatalf("Tick() error = %v; want ErrChainInconsistent", err)
	}
	f.chain.BeforeBlock = nil

	cursor, found, err := store.NewScannerStore(f.client).Cursor(f.ctx, chainID)
	if err != nil || !found {
		t.Fatalf("Cursor() = %+v, %v, %v; want found", cursor, found, err)
	}
	if got, want := cursor.Height, uint64(3); got != want {
		t.Errorf("cursor height = %d; want %d (no partial advance)", got, want)
	}

	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	if err := f.sc.Run(ctx); !errors.Is(err, errs.ErrChainInconsistent) {
		t.Errorf("Run() error = %v; want ErrChainInconsistent", err)
	}
}

func TestScannerCreditsAndFinalizes(t *testing.T) {
	f := newFixture(t)
	f.chain.AddBlock()
	f.chain.AddBlock()
	f.chain.AddBlock(nativeTransfer(testTxHash, aliceAddr, 100))
	f.tick()
	if got, want := f.state(testTransID), deposit.StatePending; got != want {
		t.Fatalf("state at depth 1 = %s; want %s", got, want)
	}

	f.addBlocks(2) // head 5: depth 3
	f.tick()
	if got, want := f.state(testTransID), deposit.StateCredited; got != want {
		t.Fatalf("state at depth 3 = %s; want %s", got, want)
	}
	balance, flagged := f.balance("alice", "ETH")
	if want := "100"; balance != want || flagged {
		t.Errorf("balance = (%s, flagged=%v); want (%s, false)", balance, flagged, want)
	}

	f.addBlocks(2) // head 7: depth 5
	f.tick()
	if got, want := f.state(testTransID), deposit.StateFinalized; got != want {
		t.Errorf("state at depth 5 = %s; want %s", got, want)
	}
	if got, want := len(f.entryRefs()), 1; got != want {
		t.Errorf("entries = %d; want %d (credit only)", got, want)
	}
}

func TestScannerBelowMinimum(t *testing.T) {
	f := newFixture(t)
	f.chain.AddBlock(nativeTransfer(testTxHash, aliceAddr, 5)) // min is 10
	f.tick()
	if got, want := f.state(testTransID), deposit.StateBelowMinimum; got != want {
		t.Fatalf("state = %s; want %s", got, want)
	}
	f.addBlocks(6)
	f.tick()
	if got, want := f.state(testTransID), deposit.StateBelowMinimum; got != want {
		t.Errorf("state after more blocks = %s; want %s (absorbing)", got, want)
	}
	if got := f.depositCount(); got != 1 {
		t.Errorf("deposits = %d; want 1", got)
	}
	if got := len(f.entryRefs()); got != 0 {
		t.Errorf("entries = %d; want 0", got)
	}
}

func TestScannerReorgBeforeCreditDrops(t *testing.T) {
	f := newFixture(t)
	f.chain.AddBlock()
	f.chain.AddBlock()
	f.chain.AddBlock(nativeTransfer(testTxHash, aliceAddr, 100))
	f.tick() // head 3: depth 1, still pending
	if got := f.state(testTransID); got != deposit.StatePending {
		t.Fatalf("state = %s; want PENDING", got)
	}

	f.chain.Reorg(1) // orphan block 3, transfer gone
	f.addBlocks(1)   // replacement branch, head 3
	f.tick()
	if got, want := f.state(testTransID), deposit.StateReorged; got != want {
		t.Fatalf("state after reorg = %s; want %s", got, want)
	}

	f.addBlocks(4) // head 7: reorg window (4) elapsed
	f.tick()
	if got, want := f.state(testTransID), deposit.StateDropped; got != want {
		t.Errorf("state after window = %s; want %s", got, want)
	}
	if got := len(f.entryRefs()); got != 0 {
		t.Errorf("entries = %d; want 0 (never credited)", got)
	}
}

// ADR 0002 end to end: credit, spend, deep reorg, reversal, negative
// balance, flagged account.
func TestScannerDeepReorgReversesCredit(t *testing.T) {
	f := newFixture(t)
	f.chain.AddBlock()
	f.chain.AddBlock()
	f.chain.AddBlock(nativeTransfer(testTxHash, aliceAddr, 100))
	f.addBlocks(2)
	f.tick()
	if got := f.state(testTransID); got != deposit.StateCredited {
		t.Fatalf("state = %s; want CREDITED", got)
	}
	if err := f.engine.Debit(f.ctx, "alice", "ETH", big.NewInt(70), "withdrawal:1"); err != nil {
		t.Fatalf("Debit() unexpected error: %v", err)
	}

	f.chain.Reorg(3)
	f.addBlocks(3)
	f.tick()
	if got, want := f.state(testTransID), deposit.StateReorged; got != want {
		t.Fatalf("state after reorg = %s; want %s", got, want)
	}

	f.addBlocks(4)
	f.tick()
	if got, want := f.state(testTransID), deposit.StateReversed; got != want {
		t.Errorf("state after window = %s; want %s", got, want)
	}
	balance, flagged := f.balance("alice", "ETH")
	if want := "-70"; balance != want || !flagged {
		t.Errorf("balance = (%s, flagged=%v); want (%s, true)", balance, flagged, want)
	}
	if got, want := len(f.entryRefs()), 3; got != want {
		t.Errorf("entries = %d; want %d (credit, debit, reversal)", got, want)
	}
}

// A re-included transfer keeps its identity: back to PENDING, re-credited
// by depth with no duplicate entry (ADR 0001).
func TestScannerReinclusionKeepsSingleCredit(t *testing.T) {
	f := newFixture(t)
	f.chain.AddBlock()
	f.chain.AddBlock()
	f.chain.AddBlock(nativeTransfer(testTxHash, aliceAddr, 100))
	f.addBlocks(2)
	f.tick()
	if got := f.state(testTransID); got != deposit.StateCredited {
		t.Fatalf("state = %s; want CREDITED", got)
	}

	f.chain.Reorg(3)
	f.chain.AddBlock(nativeTransfer(testTxHash, aliceAddr, 100)) // same transfer, new branch
	f.addBlocks(2)
	f.tick()

	if got, want := f.state(testTransID), deposit.StateCredited; got != want {
		t.Errorf("state after re-inclusion = %s; want %s", got, want)
	}
	balance, _ := f.balance("alice", "ETH")
	if want := "100"; balance != want {
		t.Errorf("balance = %s; want %s (no double credit)", balance, want)
	}
	if got, want := len(f.entryRefs()), 1; got != want {
		t.Errorf("entries = %d; want %d", got, want)
	}
}

// A new Scanner instance on the same store continues idempotently.
func TestScannerRestartIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.chain.AddBlock(nativeTransfer(testTxHash, aliceAddr, 100))
	f.addBlocks(2)
	f.tick()
	if got := f.state(testTransID); got != deposit.StateCredited {
		t.Fatalf("state = %s; want CREDITED", got)
	}

	f.sc = scanner.New(
		scanner.Config{ChainID: chainID, StartHeight: 1, MaxBatch: 100},
		f.chain, store.NewScannerStore(f.client), f.engine,
	)
	f.tick() // no new blocks: pure redelivery
	f.addBlocks(2)
	f.tick()
	if got, want := f.state(testTransID), deposit.StateFinalized; got != want {
		t.Errorf("state = %s; want %s", got, want)
	}
	if got, want := len(f.entryRefs()), 1; got != want {
		t.Errorf("entries = %d; want %d", got, want)
	}
}

func TestScannerSkipsUnsupportedAndCustodian(t *testing.T) {
	f := newFixture(t)
	if _, err := f.client.DepositAddress.Create().
		SetAccount("bob").SetChain(chainID).SetAddress("0xbob").SetMode("custodian").
		Save(f.ctx); err != nil {
		t.Fatalf("seed custodian address: %v", err)
	}
	f.chain.AddBlock(
		nativeTransfer("0xt1", "0xbob", 100),     // custodian-mode address
		nativeTransfer(testTxHash, aliceAddr, 5), // below min; block still processed
		chain.Transfer{Kind: chain.Native, TxHash: "0xt2", To: aliceAddr, Asset: "USDT", Amount: big.NewInt(100)}, // no config
	)
	f.tick()
	if got, want := f.depositCount(), 1; got != want {
		t.Errorf("deposits = %d; want %d (only the self-built ETH one)", got, want)
	}
	if got, want := f.state(testTransID), deposit.StateBelowMinimum; got != want {
		t.Errorf("state = %s; want %s", got, want)
	}
}
