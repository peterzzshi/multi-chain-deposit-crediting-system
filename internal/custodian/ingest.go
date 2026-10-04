package custodian

import (
	"context"
	"fmt"
	"log/slog"

	"deposit-crediting/internal/adapters/chain"
	"deposit-crediting/internal/domain/deposit"
	"deposit-crediting/internal/errs"
)

// Ingestor processes custodian claims from any channel (webhook delivery,
// reconciliation poll): dedup by source event, verify on-chain, then open
// the deposit. Chain facts decide; the claim only hints (ADR 0004).
type Ingestor struct {
	cfg   Config
	chain chain.Client
	store Store
}

func NewIngestor(cfg Config, c chain.Client, st Store) *Ingestor {
	return &Ingestor{cfg: cfg, chain: c, store: st}
}

// Handle processes one claim. Duplicates and claims for foreign
// addresses/assets are no-ops; claims that cannot be verified on-chain
// return errs.ErrClaimNotOnChain so the caller can retry later; claims
// that contradict the chain are rejected silently (never creditable).
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
	account, ok, err := i.store.ResolveAddress(ctx, i.cfg.ChainID, cl.To)
	if err != nil {
		return fmt.Errorf("custodian: resolve address %s: %w", cl.To, err)
	}
	if !ok {
		return nil
	}
	cfg, ok, err := i.store.AssetConfig(ctx, i.cfg.ChainID, cl.Asset)
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
	event := deposit.EventObserved
	if tr.Amount.Cmp(cfg.MinAmount) < 0 {
		event = deposit.EventObservedBelowMinimum
	}
	state, _, err := deposit.Transition(deposit.StateNone, event)
	if err != nil {
		return fmt.Errorf("custodian: initial state: %w", err)
	}
	if _, err := i.store.OpenDeposit(ctx, OpenParams{
		TransferID:  id.String(),
		State:       state,
		Chain:       i.cfg.ChainID,
		Asset:       tr.Asset,
		Account:     account,
		Address:     tr.To,
		Amount:      tr.Amount, // chain amount is the truth, not the claim's
		Height:      loc.Height,
		Hash:        loc.Hash,
		TxHash:      cl.TxHash,
		SourceEvent: i.cfg.Provider + ":" + cl.ProviderEventID,
	}); err != nil {
		return fmt.Errorf("custodian: open deposit %s: %w", id, err)
	}
	if err := i.store.RecordSourceEvent(ctx, i.cfg.Provider, cl); err != nil {
		return fmt.Errorf("custodian: record source event: %w", err)
	}
	return nil
}

// matchTransfer returns the uniquely identified transfer: every field the
// claim supplies must match, and exactly one candidate may remain —
// an ambiguous claim never credits.
func matchTransfer(transfers []chain.Transfer, cl Claim) (chain.Transfer, bool) {
	var matches []chain.Transfer
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
		return chain.Transfer{}, false
	}
	return matches[0], true
}
