//go:build integration

// Package storetest provides the shared Postgres fixture for integration
// tests: connect, migrate, and clean every table on test completion.
package storetest

import (
	"context"
	"testing"
	"time"

	"deposit-crediting/internal/store/ent"

	_ "github.com/lib/pq"
)

// OpenDB connects to the docker-compose Postgres (`make itest`) and
// migrates the schema. The test skips when the database is unreachable.
func OpenDB(t *testing.T) *ent.Client {
	t.Helper()
	client, err := ent.Open("postgres", "postgres://postgres:postgres@localhost:5432/deposit_crediting?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Skipf("postgres unavailable, skipping integration test: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Schema.Create(ctx); err != nil {
		t.Skipf("postgres unreachable at localhost:5432, skipping integration test: %v", err)
	}
	t.Cleanup(func() {
		for _, del := range []func(context.Context) (int, error){
			client.LedgerEntry.Delete().Exec,
			client.AccountBalance.Delete().Exec,
			client.Deposit.Delete().Exec,
			client.SourceEvent.Delete().Exec,
			client.DepositAddress.Delete().Exec,
			client.AssetConfig.Delete().Exec,
			client.CanonicalBlock.Delete().Exec,
			client.ChainCursor.Delete().Exec,
		} {
			if _, err := del(context.Background()); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
		if err := client.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
	})
	return client
}
