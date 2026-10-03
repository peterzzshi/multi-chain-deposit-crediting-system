// worker runs the background loops: the custodian re-checker, the
// reconciliation poller + solvency check (when CUSTODIAN_API_URL is set),
// the exposure monitor, and the runtime invariant checker.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"deposit-crediting/internal/adapters/chain/httpclient"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/custodian"
	"deposit-crediting/internal/custodian/httpprovider"
	"deposit-crediting/internal/risk"
	"deposit-crediting/internal/store"
	"deposit-crediting/internal/store/ent"

	_ "github.com/lib/pq"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	chainID := env("CHAIN_ID", "stubchain")
	poll := envDuration("POLL_INTERVAL", 2*time.Second)

	db, err := ent.Open("postgres", env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/deposit_crediting?sslmode=disable"))
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()
	if err := db.Schema.Create(context.Background()); err != nil {
		fatal("migrate schema", err)
	}

	chainClient := httpclient.New(env("CHAIN_API_URL", "http://localhost:9100"))
	engine := credit.NewEngine(store.New(db))
	custodianStore := store.NewCustodianStore(db)
	riskStore := store.NewRiskStore(db)
	custodianCfg := custodian.Config{ChainID: chainID, Provider: env("PROVIDER", "custodianA"), PollInterval: poll}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	run := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(ctx); err != nil && ctx.Err() == nil {
				slog.Error("worker component stopped", "component", name, "err", err)
				cancel()
			}
		}()
	}

	run("rechecker", custodian.NewRechecker(custodianCfg, chainClient, custodianStore, engine).Run)
	run("exposure-monitor", risk.NewMonitor(risk.Config{ChainID: chainID, PollInterval: poll}, riskStore).Run)

	invariants := risk.NewInvariantChecker(chainID, chainClient, riskStore)
	run("invariant-checker", func(ctx context.Context) error {
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
			case <-time.After(poll):
			}
		}
	})

	if providerURL := os.Getenv("CUSTODIAN_API_URL"); providerURL != "" {
		provider := httpprovider.New(providerURL)
		ingestor := custodian.NewIngestor(custodianCfg, chainClient, custodianStore)
		solvency := custodian.NewSolvencyChecker(custodianCfg, provider, custodianStore, poll)
		reconciler := custodian.NewReconciler(provider, ingestor,
			envDuration("RECONCILE_INTERVAL", 10*time.Second),
			envDuration("RECONCILE_OVERLAP", 2*time.Minute)).WithSolvency(solvency)
		run("reconciler", reconciler.Run)
		slog.Info("reconciliation enabled", "provider", providerURL)
	} else {
		slog.Info("reconciliation disabled; set CUSTODIAN_API_URL to enable")
	}

	slog.Info("worker starting", "chain", chainID)
	wg.Wait()
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func fatal(what string, err error) {
	slog.Error(what, "err", err)
	os.Exit(1)
}
