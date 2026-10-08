package main

import (
	"log/slog"
	"net/http"
	"os"

	"deposit-crediting/internal/chaintest"
	"deposit-crediting/internal/config"
)

func main() {
	addr := config.RequireEnv("LISTEN_ADDR")
	slog.Info("chain-node listening", "addr", addr)
	if err := http.ListenAndServe(addr, chaintest.Handler(chaintest.NewChain())); err != nil {
		slog.Error("chain-node exited", "err", err)
		os.Exit(1)
	}
}
