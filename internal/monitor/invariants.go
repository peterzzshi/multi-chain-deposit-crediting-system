package monitor

import (
	"context"
	"fmt"
	"math/big"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/domain"
)

type Violation struct {
	Check  string
	Detail string
}

type Mismatch struct {
	Account string
	Asset   string
	Ledger  *big.Int
	Balance *big.Int
}

type Reorged struct {
	TransferID string
	Asset      string
	Height     *uint64
}

type Store interface {
	CreditsMissingEntry(ctx context.Context, networkID string) ([]string, error)
	LedgerBalanceMismatches(ctx context.Context) ([]Mismatch, error)
	ReorgedDeposits(ctx context.Context, networkID string) ([]Reorged, error)
}

type InvariantChecker struct {
	networkID domain.NetworkID
	chain     adapters.Client
	store     Store
	windows   map[string]uint64 // asset -> reorg_window, pre-filtered for this chain
}

const reorgLagFactor = 2

func NewInvariantChecker(networkID domain.NetworkID, c adapters.Client, st Store, windows map[string]uint64) *InvariantChecker {
	return &InvariantChecker{networkID: networkID, chain: c, store: st, windows: windows}
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
	reorged, err := c.store.ReorgedDeposits(ctx, string(c.networkID))
	if err != nil {
		return nil, fmt.Errorf("invariants: reorged deposits: %w", err)
	}
	for _, r := range reorged {
		if r.Height == nil {
			continue
		}
		if window, ok := c.windows[r.Asset]; ok && head-*r.Height > reorgLagFactor*window {
			out = append(out, Violation{Check: "reorg_resolution_lag",
				Detail: "deposit " + r.TransferID + " still REORGED long past its reorg window"})
		}
	}
	return out, nil
}
