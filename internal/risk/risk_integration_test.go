//go:build integration

// Risk monitor integration tests: exposure-cap holds and the runtime
// invariant monitors, against dockerized Postgres — the P4 done-criteria
// (docs/implementation-plan.md).
package risk_test

import (
	"context"
	"errors"
	"math/big"
	"testing"

	chaintest "deposit-crediting/internal/adapters"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"
	"deposit-crediting/internal/risk"
	"deposit-crediting/internal/store"
	"deposit-crediting/internal/store/ent"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
)

const chainID = "evm"

type fixture struct {
	t      *testing.T
	ctx    context.Context
	client *ent.Client
	engine *credit.Engine
	mon    *risk.Monitor
}

// Cap 150, no tier: holds apply to every credit during a breach.
func newFixture(t *testing.T, cap, tier string) *fixture {
	t.Helper()
	client := store.OpenDB(t)
	f := &fixture{t: t, ctx: context.Background(), client: client}
	f.engine = credit.NewEngine(store.New(client))
	f.mon = risk.NewMonitor(risk.Config{ChainID: chainID}, store.NewRiskStore(client))
	cfg := client.AssetConfig.Create().
		SetChain(chainID).SetAsset("ETH").SetDecimals(18).
		SetMode("self_built").SetMinAmount("10").
		SetNCredit(3).SetNFinalize(5).SetReorgWindow(4).
		SetExposureCap(cap)
	if tier != "" {
		cfg.SetTierAmount(tier)
	}
	if _, err := cfg.Save(f.ctx); err != nil {
		t.Fatalf("seed asset config: %v", err)
	}
	return f
}

func (f *fixture) creditDeposit(transferID string, amount int64) {
	f.t.Helper()
	state, _, err := domain.Transition(domain.StateNone, domain.EventObserved)
	if err != nil {
		f.t.Fatalf("Transition(OBSERVED) unexpected error: %v", err)
	}
	amountStr := big.NewInt(amount).String()
	if _, err := f.client.Deposit.Create().
		SetTransferID(transferID).SetChain(chainID).SetAsset("ETH").
		SetAccount("alice").SetAddress("0xdeposit").SetAmount(amountStr).
		SetMode(entdomain.ModeSelfBuilt).SetState(entdomain.State(state)).
		SetBlockHeight(1).SetBlockHash("0xb1").
		Save(f.ctx); err != nil {
		f.t.Fatalf("open deposit %s: %v", transferID, err)
	}
	if err := f.engine.Apply(f.ctx, transferID, domain.EventDepthReached); err != nil {
		f.t.Fatalf("credit %s: %v", transferID, err)
	}
}

func (f *fixture) balance() (amount, held string) {
	f.t.Helper()
	row, err := f.client.AccountBalance.Query().Only(f.ctx)
	if err != nil {
		f.t.Fatalf("query balance: %v", err)
	}
	return row.Balance, row.Held
}

func (f *fixture) depositHeld(transferID string) bool {
	f.t.Helper()
	row, err := f.client.Deposit.Query().Where(entdomain.TransferID(transferID)).Only(f.ctx)
	if err != nil {
		f.t.Fatalf("query deposit %s: %v", transferID, err)
	}
	return row.Held
}

func TestExposureCapHoldsNewCreditsAndReleasesOnDrain(t *testing.T) {
	f := newFixture(t, "150", "")
	f.creditDeposit("evm:0xa:native", 100)

	alerts, err := f.mon.Tick(f.ctx)
	if err != nil || len(alerts) != 0 {
		t.Fatalf("Tick() at 67%% of cap = %v alerts, %v; want none", alerts, err)
	}
	if err := f.engine.Debit(f.ctx, "alice", "ETH", big.NewInt(50), "withdrawal:1"); err != nil {
		t.Fatalf("Debit() unexpected error: %v", err)
	}

	f.creditDeposit("evm:0xb:native", 100) // exposure now 200 >= cap 150
	alerts, err = f.mon.Tick(f.ctx)
	if err != nil {
		t.Fatalf("Tick() unexpected error: %v", err)
	}
	if len(alerts) != 1 || alerts[0].Level != "critical" {
		t.Fatalf("Tick() on breach alerts = %v; want one critical", alerts)
	}

	// The credit still posts — only spendability is held.
	f.creditDeposit("evm:0xc:native", 100)
	if !f.depositHeld("evm:0xc:native") {
		t.Error("deposit C held = false; want true under active cap")
	}
	balance, held := f.balance()
	if balance != "250" || held != "100" {
		t.Errorf("balance/held = %s/%s; want 250/100 (credit posted, spendability held)", balance, held)
	}
	if err := f.engine.Debit(f.ctx, "alice", "ETH", big.NewInt(200), "withdrawal:2"); !errors.Is(err, errs.ErrInsufficientFunds) {
		t.Errorf("Debit(200) with 150 spendable error = %v; want ErrInsufficientFunds", err)
	}

	// Finalizations drain exposure below the release threshold: holds
	// lift and the held deposit becomes spendable.
	for _, id := range []string{"evm:0xa:native", "evm:0xb:native"} {
		if err := f.engine.Apply(f.ctx, id, domain.EventFinalityReached); err != nil {
			t.Fatalf("finalize %s: %v", id, err)
		}
	}
	alerts, err = f.mon.Tick(f.ctx)
	if err != nil {
		t.Fatalf("Tick() after drain unexpected error: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("Tick() after drain alerts = %v; want none", alerts)
	}
	if f.depositHeld("evm:0xc:native") {
		t.Error("deposit C held = true after release; want false")
	}
	if _, held := f.balance(); held != "0" {
		t.Errorf("held = %s after release; want 0", held)
	}
	if err := f.engine.Debit(f.ctx, "alice", "ETH", big.NewInt(100), "withdrawal:3"); err != nil {
		t.Errorf("Debit() after release unexpected error: %v", err)
	}
}

func TestExposureCapHoldsOnlyAboveTier(t *testing.T) {
	f := newFixture(t, "100", "50")
	f.creditDeposit("evm:0xa:native", 100) // exposure 100 >= cap: breach
	if _, err := f.mon.Tick(f.ctx); err != nil {
		t.Fatalf("Tick() unexpected error: %v", err)
	}

	f.creditDeposit("evm:0xsmall:native", 30)
	f.creditDeposit("evm:0xlarge:native", 60)
	if f.depositHeld("evm:0xsmall:native") {
		t.Error("below-tier credit held; want spendable")
	}
	if !f.depositHeld("evm:0xlarge:native") {
		t.Error("above-tier credit spendable; want held")
	}
	if _, held := f.balance(); held != "60" {
		t.Errorf("held = %s; want 60 (only the above-tier credit)", held)
	}
}

func TestInvariantMonitorsFireOnInjectedFaults(t *testing.T) {
	f := newFixture(t, "1000", "")
	f.creditDeposit("evm:0xa:native", 100)
	chain := adapters.NewChain()
	checker := risk.NewInvariantChecker(chainID, chain, store.NewRiskStore(f.client))

	chain.AddBlock()
	if got, err := checker.Check(f.ctx); err != nil || len(got) != 0 {
		t.Fatalf("Check() on healthy state = %v, %v; want no violations", got, err)
	}

	// Fault 1: a deposit forced to CREDITED without a ledger entry.
	if _, err := f.client.Deposit.Create().
		SetTransferID("evm:0xmissed:native").SetChain(chainID).SetAsset("ETH").
		SetAccount("bob").SetAddress("0xdep2").SetAmount("10").
		SetMode(entdomain.ModeSelfBuilt).SetState(entdomain.StateCREDITED).
		SetBlockHeight(1).SetBlockHash("0xb1").
		Save(f.ctx); err != nil {
		t.Fatalf("inject missed credit: %v", err)
	}

	// Fault 2: the balance projection drifts from the domain.
	if err := f.client.AccountBalance.Update().
		SetBalance("70").
		Exec(f.ctx); err != nil {
		t.Fatalf("inject balance drift: %v", err)
	}

	// Fault 3: a REORGED deposit stuck long past its window.
	h := int64(1)
	if _, err := f.client.Deposit.Create().
		SetTransferID("evm:0xstuck:native").SetChain(chainID).SetAsset("ETH").
		SetAccount("carol").SetAddress("0xdep3").SetAmount("10").
		SetMode(entdomain.ModeSelfBuilt).SetState(entdomain.StateREORGED).
		SetBlockHeight(1).SetBlockHash("0xb1").
		SetReorgedHeight(h).
		Save(f.ctx); err != nil {
		t.Fatalf("inject stuck reorg: %v", err)
	}
	for i := 0; i < 10; i++ { // head 11, window 4: 11-1 > 2*4
		chain.AddBlock()
	}

	violations, err := checker.Check(f.ctx)
	if err != nil {
		t.Fatalf("Check() unexpected error: %v", err)
	}
	checks := make(map[string]bool, len(violations))
	for _, v := range violations {
		checks[v.Check] = true
	}
	for _, want := range []string{"credited_without_entry", "ledger_balance_mismatch", "reorg_resolution_lag"} {
		if !checks[want] {
			t.Errorf("violations = %v; want check %q to fire", violations, want)
		}
	}
}
