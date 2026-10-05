// chain-node runs the chain node JSON-RPC API (stub implementation for testing).
package main

import (
	"log/slog"
	"net/http"
	"os"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/config"
)

func main() {
	addr := config.RequireEnv("LISTEN_ADDR")
	slog.Info("chain-node listening", "addr", addr)
	if err := http.ListenAndServe(addr, adapters.Handler(adapters.NewChain())); err != nil {
		slog.Error("chain-node exited", "err", err)
		os.Exit(1)
	}
}
