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
	Account string
	Asset   string
	Amount  *big.Int
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
	// A missing balance row reads as (zero, false).
	BalanceForUpdate(ctx context.Context, account, asset string) (balance *big.Int, flagged bool, err error)
	SetBalance(ctx context.Context, account, asset string, balance *big.Int, flagged bool) error
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
func creditFunds(ctx context.Context, tx Tx, dep View, transferID string) error {
	credited, err := tx.HasEntry(ctx, transferID)
	if err != nil {
		return fmt.Errorf("credit: check entry %s: %w", transferID, err)
	}
	if !credited {
		return postNew(ctx, tx, ledger.Credit, dep, transferID)
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
	return err
}

// Debit posts an external debit (withdrawal, trade). Ref is the
// idempotency key: retrying with the same ref is a silent no-op.
func (e *Engine) Debit(ctx context.Context, account, asset string, amount *big.Int, ref string) error {
	entry, err := ledger.New(ledger.Debit, account, asset, amount, ref)
	if err != nil {
		return fmt.Errorf("credit: build debit: %w", err)
	}
	return e.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		_, flagged, err := tx.BalanceForUpdate(ctx, account, asset)
		if err != nil {
			return fmt.Errorf("credit: load balance: %w", err)
		}
		if flagged {
			return fmt.Errorf("%w: %s/%s", errs.ErrAccountFlagged, account, asset)
		}
		err = post(ctx, tx, entry)
		if errors.Is(err, errs.ErrDuplicateRef) {
			return nil
		}
		return err
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
	balance, _, err := tx.BalanceForUpdate(ctx, entry.Account, entry.Asset)
	if err != nil {
		return fmt.Errorf("credit: load balance: %w", err)
	}
	next, err := ledger.Apply(balance, entry)
	if err != nil {
		return err
	}
	if err := tx.SetBalance(ctx, entry.Account, entry.Asset, next, next.Sign() < 0); err != nil {
		return fmt.Errorf("credit: set balance: %w", err)
	}
	return nil
}
