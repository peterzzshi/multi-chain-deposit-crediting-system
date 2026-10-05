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
	provider Provider
	ing      *Ingestor
	solvency *SolvencyChecker
	interval time.Duration
	overlap  time.Duration
	since    time.Time // in-memory; zero on boot = idempotent full backfill
}

func NewReconciler(p Provider, ing *Ingestor, interval, overlap time.Duration) *Reconciler {
	return &Reconciler{provider: p, ing: ing, interval: interval, overlap: overlap}
}

func (r *Reconciler) WithSolvency(s *SolvencyChecker) *Reconciler {
	r.solvency = s
	return r
}

// Run polls until ctx is done; tick errors are logged and retried.
func (r *Reconciler) Run(ctx context.Context) error {
	if r.interval <= 0 {
		return fmt.Errorf("reconcile: interval must be positive")
	}
	if r.overlap < 0 {
		return fmt.Errorf("reconcile: overlap must not be negative")
	}
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
func (r *Reconciler) Tick(ctx context.Context) error {
	claims, err := r.provider.FetchDeposits(ctx, r.since.Add(-r.overlap))
	if err != nil {
		return fmt.Errorf("reconcile: fetch deposits: %w", err)
	}
	r.since = time.Now()
	for _, cl := range claims {
		if err := r.ing.Handle(ctx, cl); err != nil {
			if errors.Is(err, errs.ErrClaimNotOnChain) {
				continue // node lag; the next overlapping poll retries it
			}
			return fmt.Errorf("reconcile: claim %s: %w", cl.ProviderEventID, err)
		}
	}
	if r.solvency != nil {
		discrepancies, err := r.solvency.Check(ctx)
		if err != nil {
			return fmt.Errorf("reconcile: solvency: %w", err)
		}
		for _, d := range discrepancies {
			slog.Error("vault solvency discrepancy",
				"chain", d.Chain, "asset", d.Asset,
				"ledger", d.LedgerTotal, "vault", d.VaultTotal)
		}
	}
	return nil
}
