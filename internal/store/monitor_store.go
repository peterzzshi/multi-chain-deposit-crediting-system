package store

import (
	"context"
	"fmt"
	"math/big"

	"entgo.io/ent/dialect/sql"

	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/monitor"
	"deposit-crediting/internal/store/ent"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
	"deposit-crediting/internal/store/ent/ledgerentry"
)

// MonitorStore implements monitor.Store over the ent client.
type MonitorStore struct {
	client *ent.Client
}

func NewMonitorStore(client *ent.Client) *MonitorStore {
	return &MonitorStore{client: client}
}

// CreditsMissingEntry returns transfer IDs of deposits in a post-credit
// state (CREDITED, FINALIZED, REVERSED) that have no credit ledger entry —
// a missed credit, the system's primary correctness invariant.
func (s *MonitorStore) CreditsMissingEntry(ctx context.Context, chainID string) ([]string, error) {
	ids, err := s.client.Deposit.Query().
		Where(
			entdeposit.Chain(chainID),
			entdeposit.StateIn(domain.StateCredited, domain.StateFinalized, domain.StateReversed),
		).
		Select(entdeposit.FieldTransferID).
		Strings(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: post-credit deposits %s: %w", chainID, err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	refs, err := s.client.LedgerEntry.Query().
		Where(ledgerentry.RefIn(ids...), ledgerentry.TypeEQ(ledgerentry.TypeCredit)).
		Select(ledgerentry.FieldRef).
		Strings(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: credit entries %s: %w", chainID, err)
	}
	have := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		have[ref] = struct{}{}
	}
	var missing []string
	for _, id := range ids {
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// LedgerBalanceMismatches compares each (account, asset)'s signed ledger sum
// against the balance projection (ADR 0003's invariant).
//
// The ledger is append-only and unbounded, so the net per (account, asset) is
// aggregated in SQL (credit - debit - reversal) rather than scanned row-by-row
// into memory. The invariant is global: ledger_entry and account_balance carry
// no chain column, so there is nothing to scope the comparison by.
func (s *MonitorStore) LedgerBalanceMismatches(ctx context.Context) ([]monitor.Mismatch, error) {
	// SUM(CASE WHEN type = 'credit' THEN amount ELSE -amount END) AS net
	netPerAccountAsset := func(sel *sql.Selector) string {
		amount := sel.C(ledgerentry.FieldAmount)
		return sql.As(
			fmt.Sprintf("SUM(CASE WHEN %s = '%s' THEN %s ELSE -%s END)",
				sel.C(ledgerentry.FieldType), ledgerentry.TypeCredit, amount, amount),
			"net",
		)
	}
	var ledgerTotals []struct {
		Account string `sql:"account"`
		Asset   string `sql:"asset"`
		Net     string `sql:"net"`
	}
	err := s.client.LedgerEntry.Query().
		GroupBy(ledgerentry.FieldAccount, ledgerentry.FieldAsset).
		Aggregate(netPerAccountAsset).
		Scan(ctx, &ledgerTotals)
	if err != nil {
		return nil, fmt.Errorf("store: ledger totals: %w", err)
	}
	type key struct{ account, asset string }
	totals := make(map[key]*big.Int, len(ledgerTotals))
	for _, r := range ledgerTotals {
		net, ok := new(big.Int).SetString(r.Net, 10)
		if !ok {
			return nil, fmt.Errorf("store: corrupt ledger total %q for %s/%s", r.Net, r.Account, r.Asset)
		}
		totals[key{r.Account, r.Asset}] = net
	}
	balances, err := s.client.AccountBalance.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: balances: %w", err)
	}
	seen := make(map[key]bool)
	var out []monitor.Mismatch
	for _, b := range balances {
		balance, ok := new(big.Int).SetString(b.Balance, 10)
		if !ok {
			return nil, fmt.Errorf("store: corrupt balance %q for %s/%s", b.Balance, b.Account, b.Asset)
		}
		k := key{b.Account, b.Asset}
		seen[k] = true
		ledger := totals[k]
		if ledger == nil {
			ledger = new(big.Int)
		}
		if ledger.Cmp(balance) != 0 {
			out = append(out, monitor.Mismatch{Account: b.Account, Asset: b.Asset, Ledger: ledger, Balance: balance})
		}
	}
	for k, ledger := range totals {
		if !seen[k] && ledger.Sign() != 0 {
			out = append(out, monitor.Mismatch{Account: k.account, Asset: k.asset, Ledger: ledger, Balance: new(big.Int)})
		}
	}
	return out, nil
}

func (s *MonitorStore) ReorgedDeposits(ctx context.Context, chainID string) ([]monitor.Reorged, error) {
	rows, err := s.client.Deposit.Query().
		Where(entdeposit.Chain(chainID), entdeposit.StateEQ(domain.StateReorged)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: reorged deposits %s: %w", chainID, err)
	}
	var out []monitor.Reorged
	for _, r := range rows {
		entry := monitor.Reorged{TransferID: r.TransferID, Asset: r.Asset}
		if r.ReorgedHeight != nil {
			h := uint64(*r.ReorgedHeight)
			entry.Height = &h
		}
		out = append(out, entry)
	}
	return out, nil
}
