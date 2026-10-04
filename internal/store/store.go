// Package store implements credit.Store and credit.Tx over the ent client:
// no business rules, only translation between domain values and rows, and
// serialization at the database boundary (ADR 0003).
package store

import (
	"context"
	"fmt"
	"math/big"

	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/domain/deposit"
	"deposit-crediting/internal/domain/ledger"
	"deposit-crediting/internal/errs"
	"deposit-crediting/internal/store/ent"
	"deposit-crediting/internal/store/ent/accountbalance"
	"deposit-crediting/internal/store/ent/assetconfig"
	entdeposit "deposit-crediting/internal/store/ent/deposit"
	"deposit-crediting/internal/store/ent/exposurestate"
	"deposit-crediting/internal/store/ent/ledgerentry"
)

type Store struct {
	client *ent.Client
}

func New(client *ent.Client) *Store {
	return &Store{client: client}
}

// InTx commits on success and rolls back on any error.
func (s *Store) InTx(ctx context.Context, fn func(context.Context, credit.Tx) error) (err error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback() // secondary to the original failure
			return
		}
		if commitErr := tx.Commit(); commitErr != nil {
			err = fmt.Errorf("store: commit: %w", commitErr)
		}
	}()
	return fn(ctx, &Tx{tx: tx})
}

type Tx struct {
	tx *ent.Tx
}

func (t *Tx) DepositForUpdate(ctx context.Context, transferID string) (credit.View, error) {
	row, err := t.tx.Deposit.Query().
		Where(entdeposit.TransferID(transferID)).
		ForUpdate().
		Only(ctx)
	if ent.IsNotFound(err) {
		return credit.View{}, errs.ErrDepositNotFound
	}
	if err != nil {
		return credit.View{}, fmt.Errorf("store: load deposit %s: %w", transferID, err)
	}
	amount, ok := new(big.Int).SetString(row.Amount, 10)
	if !ok {
		return credit.View{}, fmt.Errorf("store: corrupt amount %q for %s", row.Amount, transferID)
	}
	return credit.View{
		State:       deposit.State(row.State),
		Chain:       row.Chain,
		Account:     row.Account,
		Asset:       row.Asset,
		Amount:      amount,
		Held:        row.Held,
		CreditCycle: row.CreditCycle,
	}, nil
}

func (t *Tx) SetReorgedHeight(ctx context.Context, transferID string, height uint64) error {
	if err := t.tx.Deposit.Update().
		Where(entdeposit.TransferID(transferID)).
		SetReorgedHeight(int64(height)).
		Exec(ctx); err != nil {
		return fmt.Errorf("store: set reorged height %s: %w", transferID, err)
	}
	return nil
}

func (t *Tx) SetCreditCycle(ctx context.Context, transferID string, cycle int) error {
	if err := t.tx.Deposit.Update().
		Where(entdeposit.TransferID(transferID)).
		SetCreditCycle(cycle).
		Exec(ctx); err != nil {
		return fmt.Errorf("store: set credit cycle %s: %w", transferID, err)
	}
	return nil
}

func (t *Tx) SetDepositState(ctx context.Context, transferID string, state deposit.State) error {
	n, err := t.tx.Deposit.Update().
		Where(entdeposit.TransferID(transferID)).
		SetState(entdeposit.State(state)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("store: set state of %s: %w", transferID, err)
	}
	if n == 0 {
		return errs.ErrDepositNotFound
	}
	return nil
}

func (t *Tx) InsertEntry(ctx context.Context, e ledger.Entry) error {
	_, err := t.tx.LedgerEntry.Create().
		SetAccount(e.Account).
		SetAsset(e.Asset).
		SetType(ledgerentry.Type(e.Type)).
		SetAmount(e.Amount.String()).
		SetRef(e.Ref).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return fmt.Errorf("%w: %s", errs.ErrDuplicateRef, e.Ref)
	}
	if err != nil {
		return fmt.Errorf("store: insert entry %s: %w", e.Ref, err)
	}
	return nil
}

func (t *Tx) HasEntry(ctx context.Context, ref string) (bool, error) {
	n, err := t.tx.LedgerEntry.Query().
		Where(ledgerentry.Ref(ref)).
		Limit(1).
		Count(ctx)
	if err != nil {
		return false, fmt.Errorf("store: query entry %s: %w", ref, err)
	}
	return n > 0, nil
}

// BalanceForUpdate creates a missing row first, so the lock has a row to
// hold and first-touch is serialized like every later update.
func (t *Tx) BalanceForUpdate(ctx context.Context, account, asset string) (credit.Balance, error) {
	row, err := t.lockBalance(ctx, account, asset)
	if ent.IsNotFound(err) {
		cerr := t.tx.AccountBalance.Create().
			SetAccount(account).
			SetAsset(asset).
			SetBalance("0").
			SetHeld("0").
			Exec(ctx)
		if cerr != nil && !ent.IsConstraintError(cerr) {
			return credit.Balance{}, fmt.Errorf("store: create balance %s/%s: %w", account, asset, cerr)
		}
		row, err = t.lockBalance(ctx, account, asset)
	}
	if err != nil {
		return credit.Balance{}, fmt.Errorf("store: load balance %s/%s: %w", account, asset, err)
	}
	balance, ok := new(big.Int).SetString(row.Balance, 10)
	if !ok {
		return credit.Balance{}, fmt.Errorf("store: corrupt balance %q for %s/%s", row.Balance, account, asset)
	}
	held, ok := new(big.Int).SetString(row.Held, 10)
	if !ok {
		return credit.Balance{}, fmt.Errorf("store: corrupt held %q for %s/%s", row.Held, account, asset)
	}
	return credit.Balance{Amount: balance, Held: held, Flagged: row.Flagged}, nil
}

func (t *Tx) lockBalance(ctx context.Context, account, asset string) (*ent.AccountBalance, error) {
	return t.tx.AccountBalance.Query().
		Where(accountbalance.Account(account), accountbalance.Asset(asset)).
		ForUpdate().
		Only(ctx)
}

func (t *Tx) SetBalance(ctx context.Context, account, asset string, balance *big.Int, flagged bool) error {
	n, err := t.tx.AccountBalance.Update().
		Where(accountbalance.Account(account), accountbalance.Asset(asset)).
		SetBalance(balance.String()).
		SetFlagged(flagged).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("store: set balance %s/%s: %w", account, asset, err)
	}
	if n == 0 {
		return fmt.Errorf("store: set balance %s/%s: no row, BalanceForUpdate must run first", account, asset)
	}
	return nil
}

func (t *Tx) HoldsActive(ctx context.Context, chainID, asset string) (bool, *big.Int, error) {
	row, err := t.tx.ExposureState.Query().
		Where(exposurestate.Chain(chainID), exposurestate.Asset(asset)).
		Only(ctx)
	if ent.IsNotFound(err) || !row.HoldsActive {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("store: exposure state %s/%s: %w", chainID, asset, err)
	}
	// Holds are active: the tier comes from the asset config; a missing
	// config means no tier (hold everything — the conservative reading).
	cfg, err := t.tx.AssetConfig.Query().
		Where(assetconfig.Chain(chainID), assetconfig.Asset(asset)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return true, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("store: asset config %s/%s: %w", chainID, asset, err)
	}
	if cfg.TierAmount == nil {
		return true, nil, nil
	}
	tier, ok := new(big.Int).SetString(*cfg.TierAmount, 10)
	if !ok {
		return false, nil, fmt.Errorf("store: corrupt tier amount %q for %s/%s", *cfg.TierAmount, chainID, asset)
	}
	return true, tier, nil
}

func (t *Tx) SetHeld(ctx context.Context, transferID, account, asset string, amount *big.Int) error {
	return t.adjustHold(ctx, transferID, account, asset, amount, true)
}

func (t *Tx) ClearHeld(ctx context.Context, transferID, account, asset string, amount *big.Int) error {
	return t.adjustHold(ctx, transferID, account, asset, new(big.Int).Neg(amount), false)
}

func (t *Tx) adjustHold(ctx context.Context, transferID, account, asset string, delta *big.Int, held bool) error {
	if err := t.tx.Deposit.Update().
		Where(entdeposit.TransferID(transferID)).
		SetHeld(held).
		Exec(ctx); err != nil {
		return fmt.Errorf("store: set held=%v on %s: %w", held, transferID, err)
	}
	row, err := t.lockBalance(ctx, account, asset)
	if err != nil {
		return fmt.Errorf("store: lock balance %s/%s for hold: %w", account, asset, err)
	}
	current, ok := new(big.Int).SetString(row.Held, 10)
	if !ok {
		return fmt.Errorf("store: corrupt held %q for %s/%s", row.Held, account, asset)
	}
	if err := t.tx.AccountBalance.Update().
		Where(accountbalance.Account(account), accountbalance.Asset(asset)).
		SetHeld(new(big.Int).Add(current, delta).String()).
		Exec(ctx); err != nil {
		return fmt.Errorf("store: adjust held %s/%s: %w", account, asset, err)
	}
	return nil
}
