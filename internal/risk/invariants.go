package risk

import (
	"context"
	"fmt"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/domain"
)

type Violation struct {
	Check  string
	Detail string
}

type InvariantChecker struct {
	networkID domain.NetworkID
	chain     adapters.Client
	store     Store
}

const reorgLagFactor = 2

func NewInvariantChecker(networkID domain.NetworkID, c adapters.Client, st Store) *InvariantChecker {
	return &InvariantChecker{networkID: networkID, chain: c, store: st}
}

func (c *InvariantChecker) Check(ctx context.Context) ([]Violation, error) {
	var out []Violation

	missing, err := c.store.CreditsMissingEntry(ctx, string(c.networkID))
	if err != nil {
		return nil, fmt.Errorf("invariants: credits missing entry: %w", err)
	}
	for _, id := range missing {
		out = append(out, Violation{Check: "credited_without_entry",
			Detail: "deposit " + id + " is CREDITED/FINALIZED but has no credit ledger entry"})
	}

	mismatches, err := c.store.LedgerBalanceMismatches(ctx)
	if err != nil {
		return nil, fmt.Errorf("invariants: ledger vs balance: %w", err)
	}
	for _, m := range mismatches {
		out = append(out, Violation{Check: "ledger_balance_mismatch",
			Detail: "account " + m.Account + "/" + m.Asset + ": ledger says " + m.Ledger.String() + ", balance row says " + m.Balance.String()})
	}

	head, err := c.chain.Head(ctx)
	if err != nil {
		return nil, fmt.Errorf("invariants: head: %w", err)
	}
	windows, err := c.store.Windows(ctx, string(c.networkID))
	if err != nil {
		return nil, fmt.Errorf("invariants: reorg windows: %w", err)
	}
	reorged, err := c.store.ReorgedDeposits(ctx, string(c.networkID))
	if err != nil {
		return nil, fmt.Errorf("invariants: reorged deposits: %w", err)
	}
	for _, r := range reorged {
		if r.Height == nil {
			continue
		}
		if window, ok := windows[r.Asset]; ok && head-*r.Height > reorgLagFactor*window {
			out = append(out, Violation{Check: "reorg_resolution_lag",
				Detail: "deposit " + r.TransferID + " still REORGED long past its reorg window"})
		}
	}
	return out, nil
}
