// scanner runs the self-built ingest path against a chain node and PostgreSQL.
package main

import (
	"context"
	"log/slog"
	"math/big"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/config"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/scanner"
	"deposit-crediting/internal/store"
	"deposit-crediting/internal/store/ent"

	_ "github.com/lib/pq"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	db, err := ent.Open("postgres", config.RequireEnv("DATABASE_URL"))
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()

	assets, err := config.LoadAssets(config.RequireEnv("ASSETS_CONFIG"))
	if err != nil {
		fatal("load assets config", err)
	}
	assetMap, err := config.NewAssetConfigMap(assets)
	if err != nil {
		fatal("build asset config map", err)
	}

	chains, err := config.LoadChains(config.RequireEnv("CHAINS_CONFIG"))
	if err != nil {
		fatal("load chains config", err)
	}

	creditStore := store.New(db)
	for _, chain := range chains {
		chainAssets := assetMap.ForChain(chain.NetworkID)
		tiers := make(map[string]*big.Int)
		for _, ac := range chainAssets {
			if ac.TierAmount != nil {
				tiers[ac.Asset] = ac.TierAmount
			}
		}
		creditStore.SetTierAmounts(string(chain.NetworkID), tiers)
	}

	engine := credit.NewEngine(creditStore)
	scannerStore := store.NewScannerStore(db)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for _, chain := range chains {
		wg.Add(1)
		go func(chain config.ChainConfig) {
			defer wg.Done()

			chainAssets := assetMap.ForChainAndMode(chain.NetworkID, "self_built")
			assetConfigs := make(map[string]domain.AssetPolicy, len(chainAssets))
			for _, ac := range chainAssets {
				assetConfigs[ac.Asset] = domain.AssetPolicy{
					Asset:       ac.Asset,
					MinAmount:   ac.MinAmount,
					NCredit:     ac.NCredit,
					NFinalize:   ac.NFinalize,
					ReorgWindow: ac.ReorgWindow,
				}
			}

			sc := scanner.New(scanner.Config{
				ChainID:      chain.NetworkID,
				StartHeight:  config.RequireUint64("START_HEIGHT"),
				MaxBatch:     config.RequirePositiveUint64("MAX_BATCH"),
				PollInterval: chain.PollInterval,
			}, adapters.New(chain.ChainAPIURL), scannerStore, engine, assetConfigs)

			slog.Info("scanner starting", "chain", chain.NetworkID, "assets", len(assetConfigs))
			if err := sc.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("scanner stopped", "chain", chain.NetworkID, "err", err)
				cancel()
			}
		}(chain)
	}

	slog.Info("scanners started", "chains", len(chains))
	wg.Wait()
}

func fatal(what string, err error) {
	slog.Error(what, "err", err)
	os.Exit(1)
}
