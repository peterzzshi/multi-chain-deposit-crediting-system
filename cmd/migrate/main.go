// migrate applies the prototype database schema before application services start.
package main

import (
	"context"
	"log/slog"
	"os"

	"deposit-crediting/internal/config"
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
	if err := db.Schema.Create(context.Background()); err != nil {
		fatal("migrate schema", err)
	}
	slog.Info("database schema is ready")
}

func fatal(what string, err error) {
	slog.Error(what, "err", err)
	os.Exit(1)
}
