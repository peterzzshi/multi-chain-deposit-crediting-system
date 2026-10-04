//go:build integration

// Custodian ingest integration tests: webhook dedup, on-chain
// verification, reconciliation recovery, and the chain re-checker's
// depth/reorg handling — the P3 done-criteria
// (docs/implementation-plan.md), driven against a mock custodian with
// dup/delay/drop knobs and a controllable mock chain.
package custodian_test

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"deposit-crediting/internal/adapters/chain"
	"deposit-crediting/internal/adapters/chain/chaintest"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/custodian"
	"deposit-crediting/internal/custodian/custodiantest"
	"deposit-crediting/internal/domain/deposit"
	"deposit-crediting/internal/errs"
	"deposit-crediting/internal/store"
	"deposit-crediting/internal/store/ent"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
	"deposit-crediting/internal/store/storetest"
)

const (
	chainID    = "evm"
	provider   = "custodianA"
	aliceAddr  = "0xalice"
	testTxHash = "0xctx1"
	testTrans  = "evm:" + testTxHash + ":native"
)

// Test policy: credit at depth 3, finalize at 5, reorg window 4, min 10.
type fixture struct {
	t       *testing.T
	ctx     context.Context
	client  *ent.Client
	engine  *credit.Engine
	chain   *chaintest.Chain
	cust    *custodiantest.Custodian
	ing     *custodian.Ingestor
	recheck *custodian.Rechecker
	recon   *custodian.Reconciler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	client := storetest.OpenDB(t)
	f := &fixture{t: t, ctx: context.Background(), client: client, chain: chaintest.NewChain(), cust: custodiantest.New()}
	f.engine = credit.NewEngine(store.New(f.client))
	cstore := store.NewCustodianStore(f.client)
	cfg := custodian.Config{ChainID: chainID, Provider: provider}
	f.ing = custodian.NewIngestor(cfg, f.chain, cstore)
	f.recheck = custodian.NewRechecker(cfg, f.chain, cstore, f.engine)
	f.recon = custodian.NewReconciler(f.cust, f.ing, time.Minute, 2*time.Minute)
	if _, err := f.client.AssetConfig.Create().
		SetChain(chainID).SetAsset("ETH").SetDecimals(18).
		SetMode("custodian").SetMinAmount("10").
		SetNCredit(3).SetNFinalize(5).SetReorgWindow(4).
		Save(f.ctx); err != nil {
		t.Fatalf("seed asset config: %v", err)
	}
	if _, err := f.client.DepositAddress.Create().
		SetAccount("alice").SetChain(chainID).SetAddress(aliceAddr).SetMode("custodian").
		Save(f.ctx); err != nil {
		t.Fatalf("seed address: %v", err)
	}
	return f
}

// observeChainDeposit puts the transfer on-chain and registers the
// custodian's claim for it.
func (f *fixture) observeChainDeposit(txHash string, amount int64, opts ...custodiantest.ObserveOption) custodian.Claim {
	f.t.Helper()
	f.chain.AddBlock(chain.Transfer{Kind: chain.Native, TxHash: txHash, From: "0xexternal", To: aliceAddr, Asset: "ETH", Amount: big.NewInt(amount)})
	cl := custodian.Claim{
		ProviderEventID: "ev-" + txHash,
		Chain:           chainID,
		TxHash:          txHash,
		To:              aliceAddr,
		Asset:           "ETH",
		Amount:          big.NewInt(amount),
		ObservedAt:      time.Now(),
	}
	f.cust.Observe(cl, opts...)
	return cl
}

func (f *fixture) addBlocks(n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		f.chain.AddBlock()
	}
}

func (f *fixture) handle(claims ...custodian.Claim) {
	f.t.Helper()
	for _, cl := range claims {
		if err := f.ing.Handle(f.ctx, cl); err != nil {
			f.t.Fatalf("Handle(%s) unexpected error: %v", cl.ProviderEventID, err)
		}
	}
}

func (f *fixture) recheckTick() {
	f.t.Helper()
	if err := f.recheck.Tick(f.ctx); err != nil {
		f.t.Fatalf("Rechecker.Tick() unexpected error: %v", err)
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

func (f *fixture) sourceEventCount() int {
	f.t.Helper()
	n, err := f.client.SourceEvent.Query().Count(f.ctx)
	if err != nil {
		f.t.Fatalf("count source events: %v", err)
	}
	return n
}

func (f *fixture) balance() (string, bool) {
	f.t.Helper()
	row, err := f.client.AccountBalance.Query().Only(f.ctx)
	if err != nil {
		f.t.Fatalf("query balance: %v", err)
	}
	return row.Balance, row.Flagged
}

func (f *fixture) entryCount() int {
	f.t.Helper()
	n, err := f.client.LedgerEntry.Query().Count(f.ctx)
	if err != nil {
		f.t.Fatalf("count entries: %v", err)
	}
	return n
}

func TestCustodianClaimCreditsAndFinalizes(t *testing.T) {
	f := newFixture(t)
	f.observeChainDeposit(testTxHash, 100)
	f.handle(f.cust.Webhooks()...)
	if got, want := f.state(testTrans), deposit.StatePending; got != want {
		t.Fatalf("state after webhook = %s; want %s", got, want)
	}

	f.addBlocks(2) // head 3: depth 3 >= N_credit
	f.recheckTick()
	if got, want := f.state(testTrans), deposit.StateCredited; got != want {
		t.Fatalf("state at N_credit = %s; want %s", got, want)
	}

	f.addBlocks(2) // head 5: depth 5 >= N_finalize
	f.recheckTick()
	if got, want := f.state(testTrans), deposit.StateFinalized; got != want {
		t.Fatalf("state at N_finalize = %s; want %s", got, want)
	}
	if got := f.entryCount(); got != 1 {
		t.Errorf("entries = %d; want 1 (credit only)", got)
	}
	if got, _ := f.balance(); got != "100" {
		t.Errorf("balance = %s; want 100", got)
	}
	if got := f.sourceEventCount(); got != 1 {
		t.Errorf("source events = %d; want 1", got)
	}
}

func TestCustodianDuplicateAndRenamedDeliveries(t *testing.T) {
	f := newFixture(t)
	f.observeChainDeposit(testTxHash, 100, custodiantest.Duplicate())
	f.handle(f.cust.Webhooks()...) // same provider event ID twice

	// Same transfer reported again under a NEW provider event ID: the
	// source-event layer cannot dedup it, the transfer ID layer must.
	renamed := custodian.Claim{
		ProviderEventID: "ev-renamed",
		Chain:           chainID,
		TxHash:          testTxHash,
		To:              aliceAddr,
		Asset:           "ETH",
		Amount:          big.NewInt(100),
		ObservedAt:      time.Now(),
	}
	f.handle(renamed)

	f.addBlocks(2)
	f.recheckTick()
	if got := f.depositCount(); got != 1 {
		t.Errorf("deposits = %d; want 1", got)
	}
	if got := f.sourceEventCount(); got != 2 {
		t.Errorf("source events = %d; want 2 (one per distinct provider event ID)", got)
	}
	if got := f.entryCount(); got != 1 {
		t.Errorf("entries = %d; want 1 (single credit)", got)
	}
	if got, _ := f.balance(); got != "100" {
		t.Errorf("balance = %s; want 100", got)
	}
}

func TestCustodianDroppedWebhookRecoveredByReconciliation(t *testing.T) {
	f := newFixture(t)
	f.observeChainDeposit(testTxHash, 100, custodiantest.Dropped())
	if got := f.cust.Webhooks(); len(got) != 0 {
		t.Fatalf("webhooks = %d; want 0 (dropped)", len(got))
	}
	if got := f.depositCount(); got != 0 {
		t.Fatalf("deposits before reconciliation = %d; want 0", got)
	}

	if err := f.recon.Tick(f.ctx); err != nil {
		t.Fatalf("Reconciler.Tick() unexpected error: %v", err)
	}
	if got, want := f.state(testTrans), deposit.StatePending; got != want {
		t.Fatalf("state after reconciliation = %s; want %s", got, want)
	}
	if got := f.sourceEventCount(); got != 1 {
		t.Errorf("source events = %d; want 1", got)
	}

	// A delayed delivery of the same claim afterwards is a no-op.
	f.observeChainDeposit("0xctx2", 50, custodiantest.Delayed())
	f.cust.ReleaseDelayed()
	f.handle(f.cust.Webhooks()...)
	if got := f.depositCount(); got != 2 {
		t.Errorf("deposits = %d; want 2 (delayed claim ingested once)", got)
	}
}

func TestCustodianRejectsUnverifiableClaims(t *testing.T) {
	f := newFixture(t)

	phantom := custodian.Claim{
		ProviderEventID: "ev-ghost", Chain: chainID, TxHash: "0xghost",
		To: aliceAddr, Asset: "ETH", Amount: big.NewInt(100), ObservedAt: time.Now(),
	}
	if err := f.ing.Handle(f.ctx, phantom); !errors.Is(err, errs.ErrClaimNotOnChain) {
		t.Errorf("Handle(phantom) error = %v; want ErrClaimNotOnChain", err)
	}

	// The claim contradicts the chain on amount: definitive reject, no error.
	f.chain.AddBlock(chain.Transfer{Kind: chain.Native, TxHash: "0xreal", From: "0xexternal", To: aliceAddr, Asset: "ETH", Amount: big.NewInt(100)})
	mismatched := custodian.Claim{
		ProviderEventID: "ev-mismatch", Chain: chainID, TxHash: "0xreal",
		To: aliceAddr, Asset: "ETH", Amount: big.NewInt(999), ObservedAt: time.Now(),
	}
	if err := f.ing.Handle(f.ctx, mismatched); err != nil {
		t.Errorf("Handle(amount mismatch) error = %v; want nil (silent reject)", err)
	}

	// An address we do not own is ignored.
	stranger := custodian.Claim{
		ProviderEventID: "ev-stranger", Chain: chainID, TxHash: "0xreal",
		To: "0xstranger", Asset: "ETH", Amount: big.NewInt(100), ObservedAt: time.Now(),
	}
	if err := f.ing.Handle(f.ctx, stranger); err != nil {
		t.Errorf("Handle(foreign address) error = %v; want nil", err)
	}

	if got := f.depositCount(); got != 0 {
		t.Errorf("deposits = %d; want 0 (nothing creditable)", got)
	}
	if got := f.sourceEventCount(); got != 0 {
		t.Errorf("source events = %d; want 0 (nothing ingested)", got)
	}
}

func TestCustodianNodeLagClaimRetriedByReconciliation(t *testing.T) {
	f := newFixture(t)
	// The custodian saw the deposit, but our node has not mined it yet.
	lagged := custodian.Claim{
		ProviderEventID: "ev-lag", Chain: chainID, TxHash: testTxHash,
		To: aliceAddr, Asset: "ETH", Amount: big.NewInt(100), ObservedAt: time.Now(),
	}
	f.cust.Observe(lagged, custodiantest.Dropped())
	if err := f.recon.Tick(f.ctx); err != nil {
		t.Fatalf("Reconciler.Tick() with unmined claim error = %v; want nil (skipped)", err)
	}
	if got := f.depositCount(); got != 0 {
		t.Fatalf("deposits before mining = %d; want 0", got)
	}

	// The tx lands on-chain; the next overlapping poll ingests it.
	f.chain.AddBlock(chain.Transfer{Kind: chain.Native, TxHash: testTxHash, From: "0xexternal", To: aliceAddr, Asset: "ETH", Amount: big.NewInt(100)})
	if err := f.recon.Tick(f.ctx); err != nil {
		t.Fatalf("Reconciler.Tick() after mining unexpected error: %v", err)
	}
	if got, want := f.state(testTrans), deposit.StatePending; got != want {
		t.Fatalf("state after retry = %s; want %s", got, want)
	}
}

func TestCustodianVaultSolvency(t *testing.T) {
	f := newFixture(t)
	f.observeChainDeposit(testTxHash, 100)
	f.handle(f.cust.Webhooks()...)
	f.addBlocks(2)
	f.recheckTick()
	if got, _ := f.balance(); got != "100" {
		t.Fatalf("balance = %s; want 100", got)
	}

	checker := custodian.NewSolvencyChecker(
		custodian.Config{ChainID: chainID, Provider: provider}, f.cust, store.NewCustodianStore(f.client), time.Minute)

	f.cust.SetVaultTotal(chainID, "ETH", big.NewInt(100))
	if got, err := checker.Check(f.ctx); err != nil || len(got) != 0 {
		t.Errorf("Check() at full backing = %v, %v; want no discrepancy", got, err)
	}

	// The vault shrinks below the ledger total: insolvency must surface.
	f.cust.SetVaultTotal(chainID, "ETH", big.NewInt(50))
	got, err := checker.Check(f.ctx)
	if err != nil {
		t.Fatalf("Check() unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Check() discrepancies = %d; want 1", len(got))
	}
	d := got[0]
	if d.Asset != "ETH" || d.LedgerTotal.String() != "100" || d.VaultTotal.String() != "50" {
		t.Errorf("discrepancy = %+v; want ETH ledger 100 vs vault 50", d)
	}

	// The reconciler runs the check every tick without failing on a
	// discrepancy (an alert, not an ingest error).
	recon := custodian.NewReconciler(f.cust, f.ing, time.Minute, 2*time.Minute).WithSolvency(checker)
	if err := recon.Tick(f.ctx); err != nil {
		t.Errorf("Reconciler.Tick() with discrepancy error = %v; want nil (alert only)", err)
	}
}

func TestCustodianReorgReversesAfterSpend(t *testing.T) {
	f := newFixture(t)
	f.observeChainDeposit(testTxHash, 100)
	f.handle(f.cust.Webhooks()...)
	f.addBlocks(2)
	f.recheckTick()
	if got := f.state(testTrans); got != deposit.StateCredited {
		t.Fatalf("state = %s; want CREDITED", got)
	}
	if err := f.engine.Debit(f.ctx, "alice", "ETH", big.NewInt(70), "withdrawal:1"); err != nil {
		t.Fatalf("Debit() unexpected error: %v", err)
	}

	// Deep reorg orphans the tx's block; the replacement branch lacks it.
	f.chain.Reorg(3)
	f.addBlocks(3)
	f.recheckTick()
	if got, want := f.state(testTrans), deposit.StateReorged; got != want {
		t.Fatalf("state after reorg = %s; want %s", got, want)
	}

	f.addBlocks(4) // reorg window (4) elapsed, never re-included
	f.recheckTick()
	if got, want := f.state(testTrans), deposit.StateReversed; got != want {
		t.Fatalf("state after window = %s; want %s", got, want)
	}
	bal, flagged := f.balance()
	if bal != "-70" || !flagged {
		t.Errorf("balance = %s, flagged = %v; want -70, true (ADR 0002)", bal, flagged)
	}
}

func TestCustodianReincludeKeepsSingleCredit(t *testing.T) {
	f := newFixture(t)
	f.observeChainDeposit(testTxHash, 100)
	f.handle(f.cust.Webhooks()...)
	f.addBlocks(2)
	f.recheckTick()
	if got := f.state(testTrans); got != deposit.StateCredited {
		t.Fatalf("state = %s; want CREDITED", got)
	}

	// The tx's block is orphaned; the replacement branch has no tx.
	f.chain.Reorg(3)
	f.addBlocks(3)
	f.recheckTick()
	if got := f.state(testTrans); got != deposit.StateReorged {
		t.Fatalf("state after reorg = %s; want REORGED", got)
	}

	// The tx is re-included in a later block; the re-checker picks it up
	// via TxByHash and re-opens the credit cycle without double-crediting.
	f.chain.AddBlock(chain.Transfer{Kind: chain.Native, TxHash: testTxHash, From: "0xexternal", To: aliceAddr, Asset: "ETH", Amount: big.NewInt(100)})
	f.recheckTick()
	if got, want := f.state(testTrans), deposit.StatePending; got != want {
		t.Fatalf("state after re-inclusion = %s; want %s", got, want)
	}

	f.addBlocks(3)
	f.recheckTick()
	if got, want := f.state(testTrans), deposit.StateCredited; got != want {
		t.Fatalf("state after re-credit depth = %s; want %s", got, want)
	}
	if got := f.entryCount(); got != 1 {
		t.Errorf("entries = %d; want 1 (re-credit is a no-op while credit is intact)", got)
	}
	if got, _ := f.balance(); got != "100" {
		t.Errorf("balance = %s; want 100", got)
	}
}

// A BlockHash outage must not be read as reorg evidence: the tick fails
// without touching deposits, and recovers when the node is back.
func TestRecheckerBlockHashOutageDoesNotMutate(t *testing.T) {
	f := newFixture(t)
	f.observeChainDeposit(testTxHash, 100)
	f.handle(f.cust.Webhooks()...)
	if got := f.state(testTrans); got != deposit.StatePending {
		t.Fatalf("state = %s; want PENDING", got)
	}

	f.chain.BlockHashErr = errors.New("rpc timeout")
	if err := f.recheck.Tick(f.ctx); err == nil {
		t.Fatal("Rechecker.Tick() during outage = nil error; want error")
	}
	if got := f.state(testTrans); got != deposit.StatePending {
		t.Errorf("state during outage = %s; want PENDING (no reorg mutation)", got)
	}
	row, err := f.client.Deposit.Query().Where(entdeposit.TransferID(testTrans)).Only(f.ctx)
	if err != nil {
		t.Fatalf("query deposit: %v", err)
	}
	if row.ReorgedHeight != nil {
		t.Errorf("reorged height set during outage; want NULL")
	}

	f.chain.BlockHashErr = nil
	f.addBlocks(2)
	f.recheckTick()
	if got, want := f.state(testTrans), deposit.StateCredited; got != want {
		t.Errorf("state after recovery = %s; want %s", got, want)
	}
}

// A custodian deposit reversed after the window is still watched: the
// re-checker finds the re-included transaction via TxByHash and opens a
// new credit cycle (ADR 0005).
func TestCustodianReinclusionAfterReversalOpensNewCycle(t *testing.T) {
	f := newFixture(t)
	f.observeChainDeposit(testTxHash, 100)
	f.handle(f.cust.Webhooks()...)
	f.addBlocks(2)
	f.recheckTick()
	if got := f.state(testTrans); got != deposit.StateCredited {
		t.Fatalf("state = %s; want CREDITED", got)
	}

	f.chain.Reorg(3)
	f.addBlocks(3)
	f.recheckTick()
	f.addBlocks(4) // window (4) elapses
	f.recheckTick()
	if got, want := f.state(testTrans), deposit.StateReversed; got != want {
		t.Fatalf("state = %s; want %s", got, want)
	}
	if got, _ := f.balance(); got != "0" {
		t.Fatalf("balance after reversal = %s; want 0", got)
	}

	f.chain.AddBlock(chain.Transfer{Kind: chain.Native, TxHash: testTxHash, From: "0xexternal", To: aliceAddr, Asset: "ETH", Amount: big.NewInt(100)})
	f.recheckTick()
	if got, want := f.state(testTrans), deposit.StatePending; got != want {
		t.Fatalf("state after re-inclusion = %s; want %s", got, want)
	}
	f.addBlocks(3)
	f.recheckTick()
	if got, want := f.state(testTrans), deposit.StateCredited; got != want {
		t.Fatalf("state after new cycle depth = %s; want %s", got, want)
	}
	if got := f.entryCount(); got != 3 {
		t.Errorf("entries = %d; want 3 (credit, reversal, re-credit)", got)
	}
	if got, _ := f.balance(); got != "100" {
		t.Errorf("balance = %s; want 100 (re-credit restored funds)", got)
	}
}
