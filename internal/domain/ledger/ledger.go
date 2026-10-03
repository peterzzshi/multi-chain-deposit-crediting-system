// Package ledger models the append-only account ledger: credits, debits,
// and reversals as immutable entries in integer base units (ADRs 0002,
// 0003). History is never edited; a reversal is a new compensating entry.
// Functions never mutate their *big.Int arguments.
package ledger

import (
	"errors"
	"fmt"
	"math/big"
)

// Type classifies an entry. Direction is always derived from the type;
// amounts are positive. Values match the ledger_entries.type enum.
type Type string

const (
	TypeCredit   Type = "credit"   // adds funds, e.g. a credited deposit
	TypeDebit    Type = "debit"    // removes funds, e.g. a withdrawal or trade
	TypeReversal Type = "reversal" // undoes a previous credit after a reorg
)

// Entry is one append-only ledger record in integer base units.
type Entry struct {
	Account string
	Asset   string
	Type    Type
	Amount  *big.Int
	// Ref is the unique source reference: the logical transfer ID for a
	// credit, the original entry's reference for its reversal.
	Ref string
}

// ErrInvalidEntry categorizes entry shape violations: empty fields,
// non-positive amounts, unknown types, reversing a non-credit.
var ErrInvalidEntry = errors.New("ledger: invalid entry")

// ErrInsufficientFunds is returned when a debit exceeds the balance. It is
// retryable at the hot-wallet layer after a top-up (ADR 0003).
var ErrInsufficientFunds = errors.New("ledger: insufficient funds")

// newEntry validates the shared invariants of every entry.
func newEntry(account, asset string, typ Type, amount *big.Int, ref string) (Entry, error) {
	if account == "" || asset == "" || ref == "" {
		return Entry{}, fmt.Errorf("%w: account, asset and ref must be non-empty", ErrInvalidEntry)
	}
	if amount == nil || amount.Sign() <= 0 {
		return Entry{}, fmt.Errorf("%w: amount must be positive, got %v", ErrInvalidEntry, amount)
	}
	return Entry{Account: account, Asset: asset, Type: typ, Amount: new(big.Int).Set(amount), Ref: ref}, nil
}

// NewCredit constructs a credit entry linked to its source reference.
func NewCredit(account, asset string, amount *big.Int, ref string) (Entry, error) {
	return newEntry(account, asset, TypeCredit, amount, ref)
}

// NewDebit constructs a debit entry linked to its source reference.
func NewDebit(account, asset string, amount *big.Int, ref string) (Entry, error) {
	return newEntry(account, asset, TypeDebit, amount, ref)
}

// Reverse constructs the compensating entry for an original credit. The
// reversal references the original credit's Ref, so the pair is auditable
// and the reversal itself is unique.
func Reverse(original Entry) (Entry, error) {
	if original.Type != TypeCredit {
		return Entry{}, fmt.Errorf("%w: only credits can be reversed, got %s", ErrInvalidEntry, original.Type)
	}
	return newEntry(original.Account, original.Asset, TypeReversal, original.Amount, original.Ref)
}

// Apply folds an entry into a balance and returns the new balance without
// mutating the input. A nil balance reads as zero.
//
// Debits may not exceed the balance. Reversals may drive it negative when
// the user already spent the credited funds (ADR 0002); flagging the
// account is the caller's decision: match on balance.Sign() directly.
func Apply(balance *big.Int, e Entry) (*big.Int, error) {
	if balance == nil {
		balance = new(big.Int)
	}
	switch e.Type {
	case TypeCredit:
		return new(big.Int).Add(balance, e.Amount), nil
	case TypeDebit:
		if balance.Cmp(e.Amount) < 0 {
			return nil, fmt.Errorf("%w: balance %s, debit %s on %s/%s",
				ErrInsufficientFunds, balance, e.Amount, e.Account, e.Asset)
		}
		return new(big.Int).Sub(balance, e.Amount), nil
	case TypeReversal:
		return new(big.Int).Sub(balance, e.Amount), nil
	}
	return nil, fmt.Errorf("%w: unknown entry type %q", ErrInvalidEntry, e.Type)
}
