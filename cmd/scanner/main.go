// scanner runs the self-built ingest path against a chain node API
// (chainstub locally) and PostgreSQL. See docs/manual-verification.md.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"deposit-crediting/internal/adapters/chain/httpclient"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/scanner"
	"deposit-crediting/internal/store"
	"deposit-crediting/internal/store/ent"

	_ "github.com/lib/pq"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	db, err := ent.Open("postgres", env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/deposit_crediting?sslmode=disable"))
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()
	if err := db.Schema.Create(context.Background()); err != nil {
		fatal("migrate schema", err)
	}

	engine := credit.NewEngine(store.New(db))
	sc := scanner.New(scanner.Config{
		ChainID:      env("CHAIN_ID", "stubchain"),
		StartHeight:  envUint64("START_HEIGHT", 1),
		MaxBatch:     envUint64("MAX_BATCH", 100),
		PollInterval: envDuration("POLL_INTERVAL", 500*time.Millisecond),
	}, httpclient.New(env("CHAIN_API_URL", "http://localhost:9100")), store.NewScannerStore(db), engine)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("scanner starting", "chain", env("CHAIN_ID", "stubchain"))
	if err := sc.Run(ctx); err != nil && ctx.Err() == nil {
		fatal("scanner stopped", err)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envUint64(key string, fallback uint64) uint64 {
	if v := os.Getenv(key); v != "" {
		var n uint64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
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
