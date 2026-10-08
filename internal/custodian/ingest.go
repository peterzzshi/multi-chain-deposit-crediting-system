package custodian

import (
	"context"
	"fmt"
	"log/slog"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"
)

type Ingestor struct {
	cfg          Config
	chain        adapters.Client
	store        Store
	engine       *credit.Engine
	assetConfigs map[string]domain.AssetPolicy // asset -> config, pre-filtered for this chain + custodian mode
}

func NewIngestor(cfg Config, c adapters.Client, st Store, e *credit.Engine, assetConfigs map[string]domain.AssetPolicy) *Ingestor {
	return &Ingestor{
		cfg:          cfg,
		chain:        c,
		store:        st,
		engine:       e,
		assetConfigs: assetConfigs,
	}
}

func (i *Ingestor) Handle(ctx context.Context, cl Claim) error {
	if cl.Chain != i.cfg.ChainID {
		return fmt.Errorf("custodian: claim chain %s does not match %s", cl.Chain, i.cfg.ChainID)
	}
	seen, err := i.store.SourceEventSeen(ctx, i.cfg.Provider, cl.ProviderEventID)
	if err != nil {
		return fmt.Errorf("custodian: source event lookup: %w", err)
	}
	if seen {
		return nil
	}
	account, ok, err := i.store.ResolveAddress(ctx, string(i.cfg.ChainID), cl.To)
	if err != nil {
		return fmt.Errorf("custodian: resolve address %s: %w", cl.To, err)
	}
	if !ok {
		return nil
	}
	cfg, ok := i.assetConfigs[cl.Asset]
	if !ok {
		return nil // unsupported or self-built-mode asset
	}
	loc, found, err := i.chain.TxByHash(ctx, cl.TxHash)
	if err != nil {
		return fmt.Errorf("custodian: locate tx %s: %w", cl.TxHash, err)
	}
	if !found {
		return fmt.Errorf("%w: %s tx %s", errs.ErrClaimNotOnChain, cl.Chain, cl.TxHash)
	}
	tr, ok := matchTransfer(loc.Transfers, cl)
	if !ok {
		// Record the rejection as a source event so the dedup check at the top
		// skips this claim on later polls instead of re-warning every time.
		if err := i.store.RecordSourceEvent(ctx, i.cfg.Provider, cl); err != nil {
			return fmt.Errorf("custodian: record rejected claim: %w", err)
		}
		slog.Warn("custodian claim contradicts chain, rejected",
			"chain", cl.Chain, "tx", cl.TxHash, "to", cl.To, "asset", cl.Asset, "amount", cl.Amount)
		return nil
	}
	id, err := tr.LogicalID(cl.Chain)
	if err != nil {
		return err
	}
	event := domain.EventObserved
	if tr.Amount.Cmp(cfg.MinAmount) < 0 {
		event = domain.EventObservedBelowMinimum
	}
	state, err := i.store.OpenDeposit(ctx, domain.OpenDepositParams{
		TransferID:  id.String(),
		Chain:       string(i.cfg.ChainID),
		Asset:       tr.Asset,
		Account:     account,
		Address:     tr.To,
		Amount:      tr.Amount,
		Height:      loc.Height,
		Hash:        loc.Hash,
		TxHash:      cl.TxHash,
		SourceEvent: i.cfg.Provider + ":" + cl.ProviderEventID,
	})
	if err != nil {
		return fmt.Errorf("custodian: open deposit %s: %w", id, err)
	}
	if state != domain.StateCreated {
		return nil
	}
	if err := i.store.RecordSourceEvent(ctx, i.cfg.Provider, cl); err != nil {
		return fmt.Errorf("custodian: record source event: %w", err)
	}
	return i.engine.Apply(ctx, id.String(), event)
}

func matchTransfer(transfers []adapters.Transfer, cl Claim) (adapters.Transfer, bool) {
	var matches []adapters.Transfer
	for _, tr := range transfers {
		if tr.To != cl.To || tr.Asset != cl.Asset || tr.Amount == nil || tr.Amount.Cmp(cl.Amount) != 0 {
			continue
		}
		if cl.Kind != "" && tr.Kind != cl.Kind {
			continue
		}
		if cl.LogIndex != nil && tr.LogIndex != *cl.LogIndex {
			continue
		}
		if cl.TraceIndex != nil && tr.TraceIndex != *cl.TraceIndex {
			continue
		}
		matches = append(matches, tr)
	}
	if len(matches) != 1 {
		return adapters.Transfer{}, false
	}
	return matches[0], true
}
