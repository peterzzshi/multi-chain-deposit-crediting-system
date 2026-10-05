package credit

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"
)

type View struct {
	State       domain.State
	Chain       string
	Account     string
	Asset       string
	Amount      *big.Int
	Held        bool
	CreditCycle int
}

type Balance struct {
	Amount  *big.Int
	Held    *big.Int
	Flagged bool
}

type Tx interface {
	DepositForUpdate(ctx context.Context, transferID string) (View, error)
	SetDepositState(ctx context.Context, transferID string, state domain.State) error
	InsertEntry(ctx context.Context, e domain.Entry) error
	HasEntry(ctx context.Context, ref string) (bool, error)
	BalanceForUpdate(ctx context.Context, account, asset string) (Balance, error)
	SetBalance(ctx context.Context, account, asset string, balance *big.Int, flagged bool) error
	HoldsActive(ctx context.Context, chain, asset string) (active bool, tier *big.Int, err error)
	SetHeld(ctx context.Context, transferID, account, asset string, amount *big.Int) error
	ClearHeld(ctx context.Context, transferID, account, asset string, amount *big.Int) error
	SetReorgedHeight(ctx context.Context, transferID string, height uint64) error
	SetCreditCycle(ctx context.Context, transferID string, cycle int) error
}

type Store interface {
	InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

type Engine struct {
	store Store
}

func NewEngine(s Store) *Engine {
	return &Engine{store: s}
}

const recreditPrefix = "recredit:"

func (e *Engine) Apply(ctx context.Context, transferID string, ev domain.Event) error {
	return e.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		dep, err := tx.DepositForUpdate(ctx, transferID)
		if err != nil {
			return fmt.Errorf("credit: load deposit: %w", err)
		}
		next, effect, err := domain.Transition(dep.State, ev)
		if err != nil {
			return fmt.Errorf("credit: apply %s to %s: %w", ev, transferID, err)
		}
		switch effect {
		case domain.EffectCredit:
			if err := creditFunds(ctx, tx, dep, transferID); err != nil {
				return err
			}
		case domain.EffectReverse:
			original, err := domain.New(domain.Credit, dep.Account, dep.Asset, dep.Amount, creditRef(transferID, dep.CreditCycle))
			if err != nil {
				return fmt.Errorf("credit: rebuild credit: %w", err)
			}
			reversal, err := domain.Reverse(original)
			if err != nil {
				return fmt.Errorf("credit: build reversal: %w", err)
			}
			if err := post(ctx, tx, reversal); err != nil {
				return err
			}
			if dep.Held {
				// Reversal must release the hold projection.
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

// ApplyReorg atomically marks a deposit REORGED and starts its reorg window.
func (e *Engine) ApplyReorg(ctx context.Context, transferID string, head uint64) error {
	return e.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		dep, err := tx.DepositForUpdate(ctx, transferID)
		if err != nil {
			return fmt.Errorf("credit: load deposit: %w", err)
		}
		next, _, err := domain.Transition(dep.State, domain.EventReorgedOut)
		if err != nil {
			return fmt.Errorf("credit: reorg out %s: %w", transferID, err)
		}
		if err := tx.SetReorgedHeight(ctx, transferID, head); err != nil {
			return fmt.Errorf("credit: mark reorged %s: %w", transferID, err)
		}
		if err := tx.SetDepositState(ctx, transferID, next); err != nil {
			return fmt.Errorf("credit: set state %s: %w", next, err)
		}
		return nil
	})
}

// creditRef returns the ledger reference for a credit cycle.
func creditRef(transferID string, cycle int) string {
	if cycle == 0 {
		return transferID
	}
	return fmt.Sprintf("%s%s:%d", recreditPrefix, transferID, cycle)
}

// creditFunds applies an idempotent credit, creating a new cycle after reversal.
func creditFunds(ctx context.Context, tx Tx, dep View, transferID string) error {
	credited, err := tx.HasEntry(ctx, transferID)
	if err != nil {
		return fmt.Errorf("credit: check entry %s: %w", transferID, err)
	}
	if !credited {
		if err := postNew(ctx, tx, domain.Credit, dep, transferID); err != nil {
			return err
		}
		return holdIfCapped(ctx, tx, dep, transferID)
	}
	latest := creditRef(transferID, dep.CreditCycle)
	reversed, err := tx.HasEntry(ctx, "reversal:"+latest)
	if err != nil {
		return fmt.Errorf("credit: check reversal %s: %w", latest, err)
	}
	if !reversed {
		return nil
	}
	cycle := dep.CreditCycle + 1
	if err := postNew(ctx, tx, domain.Credit, dep, creditRef(transferID, cycle)); errors.Is(err, errs.ErrDuplicateRef) {
		return nil // re-credit already posted
	} else if err != nil {
		return err
	}
	if err := tx.SetCreditCycle(ctx, transferID, cycle); err != nil {
		return fmt.Errorf("credit: set credit cycle: %w", err)
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
	entry, err := domain.New(domain.Debit, account, asset, amount, ref)
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
		if _, err := domain.Apply(available, entry); err != nil {
			return err
		}
		next, err := domain.Apply(bal.Amount, entry) // safe: balance >= available >= amount
		if err != nil {
			return err
		}
		if err := tx.SetBalance(ctx, account, asset, next, next.Sign() < 0); err != nil {
			return fmt.Errorf("credit: set balance: %w", err)
		}
		return nil
	})
}

func postNew(ctx context.Context, tx Tx, typ domain.TransactionType, dep View, ref string) error {
	entry, err := domain.New(typ, dep.Account, dep.Asset, dep.Amount, ref)
	if err != nil {
		return fmt.Errorf("credit: build entry: %w", err)
	}
	return post(ctx, tx, entry)
}

// post appends an entry and updates the balance projection.
func post(ctx context.Context, tx Tx, entry domain.Entry) error {
	if err := tx.InsertEntry(ctx, entry); err != nil {
		return fmt.Errorf("credit: insert %s %s: %w", entry.Type, entry.Ref, err)
	}
	bal, err := tx.BalanceForUpdate(ctx, entry.Account, entry.Asset)
	if err != nil {
		return fmt.Errorf("credit: load balance: %w", err)
	}
	next, err := domain.Apply(bal.Amount, entry)
	if err != nil {
		return err
	}
	if err := tx.SetBalance(ctx, entry.Account, entry.Asset, next, next.Sign() < 0); err != nil {
		return fmt.Errorf("credit: set balance: %w", err)
	}
	return nil
}
