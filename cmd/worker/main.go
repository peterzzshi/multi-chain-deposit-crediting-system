// worker runs rechecking, reconciliation, risk, and invariant loops.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/config"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/custodian"
	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/monitor"
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

	custodianStore := store.NewCustodianStore(db)
	monitorStore := store.NewMonitorStore(db)

	// Build tier amounts for credit engine
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

	// Per-chain wiring, built once and shared by every loop below.
	perChain := make([]chainWiring, len(chains))
	for i, chain := range chains {
		perChain[i] = newChainWiring(chain, assetMap)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	run := func(name string, fn func(context.Context) error) {
		wg.Go(func() {
			if err := fn(ctx); err != nil && ctx.Err() == nil {
				slog.Error("worker component stopped", "component", name, "err", err)
				cancel()
			}
		})
	}

	for i, chain := range chains {
		w := perChain[i]

		run(fmt.Sprintf("rechecker-%s", chain.NetworkID),
			custodian.NewRechecker(w.custodianCfg, w.chainClient, custodianStore, engine, w.allAssetConfigs).Run)

		// Invariant checker: verify system correctness
		allChainAssets := assetMap.ForChain(chain.NetworkID)
		windows := make(map[string]uint64)
		for _, ac := range allChainAssets {
			windows[ac.Asset] = ac.ReorgWindow
		}

		invariants := monitor.NewInvariantChecker(chain.NetworkID, w.chainClient, monitorStore, windows)
		run(fmt.Sprintf("invariant-checker-%s", chain.NetworkID), func(ctx context.Context) error {
			ticker := time.NewTicker(chain.PollInterval)
			defer ticker.Stop()
			for {
				violations, err := invariants.Check(ctx)
				if err != nil && ctx.Err() == nil {
					slog.Error("invariant check failed", "chain", chain.NetworkID, "err", err)
				}
				for _, v := range violations {
					slog.Warn("INVARIANT VIOLATION", "chain", chain.NetworkID, "check", v.Check, "detail", v.Detail)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
				}
			}
		})
	}

	if providerURL := os.Getenv("CUSTODIAN_API_URL"); providerURL != "" {
		provider := custodian.NewHTTPProvider(providerURL)
		for i, chain := range chains {
			w := perChain[i]
			ingestor := custodian.NewIngestor(w.custodianCfg, w.chainClient, custodianStore, engine, w.allAssetConfigs)
			reconciler := custodian.NewReconciler(w.custodianCfg, provider, ingestor, custodianStore, w.custodianAssetList,
				time.Duration(config.RequirePositiveUint64("RECONCILE_INTERVAL_MS"))*time.Millisecond,
				time.Duration(config.RequireUint64("RECONCILE_OVERLAP_MS"))*time.Millisecond)
			run(fmt.Sprintf("reconciler-%s", chain.NetworkID), reconciler.Run)
		}
		slog.Info("reconciliation enabled", "provider", providerURL, "chains", len(chains))
	} else {
		slog.Info("reconciliation disabled; set CUSTODIAN_API_URL to enable")
	}

	slog.Info("worker starting", "chains", len(chains))
	wg.Wait()
}

type chainWiring struct {
	chainClient        adapters.Client
	custodianCfg       custodian.Config
	allAssetConfigs    map[string]domain.AssetPolicy
	custodianAssetList []string
}

func newChainWiring(chain config.ChainConfig, assetMap *config.AssetConfigMap) chainWiring {
	allChainAssets := assetMap.ForChain(chain.NetworkID)
	allAssetConfigs := make(map[string]domain.AssetPolicy, len(allChainAssets))
	custodianAssetList := make([]string, 0, len(allChainAssets))
	for _, ac := range allChainAssets {
		allAssetConfigs[ac.Asset] = domain.AssetPolicy{
			Asset:       ac.Asset,
			MinAmount:   ac.MinAmount,
			NCredit:     ac.NCredit,
			NFinalize:   ac.NFinalize,
			ReorgWindow: ac.ReorgWindow,
			ExposureCap: ac.ExposureCap,
		}
		if ac.Mode == "custodian" {
			custodianAssetList = append(custodianAssetList, ac.Asset)
		}
	}
	return chainWiring{
		chainClient: adapters.New(chain.ChainAPIURL),
		custodianCfg: custodian.Config{
			ChainID:      chain.NetworkID,
			Provider:     config.RequireEnv("PROVIDER"),
			PollInterval: chain.PollInterval,
		},
		allAssetConfigs:    allAssetConfigs,
		custodianAssetList: custodianAssetList,
	}
}

func fatal(what string, err error) {
	slog.Error(what, "err", err)
	os.Exit(1)
}
