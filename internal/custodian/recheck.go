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
	cfg    Config
	chain  adapters.Client
	store  Store
	engine *credit.Engine
}

func NewRechecker(cfg Config, c adapters.Client, st Store, e *credit.Engine) *Rechecker {
	return &Rechecker{cfg: cfg, chain: c, store: st, engine: e}
}

// Run polls until ctx is done.
func (r *Rechecker) Run(ctx context.Context) error {
	if r.cfg.PollInterval <= 0 {
		return fmt.Errorf("rechecker: poll interval must be positive")
	}
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
	configs, err := r.configs(ctx)
	if err != nil {
		return err
	}
	if err := r.verifyTracked(ctx, head, configs); err != nil {
		return err
	}
	return r.resolveReorged(ctx, head, configs)
}

func (r *Rechecker) verifyTracked(ctx context.Context, head uint64, configs map[string]AssetConfig) error {
	tracked, err := r.store.TrackedDeposits(ctx, string(r.cfg.ChainID))
	if err != nil {
		return fmt.Errorf("rechecker: tracked deposits: %w", err)
	}
	for _, d := range tracked {
		cfg, ok := configs[d.Asset]
		if !ok || d.Height > head {
			continue
		}
		canonical, found, err := r.chain.BlockHash(ctx, d.Height)
		if err != nil {
			// RPC failure is not reorg evidence.
			return fmt.Errorf("rechecker: canonical hash at %d: %w", d.Height, err)
		}
		if !found || canonical != d.BlockHash {
			if err := r.engine.ApplyReorg(ctx, d.TransferID, head); err != nil {
				return fmt.Errorf("rechecker: reorg out %s: %w", d.TransferID, err)
			}
			continue
		}
		depth := head - d.Height + 1
		switch {
		case d.State == domain.StatePending && depth >= cfg.NCredit:
			if err := r.engine.Apply(ctx, d.TransferID, domain.EventDepthReached); err != nil {
				return fmt.Errorf("rechecker: credit %s: %w", d.TransferID, err)
			}
		case d.State == domain.StateCredited && depth >= cfg.NFinalize:
			if err := r.engine.Apply(ctx, d.TransferID, domain.EventFinalityReached); err != nil {
				return fmt.Errorf("rechecker: finalize %s: %w", d.TransferID, err)
			}
		}
	}
	return nil
}

func (r *Rechecker) resolveReorged(ctx context.Context, head uint64, configs map[string]AssetConfig) error {
	reorged, err := r.store.ReorgedDeposits(ctx, string(r.cfg.ChainID))
	if err != nil {
		return fmt.Errorf("rechecker: reorged deposits: %w", err)
	}
	for _, d := range reorged {
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
		cfg, ok := configs[d.Asset]
		if !ok || head-*d.Height < cfg.ReorgWindow {
			continue
		}
		credited, err := r.store.HasCredit(ctx, d.TransferID)
		if err != nil {
			return fmt.Errorf("rechecker: credit check %s: %w", d.TransferID, err)
		}
		event := domain.EventWindowExpiredUncredited
		if credited {
			event = domain.EventWindowExpiredCredited
		}
		if err := r.engine.Apply(ctx, d.TransferID, event); err != nil {
			return fmt.Errorf("rechecker: expire %s: %w", d.TransferID, err)
		}
	}
	return nil
}

func (r *Rechecker) configs(ctx context.Context) (map[string]AssetConfig, error) {
	list, err := r.store.AssetConfigs(ctx, string(r.cfg.ChainID))
	if err != nil {
		return nil, fmt.Errorf("rechecker: asset configs: %w", err)
	}
	configs := make(map[string]AssetConfig, len(list))
	for _, c := range list {
		configs[c.Asset] = c
	}
	return configs, nil
}
