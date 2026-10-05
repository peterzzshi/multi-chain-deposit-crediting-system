package custodian

import (
	"context"
	"fmt"
	"log/slog"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"
)

type Ingestor struct {
	cfg   Config
	chain adapters.Client
	store Store
}

func NewIngestor(cfg Config, c adapters.Client, st Store) *Ingestor {
	return &Ingestor{cfg: cfg, chain: c, store: st}
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
	cfg, ok, err := i.store.AssetConfig(ctx, string(i.cfg.ChainID), cl.Asset)
	if err != nil {
		return fmt.Errorf("custodian: asset config %s: %w", cl.Asset, err)
	}
	if !ok {
		return nil
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
	state, _, err := domain.Transition(domain.StateNone, event)
	if err != nil {
		return fmt.Errorf("custodian: initial state: %w", err)
	}
	result, err := i.store.OpenDeposit(ctx, OpenParams{
		TransferID:  id.String(),
		State:       state,
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
	if !result.Created() {
		return nil
	}
	if err := i.store.RecordSourceEvent(ctx, i.cfg.Provider, cl); err != nil {
		return fmt.Errorf("custodian: record source event: %w", err)
	}
	return nil
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
