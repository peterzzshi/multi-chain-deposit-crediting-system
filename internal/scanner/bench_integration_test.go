//go:build integration

// Scanner throughput benchmarks (P5): block filtering at chain scale and
// deep-reorg rewind+replay. Numbers feed technical-decisions.md
// §Capacity Estimation.
package scanner_test

import (
	"context"
	"fmt"
	"math/big"
	"testing"

	"deposit-crediting/internal/adapters/chain"
	"deposit-crediting/internal/adapters/chain/chaintest"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/scanner"
	"deposit-crediting/internal/store"
	"deposit-crediting/internal/store/ent"
	"deposit-crediting/internal/store/storetest"
)

const (
	benchBlocks       = 20
	benchTxPerBlock   = 3000
	benchWatchedAddrs = 10000
)

// A full pass over a pre-built chain of ~3,000-tx blocks against a
// 10k-address watch table, with the capacity scenario's 0.1% match rate.
func BenchmarkScannerFilter(b *testing.B) {
	client := storetest.OpenDB(b)
	ctx := context.Background()
	engine := credit.NewEngine(store.New(client))
	c := chaintest.NewChain()
	sc := scanner.New(scanner.Config{ChainID: chainID, StartHeight: 1, MaxBatch: 100}, c, store.NewScannerStore(client), engine)

	if _, err := client.AssetConfig.Create().
		SetChain(chainID).SetAsset("ETH").SetDecimals(18).
		SetMode("self_built").SetMinAmount("10").
		SetNCredit(3).SetNFinalize(5).SetReorgWindow(4).
		Save(ctx); err != nil {
		b.Fatalf("seed asset config: %v", err)
	}
	builders := make([]*ent.DepositAddressCreate, 0, benchWatchedAddrs)
	for i := 0; i < benchWatchedAddrs; i++ {
		builders = append(builders, client.DepositAddress.Create().
			SetAccount(fmt.Sprintf("acct-%d", i)).SetChain(chainID).
			SetAddress(fmt.Sprintf("0xw%05d", i)).SetMode("self_built"))
	}
	if err := client.DepositAddress.CreateBulk(builders...).Exec(ctx); err != nil {
		b.Fatalf("seed addresses: %v", err)
	}

	// 0.1% match rate: 3 watched recipients per 3,000-tx block.
	for height := 0; height < benchBlocks; height++ {
		transfers := make([]chain.Transfer, 0, benchTxPerBlock)
		for j := 0; j < benchTxPerBlock; j++ {
			to := fmt.Sprintf("0xstranger%05d", (height*benchTxPerBlock+j)%20000)
			if j%1000 == 0 {
				to = fmt.Sprintf("0xw%05d", (height+j)%benchWatchedAddrs)
			}
			transfers = append(transfers, chain.Transfer{
				Kind: chain.Native, TxHash: fmt.Sprintf("0xtx%05d%05d", height, j),
				From: "0xexternal", To: to, Asset: "ETH", Amount: big.NewInt(100),
			})
		}
		c.AddBlock(transfers...)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Rewind the cursor so every iteration re-scans the full range;
		// deposits already exist, so this also exercises idempotent
		// reprocessing (the restart path).
		if _, err := client.ChainCursor.Delete().Exec(ctx); err != nil {
			b.Fatalf("reset cursor: %v", err)
		}
		if err := sc.Tick(ctx); err != nil {
			b.Fatalf("Tick(): %v", err)
		}
	}
	b.StopTimer()
	txTotal := float64(b.N) * benchBlocks * benchTxPerBlock
	b.ReportMetric(txTotal/b.Elapsed().Seconds(), "tx/s")
}

// Rewind + replay of a 10-block-deep reorg, the fast chain's expected
// reorg shape.
func BenchmarkScannerReorgReplay(b *testing.B) {
	client := storetest.OpenDB(b)
	ctx := context.Background()
	engine := credit.NewEngine(store.New(client))
	c := chaintest.NewChain()
	sc := scanner.New(scanner.Config{ChainID: chainID, StartHeight: 1, MaxBatch: 100}, c, store.NewScannerStore(client), engine)

	if _, err := client.AssetConfig.Create().
		SetChain(chainID).SetAsset("ETH").SetDecimals(18).
		SetMode("self_built").SetMinAmount("10").
		SetNCredit(3).SetNFinalize(5).SetReorgWindow(20).
		Save(ctx); err != nil {
		b.Fatalf("seed asset config: %v", err)
	}
	if _, err := client.DepositAddress.Create().
		SetAccount("alice").SetChain(chainID).SetAddress(aliceAddr).SetMode("self_built").
		Save(ctx); err != nil {
		b.Fatalf("seed address: %v", err)
	}

	for i := 0; i < 30; i++ {
		c.AddBlock(chain.Transfer{Kind: chain.Native, TxHash: fmt.Sprintf("0xr%d", i), From: "0xexternal", To: aliceAddr, Asset: "ETH", Amount: big.NewInt(100)})
	}
	if err := sc.Tick(ctx); err != nil {
		b.Fatalf("initial Tick(): %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c.Reorg(10)
		for j := 0; j < 10; j++ {
			c.AddBlock(chain.Transfer{Kind: chain.Native, TxHash: fmt.Sprintf("0xr%dn%d", i, j), From: "0xexternal", To: aliceAddr, Asset: "ETH", Amount: big.NewInt(100)})
		}
		b.StartTimer()
		if err := sc.Tick(ctx); err != nil {
			b.Fatalf("replay Tick() %d: %v", i, err)
		}
	}
}
