package custodian

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"deposit-crediting/internal/errs"
)

// Reconciler polls the custodian API and feeds claims through the ingestor.
type Reconciler struct {
	cfg      Config
	provider Provider
	ing      *Ingestor
	store    Store
	assets   []string // assets to check for this chain
	interval time.Duration
	overlap  time.Duration
	since    time.Time // in-memory; zero on boot = idempotent full backfill
}

func NewReconciler(cfg Config, p Provider, ing *Ingestor, st Store, assets []string, interval, overlap time.Duration) *Reconciler {
	return &Reconciler{
		cfg:      cfg,
		provider: p,
		ing:      ing,
		store:    st,
		assets:   assets,
		interval: interval,
		overlap:  overlap,
	}
}

// Run polls until ctx is done; tick errors are logged and retried.
func (r *Reconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if err := r.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("reconciler tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Tick fetches claims since the last poll (minus the overlap) and
// reprocesses them; the Ingestor's dedup makes overlap reprocessing safe.
// After processing claims, verifies vault solvency.
func (r *Reconciler) Tick(ctx context.Context) error {
	// Stamp before the fetch: a claim observed during the round trip must
	// fall on or after the next window's start, not before it.
	fetchedAt := time.Now()
	claims, err := r.provider.FetchDeposits(ctx, r.since.Add(-r.overlap))
	if err != nil {
		return fmt.Errorf("reconcile: fetch deposits: %w", err)
	}
	for _, cl := range claims {
		if err := r.ing.Handle(ctx, cl); err != nil {
			if errors.Is(err, errs.ErrClaimNotOnChain) {
				continue // node lag; the next overlapping poll retries it
			}
			return fmt.Errorf("reconcile: claim %s: %w", cl.ProviderEventID, err)
		}
	}
	// Advance only once every claim is handled, so a mid-batch error
	// re-fetches the unprocessed remainder instead of skipping it.
	r.since = fetchedAt

	// Verify solvency: ledger totals must not exceed vault totals.
	for _, asset := range r.assets {
		ledger, err := r.store.LedgerTotal(ctx, string(r.cfg.ChainID), asset)
		if err != nil {
			return fmt.Errorf("reconcile: ledger total %s: %w", asset, err)
		}
		vault, err := r.provider.FetchVaultTotal(ctx, string(r.cfg.ChainID), asset)
		if err != nil {
			return fmt.Errorf("reconcile: vault total %s: %w", asset, err)
		}
		if ledger.Cmp(vault) > 0 {
			slog.Error("vault solvency discrepancy",
				"chain", r.cfg.ChainID, "asset", asset,
				"ledger", ledger, "vault", vault)
		}
	}
	return nil
}
