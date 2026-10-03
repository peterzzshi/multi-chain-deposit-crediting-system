// chainstub runs a stub chain node over HTTP for local runs. Mine blocks
// and trigger reorgs through the /admin endpoints; the app reads chain
// data through the /v1 endpoints. See docs/manual-verification.md.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"deposit-crediting/internal/adapters/chain/chainstub"
	"deposit-crediting/internal/adapters/chain/chaintest"
)

func main() {
	addr := env("LISTEN_ADDR", ":9100")
	slog.Info("chainstub listening", "addr", addr)
	if err := http.ListenAndServe(addr, chainstub.Handler(chaintest.NewChain())); err != nil {
		slog.Error("chainstub exited", "err", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
