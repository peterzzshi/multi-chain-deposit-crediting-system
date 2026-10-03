// Package credit contains the crediting engine, the single ledger writer
// (ADR 0003): state transition, ledger effect, and balance projection
// commit in one transaction. Decisions live in the pure domain packages;
// this package orchestrates persistence.
package credit

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"deposit-crediting/internal/domain/deposit"
	"deposit-crediting/internal/domain/ledger"
	"deposit-crediting/internal/errs"
)

// View is the engine's read model of a deposit row.
type View struct {
	State   deposit.State
	Chain   string
	Account string
	Asset   string
	Amount  *big.Int
	Held    bool
}

// Balance is the locked account balance projection. Spendable funds are
// Amount - Held; Held is the exposure-cap hold portion (risk-policy §4).
type Balance struct {
	Amount  *big.Int
	Held    *big.Int
	Flagged bool
}

// Tx is the engine's view of one database transaction. Implementations
// serialize per (account, asset), e.g. with a row lock on account_balances
// (ADR 0003).
type Tx interface {
	DepositForUpdate(ctx context.Context, transferID string) (View, error)
	SetDepositState(ctx context.Context, transferID string, state deposit.State) error
	// InsertEntry fails with errs.ErrDuplicateRef when the ref exists.
	InsertEntry(ctx context.Context, e ledger.Entry) error
	HasEntry(ctx context.Context, ref string) (bool, error)
	// A missing balance row reads as zeroed Balance.
	BalanceForUpdate(ctx context.Context, account, asset string) (Balance, error)
	SetBalance(ctx context.Context, account, asset string, balance *big.Int, flagged bool) error
	// HoldsActive reports whether the exposure monitor is holding new
	// credits for (chain, asset), and the tier above which holds apply
	// (nil tier = hold all credits while active).
	HoldsActive(ctx context.Context, chain, asset string) (active bool, tier *big.Int, err error)
	// SetHeld marks a credited deposit non-spendable and adds the amount
	// to the account's held projection.
	SetHeld(ctx context.Context, transferID, account, asset string, amount *big.Int) error
	// ClearHeld releases a held deposit: the deposit is spendable (or
	// gone) and the amount leaves the held projection.
	ClearHeld(ctx context.Context, transferID, account, asset string, amount *big.Int) error
}

// Store is the persistence boundary the engine needs; implemented by
// internal/store. Consumers never see ent types.
type Store interface {
	InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Engine is stateless; all serialization happens at the database boundary.
type Engine struct {
	store Store
}

func NewEngine(s Store) *Engine {
	return &Engine{store: s}
}

// Apply commits one deposit event's state change and ledger effect
// atomically. Redeliveries are no-ops (the machine maps them to identity
// transitions), so callers may retry freely. Opening a deposit
// (EventObserved / EventObservedBelowMinimum) is the ingest layer's job.
func (e *Engine) Apply(ctx context.Context, transferID string, ev deposit.Event) error {
	return e.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		dep, err := tx.DepositForUpdate(ctx, transferID)
		if err != nil {
			return fmt.Errorf("credit: load deposit: %w", err)
		}
		next, effect, err := deposit.Transition(dep.State, ev)
		if err != nil {
			return fmt.Errorf("credit: apply %s to %s: %w", ev, transferID, err)
		}
		switch effect {
		case deposit.EffectCredit:
			if err := creditFunds(ctx, tx, dep, transferID); err != nil {
				return err
			}
		case deposit.EffectReverse:
			original, err := ledger.New(ledger.Credit, dep.Account, dep.Asset, dep.Amount, transferID)
			if err != nil {
				return fmt.Errorf("credit: rebuild credit: %w", err)
			}
			reversal, err := ledger.Reverse(original)
			if err != nil {
				return fmt.Errorf("credit: build reversal: %w", err)
			}
			if err := post(ctx, tx, reversal); err != nil {
				return err
			}
			if dep.Held {
				// A held deposit that is reversed must release its hold,
				// or the held projection leaks.
				if err := tx.ClearHeld(ctx, transferID, dep.Account, dep.Asset, dep.Amount); err != nil {
					return err
				}
			}
		}
		if err := tx.SetDepositState(ctx, transferID, next); err != nil {
			return fmt.Errorf("credit: set state %s: %w", next, err)
		}
		return nil
	})
}

// creditFunds executes EffectCredit. A re-included transfer passes PENDING
// again: an intact credit is a no-op; a reversed one opens a new credit
// cycle, restoring the funds under a deterministic re-credit ref (ADR 0005).
// Credits always post — an active exposure cap only holds spendability
// (risk-policy §4).
func creditFunds(ctx context.Context, tx Tx, dep View, transferID string) error {
	credited, err := tx.HasEntry(ctx, transferID)
	if err != nil {
		return fmt.Errorf("credit: check entry %s: %w", transferID, err)
	}
	if !credited {
		if err := postNew(ctx, tx, ledger.Credit, dep, transferID); err != nil {
			return err
		}
		return holdIfCapped(ctx, tx, dep, transferID)
	}
	reversed, err := tx.HasEntry(ctx, "reversal:"+transferID)
	if err != nil {
		return fmt.Errorf("credit: check reversal %s: %w", transferID, err)
	}
	if !reversed {
		return nil
	}
	err = postNew(ctx, tx, ledger.Credit, dep, "recredit:"+transferID)
	if errors.Is(err, errs.ErrDuplicateRef) {
		return nil // re-credit already posted
	}
	if err != nil {
		return err
	}
	return holdIfCapped(ctx, tx, dep, transferID)
}

func holdIfCapped(ctx context.Context, tx Tx, dep View, transferID string) error {
	active, tier, err := tx.HoldsActive(ctx, dep.Chain, dep.Asset)
	if err != nil {
		return fmt.Errorf("credit: exposure holds %s/%s: %w", dep.Chain, dep.Asset, err)
	}
	if !active || (tier != nil && dep.Amount.Cmp(tier) < 0) {
		return nil
	}
	return tx.SetHeld(ctx, transferID, dep.Account, dep.Asset, dep.Amount)
}

// Debit posts an external debit (withdrawal, trade) against spendable
// funds (balance - held). Ref is the idempotency key: retrying with the
// same ref is a silent no-op.
func (e *Engine) Debit(ctx context.Context, account, asset string, amount *big.Int, ref string) error {
	entry, err := ledger.New(ledger.Debit, account, asset, amount, ref)
	if err != nil {
		return fmt.Errorf("credit: build debit: %w", err)
	}
	return e.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		bal, err := tx.BalanceForUpdate(ctx, account, asset)
		if err != nil {
			return fmt.Errorf("credit: load balance: %w", err)
		}
		if bal.Flagged {
			return fmt.Errorf("%w: %s/%s", errs.ErrAccountFlagged, account, asset)
		}
		if err := tx.InsertEntry(ctx, entry); errors.Is(err, errs.ErrDuplicateRef) {
			return nil
		} else if err != nil {
			return fmt.Errorf("credit: insert debit %s: %w", entry.Ref, err)
		}
		available := new(big.Int).Sub(bal.Amount, bal.Held)
		if _, err := ledger.Apply(available, entry); err != nil {
			return err
		}
		next, err := ledger.Apply(bal.Amount, entry) // safe: balance >= available >= amount
		if err != nil {
			return err
		}
		if err := tx.SetBalance(ctx, account, asset, next, next.Sign() < 0); err != nil {
			return fmt.Errorf("credit: set balance: %w", err)
		}
		return nil
	})
}

func postNew(ctx context.Context, tx Tx, typ ledger.TransactionType, dep View, ref string) error {
	entry, err := ledger.New(typ, dep.Account, dep.Asset, dep.Amount, ref)
	if err != nil {
		return fmt.Errorf("credit: build entry: %w", err)
	}
	return post(ctx, tx, entry)
}

// post appends an entry and folds it into the balance; the account flag
// follows the balance sign (ADR 0002).
func post(ctx context.Context, tx Tx, entry ledger.Entry) error {
	if err := tx.InsertEntry(ctx, entry); err != nil {
		return fmt.Errorf("credit: insert %s %s: %w", entry.Type, entry.Ref, err)
	}
	bal, err := tx.BalanceForUpdate(ctx, entry.Account, entry.Asset)
	if err != nil {
		return fmt.Errorf("credit: load balance: %w", err)
	}
	next, err := ledger.Apply(bal.Amount, entry)
	if err != nil {
		return err
	}
	if err := tx.SetBalance(ctx, entry.Account, entry.Asset, next, next.Sign() < 0); err != nil {
		return fmt.Errorf("credit: set balance: %w", err)
	}
	return nil
}
