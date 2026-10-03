package credit

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"deposit-crediting/internal/domain/deposit"
	"deposit-crediting/internal/domain/ledger"
	"deposit-crediting/internal/errs"
)

// fakeStore is an in-memory Store with transaction semantics (rollback on
// error), so behavior-flow tests assert on state, not interactions.
type fakeStore struct {
	deposits map[string]View
	entries  []ledger.Entry
	balances map[string]*big.Int
	flagged  map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		deposits: map[string]View{},
		balances: map[string]*big.Int{},
		flagged:  map[string]bool{},
	}
}

func (f *fakeStore) seed(t *testing.T, transferID string, state deposit.State, amount int64) {
	t.Helper()
	f.deposits[transferID] = View{
		State:   state,
		Account: "alice",
		Asset:   "ETH",
		Amount:  big.NewInt(amount),
	}
}

func (f *fakeStore) InTx(ctx context.Context, fn func(context.Context, Tx) error) error {
	snapshot := *f
	snapshot.deposits = make(map[string]View, len(f.deposits))
	for k, v := range f.deposits {
		v.Amount = new(big.Int).Set(v.Amount)
		snapshot.deposits[k] = v
	}
	snapshot.entries = append([]ledger.Entry(nil), f.entries...)
	snapshot.balances = make(map[string]*big.Int, len(f.balances))
	for k, v := range f.balances {
		snapshot.balances[k] = new(big.Int).Set(v)
	}
	snapshot.flagged = make(map[string]bool, len(f.flagged))
	for k, v := range f.flagged {
		snapshot.flagged[k] = v
	}
	if err := fn(ctx, &fakeTx{f: f}); err != nil {
		*f = snapshot // roll back
		return err
	}
	return nil
}

type fakeTx struct{ f *fakeStore }

func (t *fakeTx) DepositForUpdate(_ context.Context, transferID string) (View, error) {
	v, ok := t.f.deposits[transferID]
	if !ok {
		return View{}, errs.ErrDepositNotFound
	}
	return v, nil
}

func (t *fakeTx) SetDepositState(_ context.Context, transferID string, state deposit.State) error {
	v := t.f.deposits[transferID]
	v.State = state
	t.f.deposits[transferID] = v
	return nil
}

func (t *fakeTx) InsertEntry(_ context.Context, e ledger.Entry) error {
	for _, existing := range t.f.entries {
		if existing.Ref == e.Ref {
			return errs.ErrDuplicateRef
		}
	}
	t.f.entries = append(t.f.entries, e)
	return nil
}

func (t *fakeTx) HasEntry(_ context.Context, ref string) (bool, error) {
	for _, e := range t.f.entries {
		if e.Ref == ref {
			return true, nil
		}
	}
	return false, nil
}

func (t *fakeTx) BalanceForUpdate(_ context.Context, account, asset string) (*big.Int, bool, error) {
	k := account + "/" + asset
	b, ok := t.f.balances[k]
	if !ok {
		return new(big.Int), false, nil
	}
	return new(big.Int).Set(b), t.f.flagged[k], nil
}

func (t *fakeTx) SetBalance(_ context.Context, account, asset string, balance *big.Int, flagged bool) error {
	k := account + "/" + asset
	t.f.balances[k] = new(big.Int).Set(balance)
	t.f.flagged[k] = flagged
	return nil
}

const testTransfer = "evm:0xabc:native"

func TestApplyCreditThenFinalize(t *testing.T) {
	store := newFakeStore()
	store.seed(t, testTransfer, deposit.StatePending, 100)
	engine := NewEngine(store)
	ctx := context.Background()

	if err := engine.Apply(ctx, testTransfer, deposit.EventDepthReached); err != nil {
		t.Fatalf("Apply(DEPTH_REACHED) unexpected error: %v", err)
	}
	if got, want := store.deposits[testTransfer].State, deposit.StateCredited; got != want {
		t.Errorf("state after credit = %s; want %s", got, want)
	}
	if got, want := len(store.entries), 1; got != want {
		t.Fatalf("entries after credit = %d; want %d", got, want)
	}
	entry := store.entries[0]
	if entry.Type != ledger.Credit || entry.Ref != testTransfer || entry.Amount.Cmp(big.NewInt(100)) != 0 {
		t.Errorf("entry = (%s, %s, %s); want (%s, %s, 100)",
			entry.Type, entry.Ref, entry.Amount, ledger.Credit, testTransfer)
	}
	if got, want := store.balances["alice/ETH"].String(), "100"; got != want {
		t.Errorf("balance = %s; want %s", got, want)
	}

	if err := engine.Apply(ctx, testTransfer, deposit.EventFinalityReached); err != nil {
		t.Fatalf("Apply(FINALITY_REACHED) unexpected error: %v", err)
	}
	if got, want := store.deposits[testTransfer].State, deposit.StateFinalized; got != want {
		t.Errorf("state after finalize = %s; want %s", got, want)
	}
	if got, want := len(store.entries), 1; got != want {
		t.Errorf("entries after finalize = %d; want %d (finalization writes nothing)", got, want)
	}
}

func TestApplyRedeliveryIsNoOp(t *testing.T) {
	store := newFakeStore()
	store.seed(t, testTransfer, deposit.StatePending, 100)
	engine := NewEngine(store)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := engine.Apply(ctx, testTransfer, deposit.EventDepthReached); err != nil {
			t.Fatalf("Apply(DEPTH_REACHED) delivery %d unexpected error: %v", i+1, err)
		}
	}
	if got, want := len(store.entries), 1; got != want {
		t.Errorf("entries after 3 identical deliveries = %d; want %d", got, want)
	}
	if got, want := store.balances["alice/ETH"].String(), "100"; got != want {
		t.Errorf("balance after 3 identical deliveries = %s; want %s", got, want)
	}
}

// A re-included transfer whose credit was never reversed must not be
// credited twice: the original entry still holds the funds.
func TestReincludeWithoutReversalDoesNotDoubleCredit(t *testing.T) {
	store := newFakeStore()
	store.seed(t, testTransfer, deposit.StatePending, 100)
	engine := NewEngine(store)
	ctx := context.Background()

	for _, ev := range []deposit.Event{
		deposit.EventDepthReached,
		deposit.EventReorgedOut,
		deposit.EventReincluded,
		deposit.EventDepthReached,
	} {
		if err := engine.Apply(ctx, testTransfer, ev); err != nil {
			t.Fatalf("Apply(%s) unexpected error: %v", ev, err)
		}
	}
	if got, want := store.deposits[testTransfer].State, deposit.StateCredited; got != want {
		t.Errorf("state = %s; want %s", got, want)
	}
	if got, want := len(store.entries), 1; got != want {
		t.Errorf("entries = %d; want %d (re-inclusion must not double-credit)", got, want)
	}
	if got, want := store.balances["alice/ETH"].String(), "100"; got != want {
		t.Errorf("balance = %s; want %s", got, want)
	}
}

// A transfer re-included after its credit was reversed opens a new credit
// cycle: the re-credit entry restores the funds under a deterministic ref.
func TestRecreditAfterReversalRestoresFunds(t *testing.T) {
	store := newFakeStore()
	store.seed(t, testTransfer, deposit.StatePending, 100)
	engine := NewEngine(store)
	ctx := context.Background()

	for _, ev := range []deposit.Event{
		deposit.EventDepthReached,
		deposit.EventReorgedOut,
		deposit.EventWindowExpiredCredited,
		deposit.EventReincluded,
		deposit.EventDepthReached,
	} {
		if err := engine.Apply(ctx, testTransfer, ev); err != nil {
			t.Fatalf("Apply(%s) unexpected error: %v", ev, err)
		}
	}
	if got, want := store.deposits[testTransfer].State, deposit.StateCredited; got != want {
		t.Errorf("state = %s; want %s", got, want)
	}
	if got, want := len(store.entries), 3; got != want {
		t.Fatalf("entries = %d; want %d (credit, reversal, re-credit)", got, want)
	}
	recredit := store.entries[2]
	if recredit.Type != ledger.Credit || recredit.Ref != "recredit:"+testTransfer {
		t.Errorf("re-credit = (%s, %s); want (%s, recredit:%s)",
			recredit.Type, recredit.Ref, ledger.Credit, testTransfer)
	}
	if got, want := store.balances["alice/ETH"].String(), "100"; got != want {
		t.Errorf("balance = %s; want %s (reversal then re-credit)", got, want)
	}

	if err := engine.Apply(ctx, testTransfer, deposit.EventDepthReached); err != nil {
		t.Fatalf("Apply(DEPTH_REACHED) redelivery unexpected error: %v", err)
	}
	if got, want := len(store.entries), 3; got != want {
		t.Errorf("entries after redelivery = %d; want %d", got, want)
	}
}

// ADR 0002: credit, spend, deep reorg, reversal, negative balance, blocked
// debits.
func TestReversalAfterSpendFlagsAccount(t *testing.T) {
	store := newFakeStore()
	store.seed(t, testTransfer, deposit.StatePending, 100)
	engine := NewEngine(store)
	ctx := context.Background()

	if err := engine.Apply(ctx, testTransfer, deposit.EventDepthReached); err != nil {
		t.Fatalf("Apply(DEPTH_REACHED) unexpected error: %v", err)
	}
	if err := engine.Debit(ctx, "alice", "ETH", big.NewInt(70), "withdrawal:1"); err != nil {
		t.Fatalf("Debit() unexpected error: %v", err)
	}
	if err := engine.Apply(ctx, testTransfer, deposit.EventReorgedOut); err != nil {
		t.Fatalf("Apply(REORGED_OUT) unexpected error: %v", err)
	}
	if err := engine.Apply(ctx, testTransfer, deposit.EventWindowExpiredCredited); err != nil {
		t.Fatalf("Apply(WINDOW_EXPIRED_CREDITED) unexpected error: %v", err)
	}

	if got, want := store.deposits[testTransfer].State, deposit.StateReversed; got != want {
		t.Errorf("state = %s; want %s", got, want)
	}
	if got, want := len(store.entries), 3; got != want {
		t.Fatalf("entries = %d; want %d (credit, debit, reversal)", got, want)
	}
	reversal := store.entries[2]
	if reversal.Type != ledger.Reversal || reversal.Ref != "reversal:"+testTransfer {
		t.Errorf("reversal = (%s, %s); want (%s, reversal:%s)",
			reversal.Type, reversal.Ref, ledger.Reversal, testTransfer)
	}
	if got, want := store.balances["alice/ETH"].String(), "-70"; got != want {
		t.Errorf("balance = %s; want %s", got, want)
	}
	if !store.flagged["alice/ETH"] {
		t.Error("account not flagged after negative reversal; want flagged")
	}

	if err := engine.Debit(ctx, "alice", "ETH", big.NewInt(1), "withdrawal:2"); !errors.Is(err, errs.ErrAccountFlagged) {
		t.Errorf("Debit() on flagged account error = %v; want errs.ErrAccountFlagged", err)
	}
}

func TestDebitInsufficientFundsRollsBack(t *testing.T) {
	store := newFakeStore()
	store.seed(t, testTransfer, deposit.StatePending, 100)
	engine := NewEngine(store)
	ctx := context.Background()

	if err := engine.Apply(ctx, testTransfer, deposit.EventDepthReached); err != nil {
		t.Fatalf("Apply(DEPTH_REACHED) unexpected error: %v", err)
	}
	if err := engine.Debit(ctx, "alice", "ETH", big.NewInt(150), "withdrawal:1"); !errors.Is(err, errs.ErrInsufficientFunds) {
		t.Fatalf("Debit() error = %v; want errs.ErrInsufficientFunds", err)
	}
	if got, want := store.balances["alice/ETH"].String(), "100"; got != want {
		t.Errorf("balance after failed debit = %s; want %s (rolled back)", got, want)
	}
	if got, want := len(store.entries), 1; got != want {
		t.Errorf("entries after failed debit = %d; want %d (rolled back)", got, want)
	}
}

func TestApplyIllegalEventFailsLoudly(t *testing.T) {
	store := newFakeStore()
	store.seed(t, testTransfer, deposit.StateFinalized, 100)
	engine := NewEngine(store)
	if err := engine.Apply(context.Background(), testTransfer, deposit.EventReorgedOut); !errors.Is(err, errs.ErrIllegalTransition) {
		t.Errorf("Apply(REORGED_OUT on FINALIZED) error = %v; want errs.ErrIllegalTransition", err)
	}
}
