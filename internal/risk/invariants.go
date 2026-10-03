package risk

import (
	"context"
	"fmt"

	"deposit-crediting/internal/adapters/chain"
)

// Violation is one broken runtime invariant (Q17): the checks mirror the
// test invariants — the same assertions that guard the harness guard
// production.
type Violation struct {
	Check  string
	Detail string
}

// InvariantChecker verifies cross-table invariants the per-transaction
// constraints cannot cover. It runs on the monitor cadence; violations
// are alerts, never automatic actions.
type InvariantChecker struct {
	chainID string
	chain   chain.Client
	store   Store
}

// A reorged deposit left unresolved beyond twice its reorg window means
// the scanner/re-checker is stuck (reversal or drop lag).
const reorgLagFactor = 2

func NewInvariantChecker(chainID string, c chain.Client, st Store) *InvariantChecker {
	return &InvariantChecker{chainID: chainID, chain: c, store: st}
}

// Check runs all invariant checks and returns every violation found.
func (c *InvariantChecker) Check(ctx context.Context) ([]Violation, error) {
	var out []Violation

	missing, err := c.store.CreditsMissingEntry(ctx, c.chainID)
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
	windows, err := c.store.Windows(ctx, c.chainID)
	if err != nil {
		return nil, fmt.Errorf("invariants: reorg windows: %w", err)
	}
	reorged, err := c.store.ReorgedDeposits(ctx, c.chainID)
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
