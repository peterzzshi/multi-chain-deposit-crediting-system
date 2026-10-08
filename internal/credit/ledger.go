package credit

import (
	"fmt"
	"math/big"

	"deposit-crediting/internal/errs"
)

type TransactionType string

const (
	Credit   TransactionType = "credit"
	Debit    TransactionType = "debit"
	Reversal TransactionType = "reversal"
)

type Entry struct {
	Account string
	Asset   string
	Type    TransactionType
	Amount  *big.Int
	Ref     string
	// Set only on a reversal: the ref of the credit it compensates.
	ReversesRef string
}

func New(transactionType TransactionType, account, asset string, amount *big.Int, ref string) (Entry, error) {
	switch transactionType {
	case Credit, Debit, Reversal:
	default:
		return Entry{}, fmt.Errorf("%w: unknown transaction type %q", errs.ErrInvalidEntry, transactionType)
	}
	if account == "" || asset == "" || ref == "" {
		return Entry{}, fmt.Errorf("%w: account, asset and ref must be non-empty", errs.ErrInvalidEntry)
	}
	if amount == nil || amount.Sign() <= 0 {
		return Entry{}, fmt.Errorf("%w: amount must be positive, got %v", errs.ErrInvalidEntry, amount)
	}
	return Entry{Account: account, Asset: asset, Type: transactionType, Amount: new(big.Int).Set(amount), Ref: ref}, nil
}

// reversalRef derives the reversal's own ref. The prefix is required, not
// cosmetic: ref is unique, so a reversal cannot reuse the credit's ref. The
// reader queries ReversesRef, so breaking this format fails on the unique
// constraint instead of silently missing the pairing.
func reversalRef(ref string) string {
	return "reversal:" + ref
}

// Reverse builds the compensating entry for a credit. The derived ref makes
// re-applying a reversal a no-op; ReversesRef records the pairing.
func Reverse(original Entry) (Entry, error) {
	if original.Type != Credit {
		return Entry{}, fmt.Errorf("%w: only credits can be reversed, got %s", errs.ErrInvalidEntry, original.Type)
	}
	entry, err := New(Reversal, original.Account, original.Asset, original.Amount, reversalRef(original.Ref))
	if err != nil {
		return Entry{}, err
	}
	entry.ReversesRef = original.Ref
	return entry, nil
}

// Apply folds an entry into a balance without mutating the input; a nil
// balance reads as zero. Debits may not exceed the balance; reversals may
// drive it negative (ADR 0002) — flagging is the caller's decision.
func Apply(balance *big.Int, e Entry) (*big.Int, error) {
	if balance == nil {
		balance = new(big.Int)
	}
	switch e.Type {
	case Credit:
		return new(big.Int).Add(balance, e.Amount), nil
	case Debit:
		if balance.Cmp(e.Amount) < 0 {
			return nil, fmt.Errorf("%w: balance %s, debit %s on %s/%s",
				errs.ErrInsufficientFunds, balance, e.Amount, e.Account, e.Asset)
		}
		return new(big.Int).Sub(balance, e.Amount), nil
	case Reversal:
		return new(big.Int).Sub(balance, e.Amount), nil
	}
	return nil, fmt.Errorf("%w: unknown transaction type %q", errs.ErrInvalidEntry, e.Type)
}
