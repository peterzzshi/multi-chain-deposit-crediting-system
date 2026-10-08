package credit

import (
	"context"
	"fmt"

	"deposit-crediting/internal/domain"
)

// DepositDepthProcessor handles depth-based state transitions for deposits.
// This logic was previously duplicated in scanner.advanceDepth() and rechecker.verifyTracked().
type DepositDepthProcessor struct {
	engine       *Engine
	assetConfigs map[string]domain.AssetPolicy
}

func NewDepositDepthProcessor(engine *Engine, assetConfigs map[string]domain.AssetPolicy) *DepositDepthProcessor {
	return &DepositDepthProcessor{
		engine:       engine,
		assetConfigs: assetConfigs,
	}
}

// ProcessTrackedDepth applies depth-based state transitions for a tracked deposit.
// Returns true if a transition was applied, false if no action was taken.
func (p *DepositDepthProcessor) ProcessTrackedDepth(ctx context.Context, d domain.TrackedDeposit, head uint64) (bool, error) {
	cfg, ok := p.assetConfigs[d.Asset]
	if !ok || d.Height > head {
		return false, nil
	}

	depth := head - d.Height + 1

	switch {
	case d.State == domain.StatePending && depth >= cfg.NCredit:
		if err := p.engine.Apply(ctx, d.TransferID, domain.EventDepthReached); err != nil {
			return false, fmt.Errorf("depth processor: credit %s: %w", d.TransferID, err)
		}
		return true, nil

	case d.State == domain.StateCredited && depth >= cfg.NFinalize:
		if err := p.engine.Apply(ctx, d.TransferID, domain.EventFinalityReached); err != nil {
			return false, fmt.Errorf("depth processor: finalize %s: %w", d.TransferID, err)
		}
		return true, nil
	}

	return false, nil
}

// ReorgExpiryResult contains the event to apply for a reorged deposit that's past its window.
type ReorgExpiryResult struct {
	Event       domain.Event
	ShouldApply bool
}

// ProcessReorgedExpiry checks if a reorged deposit is past its reorg window and returns the expiry event.
// This logic was previously duplicated in scanner.expireReorged() and rechecker.resolveReorged().
func (p *DepositDepthProcessor) ProcessReorgedExpiry(ctx context.Context, transferID, asset string, reorgedHeight *uint64, head uint64, hasCredit func(context.Context, string) (bool, error)) (ReorgExpiryResult, error) {
	if reorgedHeight == nil {
		return ReorgExpiryResult{ShouldApply: false}, nil
	}

	cfg, ok := p.assetConfigs[asset]
	if !ok || head-*reorgedHeight < cfg.ReorgWindow {
		return ReorgExpiryResult{ShouldApply: false}, nil
	}

	credited, err := hasCredit(ctx, transferID)
	if err != nil {
		return ReorgExpiryResult{}, fmt.Errorf("reorg expiry: credit check %s: %w", transferID, err)
	}

	event := domain.EventWindowExpiredUncredited
	if credited {
		event = domain.EventWindowExpiredCredited
	}

	return ReorgExpiryResult{Event: event, ShouldApply: true}, nil
}
