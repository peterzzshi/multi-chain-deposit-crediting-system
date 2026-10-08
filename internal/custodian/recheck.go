package custodian

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/domain"
)

// Rechecker verifies custodian deposits and resolves reorgs.
type Rechecker struct {
	cfg          Config
	chain        adapters.Client
	store        Store
	engine       *credit.Engine
	assetConfigs map[string]domain.AssetPolicy // asset -> config, pre-filtered for this chain + custodian mode
}

func NewRechecker(cfg Config, c adapters.Client, st Store, e *credit.Engine, assetConfigs map[string]domain.AssetPolicy) *Rechecker {
	return &Rechecker{
		cfg:          cfg,
		chain:        c,
		store:        st,
		engine:       e,
		assetConfigs: assetConfigs,
	}
}

// Run polls until ctx is done.
func (r *Rechecker) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := r.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("rechecker tick failed", "chain", r.cfg.ChainID, "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Tick verifies tracked deposits, then resolves reorged deposits.
func (r *Rechecker) Tick(ctx context.Context) error {
	head, err := r.chain.Head(ctx)
	if err != nil {
		return fmt.Errorf("rechecker: head: %w", err)
	}
	if err := r.verifyTracked(ctx, head); err != nil {
		return err
	}
	return r.resolveReorged(ctx, head)
}

func (r *Rechecker) verifyTracked(ctx context.Context, head uint64) error {
	tracked, err := r.store.TrackedDeposits(ctx, string(r.cfg.ChainID))
	if err != nil {
		return fmt.Errorf("rechecker: tracked deposits: %w", err)
	}
	processor := credit.NewDepositDepthProcessor(r.engine, r.assetConfigs)
	for _, d := range tracked {
		if _, ok := r.assetConfigs[d.Asset]; !ok || d.Height > head {
			continue
		}
		// Custodian-specific: verify block hash (reorg detection)
		canonical, found, err := r.chain.BlockHash(ctx, d.Height)
		if err != nil {
			// RPC failure is not reorg evidence.
			return fmt.Errorf("rechecker: canonical hash at %d: %w", d.Height, err)
		}
		if !found || canonical != d.BlockHash {
			if err := r.engine.ApplyReorg(ctx, d.TransferID, head); err != nil {
				// One poisoned deposit must not starve the rest of the chain.
				slog.ErrorContext(ctx, "rechecker: reorg out failed",
					"chain", r.cfg.ChainID, "transfer", d.TransferID, "err", err)
			}
			continue
		}
		// Shared depth-based progression logic
		if _, err := processor.ProcessTrackedDepth(ctx, d, head); err != nil {
			slog.ErrorContext(ctx, "rechecker: depth processing failed",
				"chain", r.cfg.ChainID, "transfer", d.TransferID, "err", err)
		}
	}
	return nil
}

func (r *Rechecker) resolveReorged(ctx context.Context, head uint64) error {
	reorged, err := r.store.ReorgedDeposits(ctx, string(r.cfg.ChainID))
	if err != nil {
		return fmt.Errorf("rechecker: reorged deposits: %w", err)
	}
	processor := credit.NewDepositDepthProcessor(r.engine, r.assetConfigs)
	for _, d := range reorged {
		// Custodian-specific: try to locate tx on chain and reinclude
		if d.TxHash != "" {
			loc, found, err := r.chain.TxByHash(ctx, d.TxHash)
			if err != nil {
				return fmt.Errorf("rechecker: locate tx %s: %w", d.TxHash, err)
			}
			if found {
				if err := r.store.ReincludeDeposit(ctx, d.TransferID, loc.Height, loc.Hash); err != nil {
					return fmt.Errorf("rechecker: reinclude %s: %w", d.TransferID, err)
				}
				if err := r.engine.Apply(ctx, d.TransferID, domain.EventReincluded); err != nil {
					return fmt.Errorf("rechecker: reinclude %s: %w", d.TransferID, err)
				}
				continue
			}
		}
		if d.State == domain.StateReversed {
			continue // terminal unless re-included; no second expiry
		}
		if d.Height == nil {
			// Recover rows whose reorg marker was not written.
			if err := r.store.MarkReorged(ctx, d.TransferID, head); err != nil {
				return fmt.Errorf("rechecker: repair reorg mark %s: %w", d.TransferID, err)
			}
			continue
		}
		// Shared window expiry logic
		result, err := processor.ProcessReorgedExpiry(ctx, d.TransferID, d.Asset, d.Height, head, r.store.HasCredit)
		if err != nil {
			return fmt.Errorf("rechecker: %w", err)
		}
		if result.ShouldApply {
			if err := r.engine.Apply(ctx, d.TransferID, result.Event); err != nil {
				return fmt.Errorf("rechecker: expire %s: %w", d.TransferID, err)
			}
		}
	}
	return nil
}
