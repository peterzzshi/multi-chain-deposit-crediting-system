package store

import (
	"context"
	"fmt"
	"math/big"

	"deposit-crediting/internal/risk"
	"deposit-crediting/internal/store/ent"
	"deposit-crediting/internal/store/ent/accountbalance"
	"deposit-crediting/internal/store/ent/assetconfig"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
	"deposit-crediting/internal/store/ent/exposurestate"
	"deposit-crediting/internal/store/ent/ledgerentry"
)

// RiskStore implements risk.Store over the ent client.
type RiskStore struct {
	client *ent.Client
}

func NewRiskStore(client *ent.Client) *RiskStore {
	return &RiskStore{client: client}
}

func (s *RiskStore) RiskConfigs(ctx context.Context, chainID string) ([]risk.AssetRisk, error) {
	rows, err := s.client.AssetConfig.Query().
		Where(assetconfig.Chain(chainID), assetconfig.ExposureCapNotNil()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: risk configs %s: %w", chainID, err)
	}
	var configs []risk.AssetRisk
	for _, r := range rows {
		capAmount, ok := new(big.Int).SetString(*r.ExposureCap, 10)
		if !ok {
			return nil, fmt.Errorf("store: corrupt exposure cap %q for %s/%s", *r.ExposureCap, chainID, r.Asset)
		}
		configs = append(configs, risk.AssetRisk{Asset: r.Asset, Cap: capAmount})
	}
	return configs, nil
}

func (s *RiskStore) Exposure(ctx context.Context, chainID, asset string) (*big.Int, error) {
	rows, err := s.client.Deposit.Query().
		Where(
			entdeposit.Chain(chainID),
			entdeposit.Asset(asset),
			entdeposit.StateEQ(entdeposit.StateCREDITED),
			entdeposit.Held(false),
		).
		Select(entdeposit.FieldAmount).
		Strings(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: exposure %s/%s: %w", chainID, asset, err)
	}
	total := new(big.Int)
	for _, amount := range rows {
		a, ok := new(big.Int).SetString(amount, 10)
		if !ok {
			return nil, fmt.Errorf("store: corrupt amount %q in exposure %s/%s", amount, chainID, asset)
		}
		total.Add(total, a)
	}
	return total, nil
}

func (s *RiskStore) ExposureState(ctx context.Context, chainID, asset string) (bool, error) {
	row, err := s.client.ExposureState.Query().
		Where(exposurestate.Chain(chainID), exposurestate.Asset(asset)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: exposure state %s/%s: %w", chainID, asset, err)
	}
	return row.HoldsActive, nil
}

func (s *RiskStore) WriteExposure(ctx context.Context, chainID, asset string, exposure *big.Int, holdsActive bool) error {
	n, err := s.client.ExposureState.Update().
		Where(exposurestate.Chain(chainID), exposurestate.Asset(asset)).
		SetExposure(exposure.String()).
		SetHoldsActive(holdsActive).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("store: write exposure %s/%s: %w", chainID, asset, err)
	}
	if n == 0 {
		err = s.client.ExposureState.Create().
			SetChain(chainID).
			SetAsset(asset).
			SetExposure(exposure.String()).
			SetHoldsActive(holdsActive).
			Exec(ctx)
		if err != nil && !ent.IsConstraintError(err) {
			return fmt.Errorf("store: create exposure %s/%s: %w", chainID, asset, err)
		}
	}
	return nil
}

func (s *RiskStore) HeldDeposits(ctx context.Context, chainID, asset string) ([]risk.Held, error) {
	rows, err := s.client.Deposit.Query().
		Where(
			entdeposit.Chain(chainID),
			entdeposit.Asset(asset),
			entdeposit.StateEQ(entdeposit.StateCREDITED),
			entdeposit.Held(true),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: held deposits %s/%s: %w", chainID, asset, err)
	}
	var held []risk.Held
	for _, r := range rows {
		amount, ok := new(big.Int).SetString(r.Amount, 10)
		if !ok {
			return nil, fmt.Errorf("store: corrupt amount %q for %s", r.Amount, r.TransferID)
		}
		held = append(held, risk.Held{TransferID: r.TransferID, Account: r.Account, Asset: r.Asset, Amount: amount})
	}
	return held, nil
}

func (s *RiskStore) ReleaseHold(ctx context.Context, h risk.Held) (err error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("store: begin release tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback() // secondary to the original failure
			return
		}
		if commitErr := tx.Commit(); commitErr != nil {
			err = fmt.Errorf("store: commit release: %w", commitErr)
		}
	}()
	if err = tx.Deposit.Update().
		Where(entdeposit.TransferID(h.TransferID)).
		SetHeld(false).
		Exec(ctx); err != nil {
		return fmt.Errorf("store: release hold on %s: %w", h.TransferID, err)
	}
	row, err := tx.AccountBalance.Query().
		Where(accountbalance.Account(h.Account), accountbalance.Asset(h.Asset)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return fmt.Errorf("store: lock balance %s/%s for release: %w", h.Account, h.Asset, err)
	}
	held, ok := new(big.Int).SetString(row.Held, 10)
	if !ok {
		return fmt.Errorf("store: corrupt held %q for %s/%s", row.Held, h.Account, h.Asset)
	}
	if err = tx.AccountBalance.Update().
		Where(accountbalance.Account(h.Account), accountbalance.Asset(h.Asset)).
		SetHeld(new(big.Int).Sub(held, h.Amount).String()).
		Exec(ctx); err != nil {
		return fmt.Errorf("store: adjust held %s/%s: %w", h.Account, h.Asset, err)
	}
	return nil
}

// CreditsMissingEntry returns transfer IDs of deposits in a post-credit
// state (CREDITED, FINALIZED, REVERSED) that have no credit ledger entry —
// a missed credit, the system's primary correctness invariant.
func (s *RiskStore) CreditsMissingEntry(ctx context.Context, chainID string) ([]string, error) {
	ids, err := s.client.Deposit.Query().
		Where(
			entdeposit.Chain(chainID),
			entdeposit.StateIn(entdeposit.StateCREDITED, entdeposit.StateFINALIZED, entdeposit.StateREVERSED),
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

// LedgerBalanceMismatches replays the ledger per (account, asset) and
// compares it against the balance projection (ADR 0003's invariant).
func (s *RiskStore) LedgerBalanceMismatches(ctx context.Context) ([]risk.Mismatch, error) {
	entries, err := s.client.LedgerEntry.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: replay entries: %w", err)
	}
	type key struct{ account, asset string }
	totals := make(map[key]*big.Int)
	for _, e := range entries {
		amount, ok := new(big.Int).SetString(e.Amount, 10)
		if !ok {
			return nil, fmt.Errorf("store: corrupt entry amount %q for %s", e.Amount, e.Ref)
		}
		k := key{e.Account, e.Asset}
		if _, ok := totals[k]; !ok {
			totals[k] = new(big.Int)
		}
		switch e.Type {
		case ledgerentry.TypeCredit:
			totals[k].Add(totals[k], amount)
		case ledgerentry.TypeDebit, ledgerentry.TypeReversal:
			totals[k].Sub(totals[k], amount)
		}
	}
	balances, err := s.client.AccountBalance.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: balances: %w", err)
	}
	seen := make(map[key]bool)
	var out []risk.Mismatch
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
			out = append(out, risk.Mismatch{Account: b.Account, Asset: b.Asset, Ledger: ledger, Balance: balance})
		}
	}
	for k, ledger := range totals {
		if !seen[k] && ledger.Sign() != 0 {
			out = append(out, risk.Mismatch{Account: k.account, Asset: k.asset, Ledger: ledger, Balance: new(big.Int)})
		}
	}
	return out, nil
}

func (s *RiskStore) ReorgedDeposits(ctx context.Context, chainID string) ([]risk.Reorged, error) {
	rows, err := s.client.Deposit.Query().
		Where(entdeposit.Chain(chainID), entdeposit.StateEQ(entdeposit.StateREORGED)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: reorged deposits %s: %w", chainID, err)
	}
	var out []risk.Reorged
	for _, r := range rows {
		entry := risk.Reorged{TransferID: r.TransferID, Asset: r.Asset}
		if r.ReorgedHeight != nil {
			h := uint64(*r.ReorgedHeight)
			entry.Height = &h
		}
		out = append(out, entry)
	}
	return out, nil
}

func (s *RiskStore) Windows(ctx context.Context, chainID string) (map[string]uint64, error) {
	rows, err := s.client.AssetConfig.Query().Where(assetconfig.Chain(chainID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: reorg windows %s: %w", chainID, err)
	}
	windows := make(map[string]uint64, len(rows))
	for _, r := range rows {
		if w := uint64(r.ReorgWindow); w > windows[r.Asset] {
			windows[r.Asset] = w
		}
	}
	return windows, nil
}
