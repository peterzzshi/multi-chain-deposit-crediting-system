package custodian

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"time"
)

// Discrepancy is a solvency violation: the ledger says users hold more of
// an asset than the custodian's vault holds (ADR 0004 — the vault is the
// backing for custodian-mode credits).
type Discrepancy struct {
	Chain       string
	Asset       string
	LedgerTotal *big.Int
	VaultTotal  *big.Int
}

// SolvencyChecker compares the platform's ledger totals against the
// custodian's vault totals. It runs on the reconciliation cadence; a
// discrepancy is an alert, never a credit action.
type SolvencyChecker struct {
	cfg      Config
	provider Provider
	store    Store
	interval time.Duration
}

func NewSolvencyChecker(cfg Config, p Provider, st Store, interval time.Duration) *SolvencyChecker {
	return &SolvencyChecker{cfg: cfg, provider: p, store: st, interval: interval}
}

// Run checks until ctx is done; failures are logged and retried.
func (s *SolvencyChecker) Run(ctx context.Context) error {
	for {
		discrepancies, err := s.Check(ctx)
		if err != nil {
			slog.Error("solvency check failed", "chain", s.cfg.ChainID, "err", err)
		}
		for _, d := range discrepancies {
			slog.Error("vault solvency discrepancy",
				"chain", d.Chain, "asset", d.Asset,
				"ledger", d.LedgerTotal, "vault", d.VaultTotal)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.interval):
		}
	}
}

// Check returns one Discrepancy per (chain, asset) where the ledger total
// exceeds the vault total.
func (s *SolvencyChecker) Check(ctx context.Context) ([]Discrepancy, error) {
	configs, err := s.store.AssetConfigs(ctx, s.cfg.ChainID)
	if err != nil {
		return nil, fmt.Errorf("solvency: asset configs: %w", err)
	}
	var out []Discrepancy
	for _, cfg := range configs {
		ledger, err := s.store.LedgerTotal(ctx, s.cfg.ChainID, cfg.Asset)
		if err != nil {
			return nil, fmt.Errorf("solvency: ledger total %s: %w", cfg.Asset, err)
		}
		vault, err := s.provider.FetchVaultTotal(ctx, s.cfg.ChainID, cfg.Asset)
		if err != nil {
			return nil, fmt.Errorf("solvency: vault total %s: %w", cfg.Asset, err)
		}
		if ledger.Cmp(vault) > 0 {
			out = append(out, Discrepancy{
				Chain:       s.cfg.ChainID,
				Asset:       cfg.Asset,
				LedgerTotal: ledger,
				VaultTotal:  vault,
			})
		}
	}
	return out, nil
}
