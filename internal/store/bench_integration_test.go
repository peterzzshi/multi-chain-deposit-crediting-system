//go:build integration

// Benchmarks for the capacity estimation's hot paths (P5): the credit
// write path. Numbers feed technical-decisions.md §Capacity Estimation.
package store_test

import (
	"context"
	"fmt"
	"math/big"
	"sync/atomic"
	"testing"

	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/domain/deposit"
	"deposit-crediting/internal/store"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
	"deposit-crediting/internal/store/storetest"
)

func BenchmarkEngineCredit(b *testing.B) {
	client := storetest.OpenDB(b)
	ctx := context.Background()
	engine := credit.NewEngine(store.New(client))

	state, _, err := deposit.Transition(deposit.StateNone, deposit.EventObserved)
	if err != nil {
		b.Fatalf("Transition(OBSERVED): %v", err)
	}
	for i := 0; i < b.N; i++ {
		if _, err := client.Deposit.Create().
			SetTransferID(fmt.Sprintf("evm:0xc%08x:native", i)).
			SetChain("evm").SetAsset("ETH").SetAccount("alice").SetAddress("0xdeposit").
			SetAmount("100").SetMode(entdeposit.ModeSelfBuilt).SetState(entdeposit.State(state)).
			SetBlockHeight(1).SetBlockHash("0xb1").
			Save(ctx); err != nil {
			b.Fatalf("open deposit %d: %v", i, err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := engine.Apply(ctx, fmt.Sprintf("evm:0xc%08x:native", i), deposit.EventDepthReached); err != nil {
			b.Fatalf("credit %d: %v", i, err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "credits/s")
}

// Credits parallelize across accounts: per-(account, asset) serialization
// (ADR 0003) bounds one account, not the system.
func BenchmarkEngineCreditParallel(b *testing.B) {
	client := storetest.OpenDB(b)
	ctx := context.Background()
	engine := credit.NewEngine(store.New(client))

	state, _, err := deposit.Transition(deposit.StateNone, deposit.EventObserved)
	if err != nil {
		b.Fatalf("Transition(OBSERVED): %v", err)
	}
	for i := 0; i < b.N; i++ {
		if _, err := client.Deposit.Create().
			SetTransferID(fmt.Sprintf("evm:0xp%08x:native", i)).
			SetChain("evm").SetAsset("ETH").SetAccount(fmt.Sprintf("acct-%d", i%16)).SetAddress("0xdeposit").
			SetAmount("100").SetMode(entdeposit.ModeSelfBuilt).SetState(entdeposit.State(state)).
			SetBlockHeight(1).SetBlockHash("0xb1").
			Save(ctx); err != nil {
			b.Fatalf("open deposit %d: %v", i, err)
		}
	}

	var next atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := next.Add(1) - 1
			if err := engine.Apply(ctx, fmt.Sprintf("evm:0xp%08x:native", i), deposit.EventDepthReached); err != nil {
				b.Errorf("credit %d: %v", i, err)
			}
		}
	})
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "credits/s")
}

func BenchmarkEngineDebit(b *testing.B) {
	client := storetest.OpenDB(b)
	ctx := context.Background()
	engine := credit.NewEngine(store.New(client))

	state, _, err := deposit.Transition(deposit.StateNone, deposit.EventObserved)
	if err != nil {
		b.Fatalf("Transition(OBSERVED): %v", err)
	}
	if _, err := client.Deposit.Create().
		SetTransferID("evm:0xstake:native").
		SetChain("evm").SetAsset("ETH").SetAccount("alice").SetAddress("0xdeposit").
		SetAmount("1000000000000").SetMode(entdeposit.ModeSelfBuilt).SetState(entdeposit.State(state)).
		SetBlockHeight(1).SetBlockHash("0xb1").
		Save(ctx); err != nil {
		b.Fatalf("open stake deposit: %v", err)
	}
	if err := engine.Apply(ctx, "evm:0xstake:native", deposit.EventDepthReached); err != nil {
		b.Fatalf("stake credit: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := engine.Debit(ctx, "alice", "ETH", big.NewInt(1), fmt.Sprintf("w:%08x", i)); err != nil {
			b.Fatalf("debit %d: %v", i, err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "debits/s")
}
