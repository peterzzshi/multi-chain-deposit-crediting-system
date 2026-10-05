// worker runs rechecking, reconciliation, risk, and invariant loops.
package main

import (
	"context"
	"fmt"
	"log/slog"
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
	"deposit-crediting/internal/risk"
	"deposit-crediting/internal/store"
	"deposit-crediting/internal/store/ent"

	_ "github.com/lib/pq"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	chainID := config.RequireEnv("CHAIN_ID")
	poll := config.RequireDuration("POLL_INTERVAL")
	if poll <= 0 {
		fatal("invalid POLL_INTERVAL", fmt.Errorf("must be positive"))
	}

	db, err := ent.Open("postgres", config.RequireEnv("DATABASE_URL"))
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()
	assets, err := config.LoadAssets(config.RequireEnv("ASSETS_CONFIG"))
	if err != nil {
		fatal("load assets config", err)
	}
	if err := store.UpsertAssetConfigs(context.Background(), db, assets); err != nil {
		fatal("apply assets config", err)
	}

	chainClient := adapters.New(config.RequireEnv("CHAIN_API_URL"))
	engine := credit.NewEngine(store.New(db))
	custodianStore := store.NewCustodianStore(db)
	riskStore := store.NewRiskStore(db)
	custodianCfg := custodian.Config{ChainID: domain.NetworkID(chainID), Provider: config.RequireEnv("PROVIDER"), PollInterval: poll}

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

	run("rechecker", custodian.NewRechecker(custodianCfg, chainClient, custodianStore, engine).Run)
	run("exposure-monitor", risk.NewMonitor(risk.Config{NetworkID: domain.NetworkID(chainID), PollInterval: poll}, riskStore).Run)

	invariants := risk.NewInvariantChecker(domain.NetworkID(chainID), chainClient, riskStore)
	run("invariant-checker", func(ctx context.Context) error {
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			violations, err := invariants.Check(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Error("invariant check failed", "err", err)
			}
			for _, v := range violations {
				slog.Warn("INVARIANT VIOLATION", "check", v.Check, "detail", v.Detail)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	})

	if providerURL := os.Getenv("CUSTODIAN_API_URL"); providerURL != "" {
		provider := custodian.NewHTTPProvider(providerURL)
		ingestor := custodian.NewIngestor(custodianCfg, chainClient, custodianStore)
		solvency := custodian.NewSolvencyChecker(custodianCfg, provider, custodianStore, poll)
		reconciler := custodian.NewReconciler(provider, ingestor,
			config.RequireDuration("RECONCILE_INTERVAL"),
			config.RequireDuration("RECONCILE_OVERLAP")).WithSolvency(solvency)
		run("reconciler", reconciler.Run)
		slog.Info("reconciliation enabled", "provider", providerURL)
	} else {
		slog.Info("reconciliation disabled; set CUSTODIAN_API_URL to enable")
	}

	slog.Info("worker starting", "chain", chainID)
	wg.Wait()
}

func fatal(what string, err error) {
	slog.Error(what, "err", err)
	os.Exit(1)
}
