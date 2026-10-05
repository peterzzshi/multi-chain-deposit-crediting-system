// scanner runs the self-built ingest path against a chain node and PostgreSQL.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
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
	if err := store.UpsertAssetConfigs(context.Background(), db, assets); err != nil {
		fatal("apply assets config", err)
	}

	chainID := config.RequireEnv("CHAIN_ID")
	engine := credit.NewEngine(store.New(db))
	sc := scanner.New(scanner.Config{
		ChainID:      domain.NetworkID(chainID),
		StartHeight:  config.RequireUint64("START_HEIGHT"),
		MaxBatch:     config.RequireUint64("MAX_BATCH"),
		PollInterval: config.RequireDuration("POLL_INTERVAL"),
	}, adapters.New(config.RequireEnv("CHAIN_API_URL")), store.NewScannerStore(db), engine)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("scanner starting", "chain", chainID)
	if err := sc.Run(ctx); err != nil && ctx.Err() == nil {
		fatal("scanner stopped", err)
	}
}

func fatal(what string, err error) {
	slog.Error(what, "err", err)
	os.Exit(1)
}
